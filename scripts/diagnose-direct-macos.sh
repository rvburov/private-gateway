#!/bin/bash
# Проверка прямого пути с macOS к VPS без изменения сетевых настроек.

set -u

server_ip=162.35.172.6
panel_host=rb-gateway-3708079.duckdns.org
control_ip=1.1.1.1

usage() {
  cat <<'EOF'
Запуск на macOS: bash diagnose-direct-macos.sh [--start UNIX_UTC] [--output НОВАЯ_ПАПКА]

По умолчанию проверка начинается через 180 секунд. Сразу перед отсчётом
запрашивается sudo; после строки START_EPOCH можно отключить сторонний VPN.
К моменту START_EPOCH маршрут к VPS должен идти через физический интерфейс en*.
Результаты сохраняются в новой закрытой папке на Рабочем столе.
Скрипт не меняет VPN, маршруты, DNS и правила сетевого экрана.
EOF
}

capture_running() {
  # PID получен только от прямого дочернего процесса текущего bash.
  jobs -pr | grep -Fxq "$tcpdump_pid"
}

stop_capture() {
  local signal=${1:-TERM}
  if [[ -n "${tcpdump_pid:-}" ]]; then
    if capture_running; then
      kill -"$signal" "$tcpdump_pid" 2>/dev/null || :
    fi
    # Даём tcpdump вывести итоговые счётчики; затем ограничиваем ожидание.
    local attempt
    for attempt in 1 2; do
      capture_running || break
      sleep 1
    done
    if capture_running; then
      kill -KILL "$tcpdump_pid" 2>/dev/null || :
    fi
    wait "$tcpdump_pid" 2>/dev/null || :
    tcpdump_pid=
  fi
}

