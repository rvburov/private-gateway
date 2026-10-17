#!/bin/sh
set -eu
test -s /tmp/browser.pids
for pid in $(cat /tmp/browser.pids); do
    kill -0 "$pid"
    # kill -0 срабатывает и для зомби-процессов; их здоровыми не считать.
    test "$(ps -o stat= -p "$pid" | cut -c1)" != Z
done
xdpyinfo -display :99 >/dev/null 2>&1
curl --fail --silent --max-time 2 http://127.0.0.1:6080/vnc.html | head -c 524288 | grep -q noVNC
