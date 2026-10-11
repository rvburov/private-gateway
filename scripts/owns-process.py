#!/usr/bin/env python3
"""Identify a recorded local process without /proc/PID/exe access."""
import os
import pathlib
import sys

try:
    pid = int(sys.argv[1])
    if pid <= 1:
        raise ValueError("invalid PID")
    proc = pathlib.Path("/proc") / str(pid)
    assert proc.stat().st_uid == os.getuid()
    argv = (proc / "cmdline").read_bytes().split(b"\0")
    assert argv[0]
    assert os.path.realpath(os.fsdecode(argv[0])) == os.path.realpath(sys.argv[2])
except (OSError, ValueError, AssertionError, IndexError):
    raise SystemExit(1)
