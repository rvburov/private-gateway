#!/bin/sh
set -eu

# Всё состояние лежит в явно подключённых tmpfs и исчезает при остановке.
export HOME=/home/browser/private DISPLAY=:99 XDG_RUNTIME_DIR=/tmp/browser-runtime
umask 077
mkdir -p "$HOME" "$XDG_RUNTIME_DIR" /tmp/chromium
chmod 0700 "$HOME"
pids=""
cleanup() {
    trap - EXIT INT TERM
    [ -z "$pids" ] || kill $pids 2>/dev/null || true
    wait 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 0' INT TERM

Xvfb "$DISPLAY" -screen 0 1280x720x24 -nolisten tcp -ac &
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

# Граница изоляции — ограниченный Docker-контейнер. Вложенной песочнице Chrome на namespace и
# setuid нужны права, которых эта установка не даёт.
chromium --no-sandbox --disable-dev-shm-usage --no-first-run \
    --no-default-browser-check --user-data-dir=/tmp/chromium \
    --window-size=1280,720 --start-maximized about:blank &
chromium_pid=$!
pids="$pids $chromium_pid"
printf '%s\n' "$pids" > /tmp/browser.pids

# Потеря нужного процесса завершает контейнер, чтобы не оставлять доступную страницу
# noVNC после того, как Chromium или его дисплей упали.
while :; do
    for pid in $pids; do
        kill -0 "$pid" || { echo "Required browser process $pid exited" >&2; exit 1; }
    done
    sleep 1
done
