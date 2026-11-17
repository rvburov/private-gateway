#!/usr/bin/env bash
# Согласованный короткий захват TCP на Windows и уже готовые прямые пробы.

set -u

server_ip=162.35.172.6
control_ip=1.1.1.1
script_dir=$(cd "$(dirname "$0")" && pwd -P) || exit 2
probe_script=$script_dir/diagnose-direct-windows.sh

usage() {
  cat <<'EOF'
Git Bash от администратора: bash diagnose-direct-windows-capture.sh [--start UNIX_UTC] [--output НОВАЯ_АБСОЛЮТНАЯ_ПАПКА]

По умолчанию начало через 180 секунд. Сразу передайте START_EPOCH агенту,
потом отключите VPN. Скрипт проверяет, что pktmon остановлен и фильтры пусты,
захватывает TCP к VPS и контрольному IP и запускает прямые пробы.
Оставьте терминал открытым: Ctrl+C отменяет ожидание и проверку.
Результаты остаются в новой папке HOME; отправляйте их после просмотра.
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
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

case $(uname -s) in
  MINGW*|MSYS*) ;;
  *) echo 'Нужен Git Bash на Windows.' >&2; exit 2 ;;
esac
[[ -f $probe_script ]] || { echo 'Не найден скрипт прямых проб рядом с обёрткой.' >&2; exit 2; }
for program in pktmon.exe curl.exe route.exe ipconfig.exe cygpath; do
  command -v "$program" >/dev/null 2>&1 || { printf 'Не найдена программа: %s\n' "$program" >&2; exit 2; }
done

umask 077
if [[ -n $output_dir ]]; then
  [[ ! -e $output_dir && ! -L $output_dir ]] || { echo 'Папка результатов уже существует.' >&2; exit 2; }
  mkdir -m 700 -- "$output_dir" || exit 1
else
  output_dir=$(mktemp -d "${HOME%/}/private-gateway-capture.XXXXXX") || exit 1
fi
windows_output_dir=$(cygpath -w "$output_dir") || exit 2

# Сохраняем исходную кодировку pktmon; для проверок достаточно точных байтов и ASCII-имени ETL.
normalize() {
  local raw=$1 text=$2
  tr -d '\r' < "$raw" > "$text"
}
pktmon_read() {
  local prefix=$1 result
  shift
  pktmon.exe "$@" > "$prefix.raw" 2>&1
  result=$?
  printf '%s\n' "$result" > "$prefix.exit"
  normalize "$prefix.raw" "$prefix.txt" || {
    printf 'Не удалось сохранить ответ pktmon: %s\n' "$prefix.raw" >&2
    return 1
  }
  if (( result != 0 )); then
    printf 'pktmon %s: код %s; исходный ответ:\n' "$*" "$result" >&2
    cat "$prefix.txt" >&2
    printf 'Файлы диагностики: %s\n' "$windows_output_dir" >&2
  fi
  return "$result"
}
canonical() {
  LC_ALL=C sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$1"
}
# Русские сообщения представлены в UTF-8, OEM CP866 и ANSI CP1251 без внешнего конвертера.
inactive_phrases=(
  'Монитор пакетов не запущен'
  $'\x8c\xae\xad\xa8\xe2\xae\xe0\x20\xaf\xa0\xaa\xa5\xe2\xae\xa2\x20\xad\xa5\x20\xa7\xa0\xaf\xe3\xe9\xa5\xad'
  $'\xcc\xee\xed\xe8\xf2\xee\xf0\x20\xef\xe0\xea\xe5\xf2\xee\xe2\x20\xed\xe5\x20\xe7\xe0\xef\xf3\xf9\xe5\xed'
  'Packet Monitor is not running'
)
filter_headings=(
  'Фильтры пакетов:'
  $'\x94\xa8\xab\xec\xe2\xe0\xeb\x20\xaf\xa0\xaa\xa5\xe2\xae\xa2\x3a'
  $'\xd4\xe8\xeb\xfc\xf2\xf0\xfb\x20\xef\xe0\xea\xe5\xf2\xee\xe2\x3a'
  'Packet Filters:'
)
filter_empty_values=('Нет' $'\x8d\xa5\xe2' $'\xcd\xe5\xf2' 'None')
inactive_status() {
  local phrase
  for phrase in "${inactive_phrases[@]}"; do
    [[ $1 == "$phrase" || $1 == "$phrase." ]] && return 0
  done
  return 1
}
empty_filters() {
  local index
  for index in 0 1 2 3; do
    [[ $1 == "${filter_headings[index]} ${filter_empty_values[index]}" ||
       $1 == "${filter_headings[index]}"$'\n'"${filter_empty_values[index]}" ]] && return 0
  done
  return 1
}

