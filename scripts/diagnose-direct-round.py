#!/usr/bin/env python3
"""Запустить захват VPS и одну синхронную внешнюю контрольную TCP-попытку."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import sys
import threading
import time


TARGET = "162.35.172.6"
TARGET_PORT = 8444
SETTINGS = Path("/var/lib/private-gateway-docker/xray/xray.json")
CONTROLLER = Path(__file__).with_name("diagnose-direct-server.py")
CONTROL_DELAY = 15


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds")


def read_exact(connection: socket.socket, count: int, deadline: float) -> bytes:
    data = bytearray()
    while len(data) < count:
        connection.settimeout(max(0.01, deadline - time.monotonic()))
        chunk = connection.recv(count - len(data))
        if not chunk:
            raise ConnectionError("closed")
        data.extend(chunk)
    return bytes(data)


def send(connection: socket.socket, data: bytes, deadline: float) -> None:
    connection.settimeout(max(0.01, deadline - time.monotonic()))
    connection.sendall(data)


def connect_once() -> str:
    # Секреты остаются в памяти и не передаются через argv, файлы или вывод.
    config = json.loads(SETTINGS.read_text())
    settings = next(item["settings"] for item in config["outbounds"] if item.get("protocol") == "socks")
    user = settings["user"].encode()
    password = settings["pass"].encode()
    if not 1 <= len(user) <= 255 or not 1 <= len(password) <= 255:
        return "PROXY_CREDENTIAL_LENGTH_ERROR"
    deadline = time.monotonic() + 8
    with socket.create_connection(
        (settings["address"], int(settings["port"])), timeout=5
    ) as connection:
        send(connection, b"\x05\x01\x02", deadline)
        if read_exact(connection, 2, deadline) != b"\x05\x02":
            return "PROXY_NEGOTIATION_FAILED"
        send(connection, bytes((1, len(user))) + user + bytes((len(password),)) + password, deadline)
        if read_exact(connection, 2, deadline) != b"\x01\x00":
            return "PROXY_AUTH_FAILED"
        request = b"\x05\x01\x00\x01" + socket.inet_aton(TARGET) + struct.pack("!H", TARGET_PORT)
        send(connection, request, deadline)
        version, reply, _reserved, address_type = read_exact(connection, 4, deadline)
        if version != 5:
            return "PROXY_REPLY_INVALID"
        if address_type == 1:
            read_exact(connection, 4, deadline)
        elif address_type == 4:
            read_exact(connection, 16, deadline)
        elif address_type == 3:
            read_exact(connection, read_exact(connection, 1, deadline)[0], deadline)
        else:
            return "PROXY_REPLY_INVALID"
        read_exact(connection, 2, deadline)
        return "TCP_CONNECT_OK" if reply == 0 else f"TCP_CONNECT_FAILED_{reply}"


def control(output_dir: Path | None) -> str:
    started = utc_now()
    began = time.monotonic()
    try:
        result = connect_once()
    except TimeoutError:
        result = "TIMEOUT"
    except Exception as error:
        # Не печатать исключение: оно может включать адрес прокси или секреты.
        result = "ERROR_" + type(error).__name__
    record = {
        "endpoint": f"{TARGET}:{TARGET_PORT}",
        "start_utc": started,
        "end_utc": utc_now(),
        "elapsed_ms": round((time.monotonic() - began) * 1000),
        "result": result,
    }
    print("CONTROL " + json.dumps(record, ensure_ascii=False), flush=True)
    if output_dir is not None:
        try:
            (output_dir / "control.json").write_text(
                json.dumps(record, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
            )
        except OSError:
            print("CONTROL_RECORD_NOT_SAVED", flush=True)
    return result


def run_round(
    start: int,
    duration: int,
    output: Path | None = None,
    *,
    controller: Path = CONTROLLER,
    delay: float = CONTROL_DELAY,
) -> int:
    command = [sys.executable, str(controller), "--start", str(start), "--duration", str(duration)]
    if output is not None:
        command += ["--output", str(output)]
    process = subprocess.Popen(
        command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, text=True, bufsize=1,
    )
    ready = threading.Event()
    finished = threading.Event()
    state: dict[str, str | Path] = {}

    def relay() -> None:
        assert process.stdout is not None
        for line in process.stdout:
            print(line, end="", flush=True)
            if line.startswith("OUTPUT_DIR="):
                state["output_dir"] = Path(line.removeprefix("OUTPUT_DIR=").strip())
            elif line.startswith("READY_UTC="):
                state["ready_utc"] = line.removeprefix("READY_UTC=").strip()
                ready.set()
        finished.set()

    reader = threading.Thread(target=relay, daemon=True)
    reader.start()
    try:
        while not ready.is_set() and not finished.is_set():
            ready.wait(0.1)
        if not ready.is_set():
            print("CONTROL_SKIPPED=NO_CAPTURE_READY", flush=True)
            process.wait()
            reader.join()
            return process.returncode or 1

        try:
            ready_epoch = dt.datetime.fromisoformat(str(state["ready_utc"])).timestamp()
        except (KeyError, ValueError):
            print("CONTROL_SKIPPED=INVALID_READY_TIME", flush=True)
            process.wait()
            reader.join()
            return process.returncode or 1
        control_at = ready_epoch + delay
        latest_start = start + duration - 15
        if control_at > latest_start:
            print("CONTROL_SKIPPED=INSUFFICIENT_CAPTURE_TIME", flush=True)
            result = "SKIPPED"
        else:
            while time.time() < control_at and process.poll() is None:
                time.sleep(min(0.1, max(0.0, control_at - time.time())))
            if process.poll() is not None or time.time() > control_at + 3:
                print("CONTROL_SKIPPED=CAPTURE_ENDED_OR_LATE", flush=True)
                result = "SKIPPED"
            else:
                os.umask(0o077)
                path = state.get("output_dir")
                result = control(path if isinstance(path, Path) else None)
        process.wait()
        reader.join()
        return 0 if process.returncode == 0 and result == "TCP_CONNECT_OK" else 1
    except KeyboardInterrupt:
        print("CONTROL_SKIPPED=INTERRUPTED", flush=True)
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
        process.wait()
        reader.join()
        return 130


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--start", type=int, required=True, help="Unix epoch времени запуска, UTC")
    parser.add_argument("--duration", type=int, default=90, help="Общий лимит захвата, 1–90 с")
    parser.add_argument("--output", type=Path, help="Новый закрытый каталог отчёта")
    args = parser.parse_args()
    if not 1 <= args.duration <= 90:
        parser.error("--duration должен быть от 1 до 90 секунд")
    if os.geteuid() != 0:
        print("Ошибка: запустите через sudo.", file=sys.stderr)
        return 2
    return run_round(args.start, args.duration, args.output)


if __name__ == "__main__":
    raise SystemExit(main())
