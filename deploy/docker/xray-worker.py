#!/usr/bin/env python3
"""Перезапускать Xray, когда API атомарно заменяет общий файл конфигурации."""

import hashlib
import json
import math
import os
import pathlib
import signal
import socket
import subprocess
import tempfile
import time


CONFIG = pathlib.Path("/state/xray.json")
STATUS = pathlib.Path("/state/xray.status.json")
METRICS = pathlib.Path("/state/xray.metrics.json")
running = True
process = None


def snapshot():
    try:
        with CONFIG.open("rb") as source:
            data = source.read()
            stat = os.fstat(source.fileno())
    except FileNotFoundError:
        return None
    return {"mtime_ns": stat.st_mtime_ns, "sha256": hashlib.sha256(data).hexdigest()}


def write_document(path, document):
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent,
                                         prefix=f".{path.stem}-", delete=False) as output:
            temporary = pathlib.Path(output.name)
            json.dump(document, output, separators=(",", ":"), allow_nan=False)
            output.write("\n")
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def write_status(state, active, error=""):
    document = dict(state or {"mtime_ns": 0, "sha256": ""})
    document.update(running=active, error=error)
    write_document(STATUS, document)


def read_metric(path):
    try:
        with path.open(encoding="utf-8") as source:
            data = source.read(65537)
        return data.strip() if len(data) <= 65536 else ""
    except (OSError, UnicodeError):
        return ""


def metric_uint(value):
    try:
        # Формат proc/cgroup — неотрицательные десятичные целые, без знака.
        if not isinstance(value, str) or not value or not value.isascii() or not value.isdecimal():
            return None
        result = int(value)
        return result if result <= 18446744073709551615 else None
    except (ValueError, TypeError):
        return None


def metric_float(value):
    try:
        result = float(value)
        return result if result >= 0 and math.isfinite(result) else None
    except (ValueError, TypeError):
        return None


def metric_pairs(data):
    return {parts[0]: parts[1] for line in data.splitlines()
            if len(parts := line.split()) == 2}


def cgroup_metrics(proc, cgroup):
    result = dict.fromkeys(("memoryBytes", "memoryLimitBytes", "cpuQuotaCores",
                            "cpuUsageUsec", "cpuThrottledUsec", "cpuThrottledPeriods"))
    groups = {}
    for line in read_metric(proc / "self/cgroup").splitlines():
        parts = line.split(":", 2)
        if len(parts) == 3 and parts[2].startswith("/") and ".." not in parts[2]:
            for controller in parts[1].split(","):
                groups[controller] = parts[2].lstrip("/")

    # Родительские счётчики не заменяют недоступную собственную группу.
    if "" in groups:
        root = cgroup / groups[""]
        result["memoryBytes"] = metric_uint(read_metric(root / "memory.current"))
        result["memoryLimitBytes"] = metric_uint(read_metric(root / "memory.max"))
        maximum = read_metric(root / "cpu.max").split()
        if len(maximum) == 2:
            quota, period = (metric_float(value) for value in maximum)
            if quota is not None and period is not None and quota > 0 and period > 0:
                result["cpuQuotaCores"] = quota / period
        stats = metric_pairs(read_metric(root / "cpu.stat"))
        for field, key in (("cpuUsageUsec", "usage_usec"),
                           ("cpuThrottledUsec", "throttled_usec"),
                           ("cpuThrottledPeriods", "nr_throttled")):
            result[field] = metric_uint(stats.get(key))
        return result

    def controller_root(controller, probe):
        if controller not in groups:
            return None
        for mount in (controller, "cpu,cpuacct", "cpuacct,cpu"):
            base = cgroup / mount / groups[controller]
            if read_metric(base / probe):
                return base
        return None

    def controller_read(root, name):
        return read_metric(root / name) if root is not None else ""

    memory = controller_root("memory", "memory.usage_in_bytes")
    result["memoryBytes"] = metric_uint(controller_read(memory, "memory.usage_in_bytes"))
    maximum = metric_uint(controller_read(memory, "memory.limit_in_bytes"))
    result["memoryLimitBytes"] = maximum if maximum is not None and maximum < 1 << 60 else None
    cpu = controller_root("cpu", "cpu.cfs_period_us")
    quota = metric_float(controller_read(cpu, "cpu.cfs_quota_us"))
    period = metric_float(controller_read(cpu, "cpu.cfs_period_us"))
    if quota is not None and period is not None and quota > 0 and period > 0:
        result["cpuQuotaCores"] = quota / period
    stats = metric_pairs(controller_read(cpu, "cpu.stat"))
    result["cpuThrottledPeriods"] = metric_uint(stats.get("nr_throttled"))
    nanos = metric_uint(stats.get("throttled_time"))
    result["cpuThrottledUsec"] = nanos // 1000 if nanos is not None else None
    accounting = controller_root("cpuacct", "cpuacct.usage")
    nanos = metric_uint(controller_read(accounting, "cpuacct.usage"))
    result["cpuUsageUsec"] = nanos // 1000 if nanos is not None else None
    return result


