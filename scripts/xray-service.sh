#!/usr/bin/env bash
# Local development lifecycle only. Production uses a narrowly scoped service.
set -euo pipefail
cd "$(dirname "$0")/.."
set -a
source .env
set +a
umask 077
mkdir -p .local
stop_xray() {
  if test -f .local/xray.pid; then
    pid=$(cat .local/xray.pid)
    binary_path=$(command -v "$GATEWAY_XRAY_BINARY")
    expected=$(readlink -f "$binary_path")
    if python3 scripts/owns-process.py "$pid" "$expected"; then
      kill "$pid"
      for attempt in $(seq 1 100); do
        if ! kill -0 "$pid" 2>/dev/null || [[ "$(ps -o stat= -p "$pid" 2>/dev/null || true)" == Z* ]]; then break; fi
        sleep 0.1
      done
      if kill -0 "$pid" 2>/dev/null && [[ "$(ps -o stat= -p "$pid" 2>/dev/null || true)" != Z* ]]; then echo 'Xray did not stop' >&2; exit 1; fi
    fi
    rm -f .local/xray.pid
  fi
}
case "${1:-}" in
  restart)
    stop_xray
    nohup "$GATEWAY_XRAY_BINARY" run -config "$GATEWAY_XRAY_CONFIG" >.local/xray.log 2>&1 &
    pid=$!
    printf '%s\n' "$pid" >.local/xray.pid
    for attempt in $(seq 1 50); do
      if ! kill -0 "$pid" 2>/dev/null || [[ "$(ps -o stat= -p "$pid" 2>/dev/null || true)" == Z* ]]; then echo 'Xray startup failed; inspect .local/xray.log' >&2; exit 1; fi
      if python3 - <<'PY'
import os,socket
try:
 s=socket.create_connection((os.environ.get('GATEWAY_XRAY_LISTEN','127.0.0.1'),int(os.environ['GATEWAY_PORT'])),timeout=.2)
 s.close()
except OSError: raise SystemExit(1)
PY
      then exit 0; fi
      sleep 0.1
    done
    echo 'Xray startup timed out' >&2
    exit 1
    ;;
  stop) stop_xray ;;
  *) echo 'usage: scripts/xray-service.sh restart|stop' >&2; exit 1 ;;
esac
