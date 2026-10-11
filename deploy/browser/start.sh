#!/bin/sh
set -eu

# All state lives in explicit tmpfs mounts and disappears at stop.
export HOME=/home/browser DISPLAY=:99 XDG_RUNTIME_DIR=/tmp/browser-runtime
umask 077
mkdir -p "$XDG_RUNTIME_DIR" /tmp/chromium
pids=""
cleanup() {
    trap - EXIT INT TERM
    [ -z "$pids" ] || kill $pids 2>/dev/null || true
    wait 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 0' INT TERM

Xvfb "$DISPLAY" -screen 0 1440x900x24 -nolisten tcp -ac &
xvfb_pid=$!
pids="$xvfb_pid"
attempt=0
until xdpyinfo -display "$DISPLAY" >/dev/null 2>&1; do
    kill -0 "$xvfb_pid"
    attempt=$((attempt + 1))
    [ "$attempt" -lt 100 ] || { echo "Xvfb did not become ready" >&2; exit 1; }
    sleep 0.1
done

openbox --sm-disable &
openbox_pid=$!
pids="$pids $openbox_pid"
x0vncserver -fg -display "$DISPLAY" -rfbport 5900 -localhost -SecurityTypes None -AlwaysShared &
vnc_pid=$!
pids="$pids $vnc_pid"
websockify --web=/usr/share/novnc 6080 127.0.0.1:5900 &
novnc_pid=$!
pids="$pids $novnc_pid"

# Rootless OCI is the isolation boundary. Chrome's nested namespace/setuid
# sandbox requires privileges denied by this deployment.
chromium --no-sandbox --disable-dev-shm-usage --no-first-run \
    --no-default-browser-check --user-data-dir=/tmp/chromium \
    --window-size=1440,900 --start-maximized about:blank &
chromium_pid=$!
pids="$pids $chromium_pid"
printf '%s\n' "$pids" > /tmp/browser.pids

# Losing a required process ends the container rather than leaving a reachable
# noVNC page after Chromium or its display has died.
while :; do
    for pid in $pids; do
        kill -0 "$pid" || { echo "Required browser process $pid exited" >&2; exit 1; }
    done
    sleep 1
done