def network_metrics(proc):
    counters = []
    for line in read_metric(proc / "net/dev").splitlines():
        if ":" not in line:
            continue
        name, data = line.split(":", 1)
        if name.strip() == "lo":
            continue
        values = data.split()
        if len(values) != 16:
            return None
        received, sent = metric_uint(values[0]), metric_uint(values[8])
        if received is None or sent is None:
            return None
        counters.append((received, sent))
    if not counters:
        return None
    received, sent = sum(pair[0] for pair in counters), sum(pair[1] for pair in counters)
    if max(received, sent) > 18446744073709551615:
        return None
    return {"rxBytes": received, "txBytes": sent}


def tcp_metrics(proc):
    lines = read_metric(proc / "net/snmp").splitlines()
    for index, line in enumerate(lines[:-1]):
        headers = line.split()
        if not headers or headers[0] != "Tcp:":
            continue
        values = lines[index + 1].split()
        if len(headers) != len(values) or values[0] != "Tcp:":
            return None
        fields = dict(zip(headers[1:], values[1:]))
        output, retrans = metric_uint(fields.get("OutSegs")), metric_uint(fields.get("RetransSegs"))
        if output is not None and retrans is not None:
            return {"outSegments": output, "retransmittedSegments": retrans}
        return None
    return None


def collect_metrics(active, proc=pathlib.Path("/proc"), cgroup=pathlib.Path("/sys/fs/cgroup")):
    return {
        "version": 1,
        "sampledAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "running": bool(active),
        "cgroup": cgroup_metrics(proc, cgroup),
        # Это счётчики контейнера Xray, не контейнера API и не физического хоста.
        "network": network_metrics(proc),
        "tcp": tcp_metrics(proc),
    }


def stop_child():
    global process
    if process is not None and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    process = None


def start_child(state):
    global process
    # В XHTTP-режиме не отдаём сырой вывод дочернего процесса в журнал контейнера:
    # даже ошибка старта может включать адрес, путь или неизвестное нам поле секрета.
    # Неразбираемый конфиг также не является основанием раскрывать его содержимое.
    suppress_output = True
    try:
        configuration = json.loads(CONFIG.read_text(encoding="utf-8"))
        suppress_output = any(inbound.get("streamSettings", {}).get("network") == "xhttp"
                              for inbound in configuration.get("inbounds", []))
    except (OSError, ValueError, TypeError, AttributeError):
        pass
    output = {"stdout": subprocess.DEVNULL, "stderr": subprocess.DEVNULL} if suppress_output else {}
    process = subprocess.Popen(["/usr/local/bin/xray", "run", "-config", str(CONFIG)], **output)
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if process.poll() is not None:
            return False
        try:
            with socket.create_connection(("127.0.0.1", 443), timeout=0.25):
                write_status(state, True)
                return True
        except OSError:
            time.sleep(0.1)
    return False


def terminate(_signum, _frame):
    global running
    running = False


def main():
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    last_state = None
    next_metrics = 0
    write_status(None, False)
    while running:
        try:
            state = snapshot()
            if state != last_state or (process is not None and process.poll() is not None):
                stop_child()
                last_state = state
                if state is None:
                    write_status(None, False)
                elif not start_child(state):
                    write_status(state, False, "Xray failed to listen on TCP 443")
                    stop_child()
                    # Неверная конфигурация не должна вызывать частый цикл перезапусков.
                    time.sleep(2)
            if time.monotonic() >= next_metrics:
                next_metrics = time.monotonic() + 2
                try:
                    write_document(METRICS, collect_metrics(process is not None and process.poll() is None))
                except (OSError, ValueError) as error:
                    # Сбой необязательной телеметрии не перезапускает рабочий Xray.
                    print(f"Xray telemetry: {type(error).__name__}", flush=True)
            time.sleep(0.25)
        except (OSError, ValueError) as error:
            print(f"Xray worker: {type(error).__name__}", flush=True)
            time.sleep(2)
    stop_child()
    write_status(last_state, False)
    try:
        write_document(METRICS, collect_metrics(False))
    except (OSError, ValueError):
        pass


if __name__ == "__main__":
    main()
