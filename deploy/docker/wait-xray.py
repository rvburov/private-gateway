#!/usr/bin/env python3
"""Wait until the separate Xray container has loaded the exact applied config."""

import hashlib
import json
import os
import pathlib
import sys
import time


config = pathlib.Path("/state/xray.json")
status = pathlib.Path("/state/xray.status.json")
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
