#!/usr/bin/env python3
"""Дождаться, пока отдельный контейнер Xray загрузит в точности применённую конфигурацию."""

import hashlib
import json
import os
import pathlib
import sys
import time


transport = sys.argv[1] if len(sys.argv) == 2 else "tcp"
if transport not in ("tcp", "xhttp"):
    raise SystemExit("unknown Xray transport")
stem = "xray" if transport == "tcp" else "xhttp"
config = pathlib.Path("/state") / (stem + ".json")
status = pathlib.Path("/state") / (stem + ".status.json")
with config.open("rb") as source:
    content = source.read()
    modified = os.fstat(source.fileno()).st_mtime_ns
expected = hashlib.sha256(content).hexdigest()
deadline = time.monotonic() + 25
while time.monotonic() < deadline:
    try:
        result = json.loads(status.read_text())
    except (FileNotFoundError, ValueError):
        time.sleep(0.2)
        continue
    if result.get("sha256") == expected and result.get("mtime_ns") == modified:
        if result.get("running") is True:
            sys.exit(0)
        print("Xray worker could not start the applied configuration", file=sys.stderr)
        sys.exit(1)
    time.sleep(0.2)
print("Timed out waiting for Xray to load the applied configuration", file=sys.stderr)
sys.exit(1)
