#!/usr/bin/env bash
# Проверка локальных ресурсов только на чтение. Окружение и секреты никогда не печатает.
set -euo pipefail
uname -srm
getconf _NPROCESSORS_ONLN
free -h
df -h /
if command -v ss >/dev/null; then ss -lnt; fi
if command -v docker >/dev/null; then docker compose version; fi
