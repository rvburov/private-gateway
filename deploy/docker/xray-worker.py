#!/usr/bin/env python3
"""Restart Xray when the API atomically replaces its shared config file."""

import hashlib
import json
import os
import pathlib
import signal
import socket
import subprocess
import tempfile
import time


CONFIG = pathlib.Path("/state/xray.json")
STATUS = pathlib.Path("/state/xray.status.json")
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


def write_status(state, active, error=""):
    document = dict(state or {"mtime_ns": 0, "sha256": ""})
    document.update(running=active, error=error)
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=STATUS.parent, prefix=".xray-status-", delete=False) as output:
        json.dump(document, output, separators=(",", ":"))
        output.write("\n")
        temporary = pathlib.Path(output.name)
    os.chmod(temporary, 0o600)
    os.replace(temporary, STATUS)


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
    process = subprocess.Popen(["/usr/local/bin/xray", "run", "-config", str(CONFIG)])
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


signal.signal(signal.SIGTERM, terminate)
signal.signal(signal.SIGINT, terminate)
last_state = None
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
                # An invalid config must not cause a tight restart loop.
                time.sleep(2)
        time.sleep(0.25)
    except (OSError, ValueError) as error:
        print(f"Xray worker: {type(error).__name__}", flush=True)
        time.sleep(2)
stop_child()
write_status(last_state, False)