# Точный ответ об остановленном мониторе достаточен для этой проверки даже при ненулевом коде.
status_exit=0
pktmon_read "$output_dir/status-before" status || status_exit=$?
status_before=$(canonical "$output_dir/status-before.txt")
inactive_status "$status_before" || {
  printf 'pktmon уже запущен или его состояние неизвестно (код %s).\n' "$status_exit" >&2
  cat "$output_dir/status-before.txt" >&2
  printf 'Файлы диагностики: %s\n' "$windows_output_dir" >&2
  exit 2
}
pktmon_read "$output_dir/filters-before" filter list || { echo 'Не удалось проверить фильтры pktmon.' >&2; exit 2; }
filters_before=$(canonical "$output_dir/filters-before.txt")
empty_filters "$filters_before" || { echo 'У pktmon уже есть фильтры или их список неизвестен; ничего не меняем.' >&2; exit 2; }
pktmon_read "$output_dir/components" list || { echo 'Не удалось снять список сетевых компонентов pktmon.' >&2; exit 2; }

now=$(date -u +%s) || exit 2
[[ -n $start_epoch ]] || start_epoch=$((now + 180))
if (( start_epoch < now - 2 || start_epoch > now + 600 )); then
  echo 'START_EPOCH прошёл или дальше чем через 10 минут.' >&2
  exit 2
fi

etl_basename=pg-direct-"$start_epoch"-"$$".etl
etl_path=$output_dir/$etl_basename
etl_windows=$(cygpath -w "$etl_path") || exit 2
txt_windows=$(cygpath -w "$output_dir/packets.txt") || exit 2
filter_own_raw=
filters_added=0
capture_started=0
probe_pid=
probe_result=
status_started=
run_stage=preparing
received_signal=none

probe_running() {
  [[ -n $probe_pid ]] && jobs -pr | grep -Fxq "$probe_pid"
}
same_filters() {
  [[ -n $filter_own_raw && -f $filter_own_raw ]] || return 1
  pktmon_read "$output_dir/filters-current" filter list || return 1
  cmp -s "$filter_own_raw" "$output_dir/filters-current.raw"
}
stop_owned() {
  (( capture_started == 1 )) || return 0
  pktmon_read "$output_dir/status-before-stop" status || {
    echo 'Не удалось проверить владельца захвата; pktmon stop не вызывается.' >&2
    return 1
  }
  local current
  current=$(canonical "$output_dir/status-before-stop.txt")
  if inactive_status "$current"; then
    echo 'pktmon уже остановлен; чужой захват не останавливаем.' >&2
    capture_started=0
    return 0
  fi
  # Чужие фильтры не мешают остановить именно наш захват; remove-all проверяется отдельно.
  if [[ $current != *"$etl_basename"* ]]; then
    echo 'Состояние pktmon изменилось и не содержит имя нашего ETL; pktmon stop не вызывается.' >&2
    return 1
  fi
  pktmon.exe stop > "$output_dir/stop.log" 2>&1 || {
    echo 'pktmon stop завершился ошибкой; проверьте stop.log и pktmon status.' >&2
    return 1
  }
  capture_started=0
  printf 'CAPTURE_END_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
}
stop_unverified_start() {
  # Только что успешный start ещё не передал управление пробам: останавливаем его сразу.
  pktmon.exe stop > "$output_dir/stop.log" 2>&1 || {
    echo 'Не удалось немедленно остановить pktmon; проверьте stop.log и pktmon status.' >&2
    return 1
  }
  capture_started=0
}
cleanup_filters() {
  (( filters_added == 1 )) || return 0
  if (( capture_started == 1 )); then
    echo 'Захват ещё работает; фильтры оставлены для сохранения состояния.' >&2
    return 1
  fi
  if ! same_filters; then
    echo 'Фильтры pktmon изменились; remove-all не вызывается. Проверьте их вручную.' >&2
    return 1
  fi
  pktmon.exe filter remove > "$output_dir/filter-remove.log" 2>&1 || {
    echo 'Не удалось удалить свои фильтры; проверьте filter-remove.log.' >&2
    return 1
  }
  filters_added=0
}
finish() {
  local result=$?
  trap - EXIT INT TERM
  stop_owned || :
  if probe_running; then
    kill -TERM "$probe_pid" 2>/dev/null || :
    wait "$probe_pid" 2>/dev/null || :
  fi
  cleanup_filters || :
  printf 'RUN_EXIT=%s\nRUN_STAGE=%s\nSIGNAL=%s\nFINISHED_UTC=%s\nCAPTURE_STILL_MARKED_ACTIVE=%s\nFILTERS_STILL_MARKED_ADDED=%s\n' \
    "$result" "$run_stage" "$received_signal" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
    "$capture_started" "$filters_added" > "$output_dir/run-result.txt"
  exit "$result"
}
interrupted() {
  received_signal=$1
  printf '\nПроверка прервана сигналом %s; DONE_UTC в этом запуске не будет.\n' "$received_signal" >&2
  printf 'Этап: %s. Результаты: %s\n' "$run_stage" "$windows_output_dir" >&2
  exit "$2"
}
trap finish EXIT
trap 'interrupted INT 130' INT
trap 'interrupted TERM 143' TERM