capture_main() {
  [[ $# -eq 2 && $1 == en[0-9]* && $2 == "$server_ip" ]] || exit 2
  [[ $(uname -s) == Darwin ]] || exit 2
  [[ -x /usr/sbin/tcpdump ]] || exit 2
  local iface=$1 deadline
  export TZ=UTC
  /usr/sbin/tcpdump -i "$iface" -nn -p -tttt -l -s 74 \
    "tcp and (((host $server_ip) and (port 22 or port 443 or port 8443 or port 8444)) or (host $control_ip and port 443)) and (tcp[13] & 0x16 != 0)" &
  tcpdump_pid=$!
  # До 87 секунд работы и до трёх секунд на остановку: предел 90 секунд.
  deadline=$(( $(date -u +%s) + 86 ))
  trap 'stop_capture INT; exit 130' INT
  trap 'stop_capture TERM; exit 143' TERM
  trap 'stop_capture TERM' EXIT
  while (( $(date -u +%s) < deadline )); do
    capture_running || break
    sleep 1
  done
  stop_capture TERM
  trap - EXIT INT TERM
}

if [[ ${1:-} == --capture ]]; then
  shift
  capture_main "$@"
  exit $?
fi

start_epoch=
output_dir=
while [[ $# -gt 0 ]]; do
  case $1 in
    --start)
      [[ $# -ge 2 && $2 =~ ^[0-9]{10}$ ]] || { usage >&2; exit 2; }
      start_epoch=$2
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

[[ $(uname -s) == Darwin ]] || { echo 'Этот скрипт запускается только на macOS.' >&2; exit 2; }

# Разрешение sudo получаем до отсчёта, чтобы пароль не задержал начало проверки.
sudo -v || exit 1
now=$(date -u +%s)
[[ -n $start_epoch ]] || start_epoch=$((now + 180))
(( start_epoch > now )) || { echo 'START_EPOCH уже прошёл после ввода sudo; задайте новое время.' >&2; exit 2; }
script_path=$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")
[[ -f $script_path ]] || { echo 'Не найден файл запущенного скрипта.' >&2; exit 1; }
umask 077
if [[ -n $output_dir ]]; then
  [[ ! -e $output_dir && ! -L $output_dir ]] || { echo 'Папка результатов уже существует.' >&2; exit 2; }
  mkdir -m 700 "$output_dir" || exit 1
else
  output_dir=$(mktemp -d "$HOME/Desktop/private-gateway-direct.XXXXXX") || exit 1
fi

printf 'START_EPOCH=%s\n' "$start_epoch"
printf 'START_UTC=%s\n' "$(date -u -r "$start_epoch" '+%Y-%m-%dT%H:%M:%SZ')"
printf 'OUTPUT_DIR=%s\n' "$output_dir"
echo 'Пришлите START_EPOCH в чат, пока сторонний VPN включён; затем отключите VPN до START_UTC.'
echo 'Проверка запустится автоматически.'

refreshed=0
while :; do
  now=$(date -u +%s)
  (( now < start_epoch )) || break
  remaining=$((start_epoch - now))
  if (( remaining <= 60 && refreshed == 0 )); then
    sudo -n -v || { echo 'Срок действия sudo истёк до старта.' >&2; exit 1; }
    refreshed=1
  fi
  sleep 1
done
now=$(date -u +%s)
(( now <= start_epoch + 2 )) || { echo 'Время START_EPOCH пропущено; повторите проверку с новым временем.' >&2; exit 1; }

sudo -n -v || { echo 'Для tcpdump требуется действующее sudo.' >&2; exit 1; }
route_text=$(route -n get "$server_ip" 2>&1) || { printf '%s\n' "$route_text" >&2; exit 1; }
printf '%s\n' "$route_text" > "$output_dir/route-before.txt"
iface=$(printf '%s\n' "$route_text" | awk '$1 == "interface:" { print $2; exit }')
case $iface in
  en[0-9]*) ;;
  *) echo "Маршрут к VPS идёт через ${iface:-неизвестный интерфейс}; отключите VPN и повторите." >&2; exit 1 ;;
esac

packets_file=$output_dir/mac-tcp.txt
capture_log=$output_dir/capture.log
: > "$packets_file"
: > "$capture_log"
sudo -n /bin/bash "$script_path" --capture "$iface" "$server_ip" > "$packets_file" 2> "$capture_log" &
capture_job=$!

stop_main_capture() {
  # sudo — прямой дочерний процесс текущего bash; захват также сам завершится за 90 с.
  if jobs -pr | grep -Fxq "$capture_job"; then
    kill -TERM "$capture_job" 2>/dev/null || :
  fi
}
trap 'stop_main_capture; exit 130' INT TERM

ready=0
for attempt in 1 2 3 4 5 6 7 8; do
  if grep -q 'listening on ' "$capture_log"; then ready=1; break; fi
  if ! jobs -pr | grep -Fxq "$capture_job"; then break; fi
  sleep 1
done
if (( ready == 0 )); then
  echo "tcpdump не начал захват. Смотрите $capture_log" >&2
  stop_main_capture
  wait "$capture_job" 2>/dev/null || :
  exit 1
fi
printf 'CAPTURE_READY_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

# Первые соединения начинаются после десяти секунд на запуск захвата на VPS.
probe_epoch=$((start_epoch + 10))
while (( $(date -u +%s) < probe_epoch )); do sleep 1; done
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

for pass in 1 2; do
  for port in 22 443 8443 8444; do
    run_probe "pass${pass}_tcp${port}" nc -4 -n -G 5 -vz "$server_ip" "$port"
  done
done
run_probe control_tcp443 nc -4 -n -G 5 -vz "$control_ip" 443
run_probe panel_https curl -4 --proxy '' --noproxy '*' \
  --resolve "$panel_host:8443:$server_ip" --connect-timeout 5 --max-time 10 \
  -sS -o /dev/null -w 'http=%{http_code} remote=%{remote_ip} connect=%{time_connect} tls=%{time_appconnect}\n' \
  "https://$panel_host:8443/readyz"
run_probe source_ipv4 curl -4 --proxy '' --noproxy '*' \
  --connect-timeout 5 --max-time 10 -fsS -w '\n' https://api.ipify.org/

route -n get "$server_ip" > "$output_dir/route-after.txt" 2>&1 || :
wait "$capture_job" || echo 'tcpdump завершился с ошибкой; смотрите capture.log.' >&2
trap - INT TERM
printf 'DONE_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
printf 'OUTPUT_DIR=%s\n' "$output_dir"
