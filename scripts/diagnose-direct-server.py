#!/usr/bin/env python3
"""Короткая синхронная проверка входящих TCP-пакетов на VPS."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path


TARGET_IP = "162.35.172.6"
PORTS = (22, 443, 8443, 8444)
CAPTURE_FILTER = (
    f"tcp and ((dst host {TARGET_IP} and "
    "(dst port 22 or dst port 443 or dst port 8443 or dst port 8444)) or "
    f"(src host {TARGET_IP} and "
    "(src port 22 or src port 443 or src port 8443 or src port 8444))) and "
    "(tcp[13] & 0x16 != 0)"
)
STAT_PATTERN = re.compile(
    r"^(\d+) packets? (captured|received by filter|dropped by kernel|dropped by interface)$"
)


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--start", type=int, required=True, help="Время запуска: Unix epoch, UTC")
    parser.add_argument("--duration", type=int, default=90, help="Длительность захвата: 1–90 с")
    parser.add_argument("--interface", default="eth0", help="Сетевой интерфейс VPS")
    parser.add_argument("--output", type=Path, help="Новый закрытый каталог для отчёта")
    args = parser.parse_args()
    if not 1 <= args.duration <= 90:
        parser.error("--duration должен быть от 1 до 90 секунд")
    if not args.interface or args.interface.startswith("-"):
        parser.error("--interface должен быть именем сетевого интерфейса")
    return args


def make_output_dir(requested: Path | None) -> Path:
    if requested is None:
        return Path(tempfile.mkdtemp(prefix="pg-direct-", dir="/tmp"))
    path = requested.expanduser().absolute()
    path.mkdir(mode=0o700)
    return path


def stop_child(child: subprocess.Popen[str], hard_deadline: float) -> str:
    if child.poll() is not None:
        return "already_exited"

    # Резерв на завершение входит в общий лимит жизни tcpdump.
    grace = min(1.0, max(0.0, hard_deadline - time.monotonic()) / 3)
    child.send_signal(signal.SIGINT)
    try:
        child.wait(timeout=grace)
        return "sigint"
    except subprocess.TimeoutExpired:
        pass
    child.terminate()
    try:
        child.wait(timeout=min(grace, max(0.0, hard_deadline - time.monotonic())))
        return "sigterm"
    except subprocess.TimeoutExpired:
        pass
    child.kill()
    try:
        child.wait(timeout=max(0.0, hard_deadline - time.monotonic()))
        return "sigkill"
    except subprocess.TimeoutExpired:
        return "sigkill_pending"


def main() -> int:
    args = parse_args()
    if os.geteuid() != 0:
        print("Ошибка: скрипт нужно запустить от root (sudo).", file=sys.stderr)
        return 2
    current_time = time.time()
    if args.start < current_time - 2:
        print("Ошибка: заданное время запуска уже прошло.", file=sys.stderr)
        return 2
    if args.start > current_time + 600:
        print("Ошибка: начало должно быть в ближайшие 10 минут; проверьте часы и START_EPOCH.", file=sys.stderr)
        return 2

    os.umask(0o077)
    try:
        output_dir = make_output_dir(args.output)
    except OSError as exc:
        print(f"Ошибка создания каталога отчёта: {exc}", file=sys.stderr)
        return 2

    command = [
        "tcpdump", "-i", args.interface, "-nn", "-tttt", "-l", "-s", "74",
        "-Q", "inout", CAPTURE_FILTER,
    ]
    report: dict[str, object] = {
        "scheduled_start_epoch": args.start,
        "scheduled_start_utc": dt.datetime.fromtimestamp(
            args.start, dt.timezone.utc
        ).isoformat(),
        "duration_limit_seconds": args.duration,
        "target_ip": TARGET_IP,
        "ports": PORTS,
        "command": command,
        "output_dir": str(output_dir),
        "status": "scheduled",
        "statistics": {},
    }
    stop_event = threading.Event()

    def request_stop(signum: int, _frame: object) -> None:
        report["interrupted_by_signal"] = signum
        stop_event.set()

    old_sigint = signal.signal(signal.SIGINT, request_stop)
    old_sigterm = signal.signal(signal.SIGTERM, request_stop)
    report_path = output_dir / "report.json"
    log_path = output_dir / "capture.log"
    print(f"SCHEDULED_UTC={report['scheduled_start_utc']}", flush=True)
    print(f"OUTPUT_DIR={output_dir}", flush=True)
    child: subprocess.Popen[str] | None = None
    hard_deadline: float | None = None
    try:
        while not stop_event.is_set() and time.time() < args.start:
            stop_event.wait(min(0.2, max(0.0, args.start - time.time())))
        if stop_event.is_set():
            report["status"] = "interrupted_before_start"
            return 130
        if time.time() > args.start + 2:
            report["status"] = "missed_start"
            print("MISSED_START: время запуска прошло более двух секунд назад.", flush=True)
            return 2

        ready_event = threading.Event()
        log_lock = threading.Lock()
        stats: dict[str, int] = {}
        stderr_tail: list[str] = []
        with log_path.open("x", encoding="utf-8", buffering=1) as capture_log:
            environment = os.environ.copy()
            environment["TZ"] = "UTC"
            try:
                spawn_time = utc_now()
                spawn_monotonic = time.monotonic()
                hard_deadline = spawn_monotonic + args.duration
                child = subprocess.Popen(
                    command,
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    bufsize=1,
                    env=environment,
                )
            except OSError as exc:
                report["status"] = "start_failed"
                report["error"] = str(exc)
                print(f"START_FAILED={exc}", file=sys.stderr, flush=True)
                return 2
            report["spawn_utc"] = spawn_time
            report["pid"] = child.pid
            report["status"] = "starting"

            def read_stream(stream: object, label: str) -> None:
                assert stream is not None
                for line in stream:
                    with log_lock:
                        capture_log.write(f"[{label}] {line}")
                        if label == "stderr":
                            stderr_tail.append(line.rstrip())
                            del stderr_tail[:-8]
                            match = STAT_PATTERN.match(line.strip())
                            if match:
                                stats[match.group(2)] = int(match.group(1))
                    if label == "stderr" and "listening on " in line:
                        ready_event.set()
                stream.close()

            assert child.stdout is not None and child.stderr is not None
            readers = [
                threading.Thread(target=read_stream, args=(child.stdout, "stdout")),
                threading.Thread(target=read_stream, args=(child.stderr, "stderr")),
            ]
            for reader in readers:
                reader.start()

            shutdown_reserve = min(3.0, args.duration / 2)
            capture_deadline = hard_deadline - shutdown_reserve
            report["shutdown_reserve_seconds"] = shutdown_reserve
            readiness_deadline = min(capture_deadline, spawn_monotonic + 10)
            while not ready_event.is_set() and not stop_event.is_set():
                if child.poll() is not None or time.monotonic() >= readiness_deadline:
                    break
                ready_event.wait(0.05)

            if (
                ready_event.is_set()
                and child.poll() is None
                and time.monotonic() < capture_deadline
            ):
                report["ready_utc"] = utc_now()
                report["status"] = "capturing"
                print(f"READY_UTC={report['ready_utc']}", flush=True)
                while (
                    child.poll() is None
                    and not stop_event.is_set()
                    and time.monotonic() < capture_deadline
                ):
                    stop_event.wait(
                        min(0.1, max(0.0, capture_deadline - time.monotonic()))
                    )
                if stop_event.is_set():
                    report["status"] = "interrupted_during_capture"
                elif child.poll() is not None:
                    report["status"] = "ended_early"
                else:
                    report["status"] = "complete"
            else:
                report["status"] = "not_ready"
                report["error"] = "tcpdump завершился или не сообщил о готовности"

            report["termination"] = stop_child(child, hard_deadline)
            if report["status"] == "complete" and report["termination"] not in (
                "sigint", "already_exited"
            ):
                report["status"] = "forced_stop"
            report["end_utc"] = utc_now()
            report["exit_code"] = child.returncode
            for reader in readers:
                reader.join()
            report["statistics"] = stats
            if report["status"] == "not_ready":
                report["stderr_tail"] = stderr_tail
            print(f"END_UTC={report['end_utc']}", flush=True)
            print(f"STATUS={report['status']} EXIT_CODE={child.returncode}", flush=True)
            print(f"STATISTICS={json.dumps(stats, ensure_ascii=False)}", flush=True)
            return 0 if report["status"] == "complete" and child.returncode == 0 else 1
    finally:
        if child is not None and child.poll() is None:
            stop_child(child, hard_deadline or time.monotonic())
        report["finished_utc"] = utc_now()
        report_path.write_text(
            json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
        )
        signal.signal(signal.SIGINT, old_sigint)
        signal.signal(signal.SIGTERM, old_sigterm)


if __name__ == "__main__":
    raise SystemExit(main())