# После каждого добавления запоминаем полный список, в том числе для сбоя второго add.
pktmon.exe filter add "PGD_VPS_$start_epoch" -i "$server_ip" -t TCP > "$output_dir/filter-add-vps.log" 2>&1 || {
  echo 'Не удалось добавить фильтр VPS.' >&2; exit 2;
}
filters_added=1
pktmon_read "$output_dir/filters-vps" filter list || { echo 'Не удалось снять список своих фильтров.' >&2; exit 2; }
filter_own_raw=$output_dir/filters-vps.raw
pktmon.exe filter add "PGD_CTRL_$start_epoch" -i "$control_ip" -t TCP > "$output_dir/filter-add-control.log" 2>&1 || {
  echo 'Не удалось добавить контрольный фильтр.' >&2; exit 2;
}
pktmon_read "$output_dir/filters-owned" filter list || { echo 'Не удалось снять список своих фильтров.' >&2; exit 2; }
filter_own_raw=$output_dir/filters-owned.raw

# Готовые 11 проб ждут тот же START_EPOCH; отдельного второго времени нет.
bash "$probe_script" --start "$start_epoch" --output "$output_dir/probes" > "$output_dir/probe-console.log" 2>&1 &
probe_pid=$!
while ! grep -q "^START_EPOCH=$start_epoch$" "$output_dir/probe-console.log"; do
  probe_running || { echo 'Скрипт проб не подготовился.' >&2; exit 2; }
  (( $(date -u +%s) < start_epoch - 2 )) || { echo 'До старта осталось меньше двух секунд.' >&2; exit 2; }
  sleep 1
done

printf 'START_EPOCH=%s\n' "$start_epoch"
printf 'START_UTC=%s\n' "$(date -u -d "@$start_epoch" '+%Y-%m-%dT%H:%M:%SZ')"
printf 'START_LOCAL=%s\n' "$(date -d "@$start_epoch" '+%Y-%m-%d %H:%M:%S %z')"
printf 'CURRENT_LOCAL=%s\n' "$(date '+%Y-%m-%d %H:%M:%S %z')"
printf 'OUTPUT_DIR=%s\n' "$output_dir"
printf 'OUTPUT_DIR_WINDOWS=%s\n' "$windows_output_dir"
echo 'Передайте START_EPOCH агенту, затем отключите VPN до START_UTC.'
echo 'Сначала идёт отсчёт до START_LOCAL (обычно 3 минуты), затем проверка около 90 секунд.'
echo 'Оставьте это окно открытым до DONE_UTC. Ctrl+C отменяет проверку.'

run_stage=waiting_for_start
last_notice=0
while (( $(date -u +%s) < start_epoch )); do
  now=$(date -u +%s)
  if (( now - last_notice >= 5 )); then
    printf 'До старта захвата около %s с; пробы начнутся автоматически.\n' "$((start_epoch - now))"
    last_notice=$now
  fi
  probe_running || { echo 'Скрипт проб завершился до старта захвата.' >&2; exit 2; }
  sleep 1
done
now=$(date -u +%s)
if (( now > start_epoch + 2 )); then
  echo 'Время старта пропущено; захват не запускается.' >&2
  exit 2
fi
run_stage=starting_capture
pktmon.exe start --capture --comp all --pkt-size 96 --file-name "$etl_windows" --file-size 8 --log-mode circular > "$output_dir/start.log" 2>&1 || {
  echo 'pktmon start не удался; пробы будут отменены.' >&2
  exit 2
}
capture_started=1
run_stage=capturing
printf 'CAPTURE_START_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
pktmon_read "$output_dir/status-started" status || {
  echo 'Не удалось проверить готовность pktmon; немедленно останавливаем только что запущенный захват.' >&2
  stop_unverified_start || exit 1
  exit 2
}
status_started=$(canonical "$output_dir/status-started.txt")
if inactive_status "$status_started"; then
  capture_started=0
  echo 'pktmon сообщил, что захват не запущен.' >&2
  exit 2
fi
if [[ $status_started != *"$etl_basename"* ]]; then
  echo 'pktmon status не назвал наш ETL; немедленно останавливаем только что запущенный захват.' >&2
  stop_unverified_start || exit 1
  exit 2
fi
echo 'Захват запущен. Прямые пробы начнутся автоматически; дождитесь завершения.'

# Основной процесс — сторож: завершает только свой захват не позже START+84.
deadline=$((start_epoch + 84))
while (( $(date -u +%s) < deadline )); do
  probe_running || break
  sleep 1
done
stop_owned || exit 1
if probe_running; then
  echo 'Время проб истекло; останавливаем только свой дочерний скрипт.' >&2
  kill -TERM "$probe_pid" 2>/dev/null || :
fi
wait "$probe_pid" || probe_result=$?
probe_pid=
cleanup_filters || exit 1
run_stage=converting
pktmon.exe etl2txt "$etl_windows" --out "$txt_windows" --verbose 2 > "$output_dir/convert.log" 2>&1 || {
  echo 'Не удалось преобразовать ETL; исходный файл сохранён.' >&2
}
printf 'DONE_UTC=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
if [[ -n $probe_result ]]; then
  printf 'Скрипт проб завершился с кодом %s; смотрите probe-console.log.\n' "$probe_result" >&2
  exit 1
fi
run_stage=complete
echo 'Готово. Проверьте packets.txt, probes/probes.tsv и probe-console.log.'
