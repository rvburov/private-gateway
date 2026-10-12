#!/usr/bin/env bash
# Read-only local resource audit. Never prints environment or credentials.
set -euo pipefail
uname -srm
getconf _NPROCESSORS_ONLN
free -h
df -h /
if command -v ss >/dev/null; then ss -lnt; fi
if command -v docker >/dev/null; then docker compose version; fi
