#!/usr/bin/env bash
# Прямые сетевые пробы Windows через Git Bash без изменения настроек.

set -u

server_ip=162.35.172.6
panel_host=rb-gateway-3708079.duckdns.org
control_ip=1.1.1.1

usage() {
  cat <<'EOF'
Запуск в Git Bash на Windows: bash diagnose-direct-windows.sh [--start UNIX_UTC] [--output НОВАЯ_АБСОЛЮТНАЯ_ПАПКА]

По умолчанию проверка начинается через 180 секунд. Передайте START_EPOCH
агенту, затем отключите сторонний VPN до START_UTC. Папка результатов
создаётся в домашней папке Windows, если --output не указан.
Скрипт не требует прав администратора и не меняет сетевые настройки.
EOF
}

start_epoch=
output_dir=
while [[ $# -gt 0 ]]; do
  case $1 in
    --start)
      [[ $# -ge 2 && $2 =~ ^[0-9]{10}$ ]] || { usage >&2; exit 2; }
      start_epoch=$((10#$2))
      shift 2
      ;;
    --output)
      [[ $# -ge 2 && $2 == /* ]] || { usage >&2; exit 2; }
      output_dir=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

case $(uname -s) in
  MINGW*|MSYS*) ;;
  *) echo 'Этот скрипт запускается только в Git Bash на Windows.' >&2; exit 2 ;;
esac

for program in curl.exe route.exe ipconfig.exe cygpath; do
  command -v "$program" >/dev/null 2>&1 || {
    printf 'Не найдена программа: %s\n' "$program" >&2
    exit 2
  }
done

curl_version=$(curl.exe -q -V 2>&1) || {
  printf 'Не удалось проверить curl.exe: %s\n' "$curl_version" >&2
  exit 2
}
protocols_line=$(printf '%s\n' "$curl_version" | sed -n '/^Protocols:/p')
if ! printf '%s\n' "$protocols_line" | grep -Eiq '(^|[[:space:]])telnet([[:space:]]|$)'; then
  echo 'Эта сборка curl.exe не поддерживает TELNET; пробы не запущены.' >&2
  exit 2
fi

now=$(date -u +%s) || exit 2
[[ -n $start_epoch ]] || start_epoch=$((now + 180))
if (( start_epoch < now - 2 )); then
  echo 'START_EPOCH уже прошёл; запустите проверку с новым временем.' >&2
  exit 2
fi
if (( start_epoch > now + 600 )); then
  echo 'START_EPOCH должен быть в ближайшие 10 минут для синхронизации с VPS.' >&2
  exit 2
fi

umask 077
if [[ -n $output_dir ]]; then
  [[ ! -e $output_dir && ! -L $output_dir ]] || {
    echo 'Папка результатов уже существует.' >&2
    exit 2
  }
  mkdir -m 700 -- "$output_dir" || exit 1
else
  output_dir=$(mktemp -d "${HOME%/}/private-gateway-direct.XXXXXX") || exit 1
fi
windows_output_dir=$(cygpath -w "$output_dir") || exit 1
printf '%s\n' "$curl_version" > "$output_dir/curl-version.txt"

printf 'START_EPOCH=%s\n' "$start_epoch"
printf 'START_UTC=%s\n' "$(date -u -d "@$start_epoch" '+%Y-%m-%dT%H:%M:%SZ')"
printf 'OUTPUT_DIR=%s\n' "$output_dir"
printf 'OUTPUT_DIR_WINDOWS=%s\n' "$windows_output_dir"
echo 'Передайте START_EPOCH агенту, затем отключите VPN до START_UTC.'
echo 'После отсчёта проверки начнутся автоматически.'

while (( $(date -u +%s) < start_epoch )); do sleep 1; done
now=$(date -u +%s)
if (( now > start_epoch + 2 )); then
  echo 'Назначенное начало пропущено более чем на две секунды; пробы не запущены.' >&2
  exit 2
fi

printf 'start_utc\tend_utc\tsnapshot\texit\n' > "$output_dir/snapshots.tsv"
snapshot() {
  local label=$1 started ended result
  shift
  started=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  "$@" > "$output_dir/$label.txt" 2>&1
  result=$?
  ended=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  printf '%s\t%s\t%s\t%s\n' "$started" "$ended" "$label" "$result" >> "$output_dir/snapshots.tsv"
}

snapshot route-before route.exe print -4
snapshot ipconfig-before ipconfig.exe

# Серверный захват начинается в START_EPOCH; оставляем время на его готовность.
probe_epoch=$((start_epoch + 10))
while (( $(date -u +%s) < probe_epoch )); do sleep 1; done
now=$(date -u +%s)
if (( now > start_epoch + 12 )); then
  echo 'Снимки сети задержали запуск проб; окно захвата VPS могло закончиться. Повторите проверку с новым START_EPOCH.' >&2
  exit 2
fi
printf 'PROBES_BEGIN_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

printf 'start_utc\tend_utc\tprobe\texit\n' > "$output_dir/probes.tsv"
: > "$output_dir/probe-detail.txt"
run_probe() {
  local label=$1 started ended result
  shift
  started=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  printf '\n%s %s\n' "$started" "$label" >> "$output_dir/probe-detail.txt"
  "$@" >> "$output_dir/probe-detail.txt" 2>&1
  result=$?
  ended=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  printf '%s\t%s\t%s\t%s\n' "$started" "$ended" "$label" "$result" >> "$output_dir/probes.tsv"
}

tcp_metrics='remote=%{remote_ip} tcp_connect=%{time_connect}\n'
for pass in 1 2; do
  for port in 22 443 8443 8444; do
    run_probe "pass${pass}_tcp${port}" curl.exe -q -4 --proxy '' --noproxy '*' \
      --connect-timeout 5 --max-time 5 -sS -v -o NUL -w "$tcp_metrics" \
      "telnet://$server_ip:$port" < /dev/null
  done
done
run_probe control_tcp443 curl.exe -q -4 --proxy '' --noproxy '*' \
  --connect-timeout 5 --max-time 5 -sS -v -o NUL -w "$tcp_metrics" \
  "telnet://$control_ip:443" < /dev/null
run_probe panel_https curl.exe -q -4 --proxy '' --noproxy '*' \
  --resolve "$panel_host:8443:$server_ip" --connect-timeout 5 --max-time 10 \
  -sS -o NUL -w 'http=%{http_code} remote=%{remote_ip} tcp_connect=%{time_connect} tls=%{time_appconnect}\n' \
  "https://$panel_host:8443/readyz"
run_probe source_ipv4 curl.exe -q -4 --proxy '' --noproxy '*' \
  --connect-timeout 5 --max-time 10 -fsS -w '\nremote=%{remote_ip} tcp_connect=%{time_connect}\n' \
  https://api.ipify.org/

snapshot route-after route.exe print -4
snapshot ipconfig-after ipconfig.exe
printf 'DONE_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
printf 'OUTPUT_DIR=%s\n' "$output_dir"
printf 'OUTPUT_DIR_WINDOWS=%s\n' "$windows_output_dir"
echo 'Код 28 для TELNET означает общий таймаут; TCP-подключение оценивайте по tcp_connect и сообщению об установленном соединении в подробном журнале.'
