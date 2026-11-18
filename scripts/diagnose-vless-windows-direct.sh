#!/usr/bin/env bash
# Проверить обычный TCP/Vision профиль моста отдельным Windows Xray без изменения сети.

fail() {
  printf 'ОШИБКА: %s\n' "$1" >&2
  return 1
}

# Возвращает декодированное ASCII-значение в REPLY, не выводя его в терминал.
url_decode() {
  local input=$1 output='' character pair i=0
  while (( i < ${#input} )); do
    character=${input:i:1}
    if [[ $character == '%' ]]; then
      pair=${input:i+1:2}
      [[ ${#pair} == 2 && $pair =~ ^[0-9A-Fa-f]{2}$ ]] || return 1
      printf -v character '%b' "\\x$pair"
      (( i += 3 ))
    else
      (( i += 1 ))
      [[ $character != '+' ]] || character=' '
    fi
    [[ $character == [[:print:]] ]] || return 1
    output+=$character
  done
  REPLY=$output
}

# JSON-строка из печатных ASCII-байтов; кавычки и обратные слеши экранируются.
json_quote() {
  local input=$1 output='"' character i
  for (( i=0; i<${#input}; i++ )); do
    character=${input:i:1}
    [[ $character == [[:print:]] ]] || return 1
    case $character in
      '"') output+='\"' ;;
      '\') output+='\\' ;;
      *) output+=$character ;;
    esac
  done
  REPLY="${output}\""
}

valid_dns_name() {
  local rest=$1 label
  [[ ${#rest} -le 253 && $rest == *.* ]] || return 1
  while :; do
    label=${rest%%.*}
    [[ ${#label} -le 63 && $label =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]] || return 1
    [[ $rest == *.* ]] || break
    rest=${rest#*.}
  done
}

parse_profile() {
  local uri=$1 authority query pair key value
  local -A values=()
  [[ $uri == vless://* && ${#uri} -le 4096 ]] || return 1
  uri=${uri#vless://}
  uri=${uri%%#*}
  [[ $uri == *\?* ]] || return 1
  authority=${uri%%\?*}
  query=${uri#*\?}
  [[ $authority == *@* && -n $query && $query != *'&' ]] || return 1
  PROFILE_UUID=${authority%@*}
  authority=${authority#*@}
  [[ $PROFILE_UUID =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]] || return 1
  [[ $authority =~ ^(87\.199\.131\.176):(443)$ ]] || return 1
  PROFILE_PORT=${BASH_REMATCH[2]}

  while :; do
    if [[ $query == *'&'* ]]; then
      pair=${query%%&*}
      query=${query#*&}
    else
      pair=$query
      query=''
    fi
    [[ $pair == *=* ]] || return 1
    key=${pair%%=*}
    value=${pair#*=}
    case $key in
      encryption|flow|type|security|sni|fp|pbk|sid|spx) ;;
      *) return 1 ;;
    esac
    [[ ! -v values[$key] ]] || return 1
    url_decode "$value" || return 1
    values[$key]=$REPLY
    [[ -n $query ]] || break
  done
  [[ ${#values[@]} == 9 ]] || return 1
  [[ ${values[encryption]} == none && ${values[flow]} == xtls-rprx-vision &&
     ${values[type]} == tcp && ${values[security]} == reality &&
     ${values[fp]} == chrome ]] || return 1
  valid_dns_name "${values[sni]}" || return 1
  [[ ${values[pbk]} =~ ^[A-Za-z0-9_-]{43}$ ]] || return 1
  [[ ${values[sid]} =~ ^[0-9A-Fa-f]{2,16}$ && $(( ${#values[sid]} % 2 )) == 0 ]] || return 1
  [[ ${values[spx]} == /* && ${#values[spx]} -le 256 ]] || return 1
  PROFILE_SNI=${values[sni]}
  PROFILE_FP=${values[fp]}
  PROFILE_PBK=${values[pbk]}
  PROFILE_SID=${values[sid]}
  PROFILE_SPX=${values[spx]}
}

valid_ipv4() {
  local value=$1 octet
  local -a octets
  [[ $value =~ ^(0|[1-9][0-9]{0,2})(\.(0|[1-9][0-9]{0,2})){3}$ ]] || return 1
  IFS=. read -r -a octets <<< "$value"
  for octet in "${octets[@]}"; do
    (( 10#$octet <= 255 )) || return 1
  done
}

# Снимок netstat только читается: к чужим локальным портам не подключаемся.
socket_snapshot() {
  REPLY=$(netstat.exe -ano -p TCP 2>/dev/null) || return 1
}

port_in_snapshot() {
  local wanted=$1 snapshot=$2 protocol endpoint rest
  while read -r protocol endpoint rest; do
    [[ $protocol == TCP && $endpoint == *:"$wanted" ]] && return 0
  done <<< "$snapshot"
  return 1
}

choose_port() {
  local snapshot candidate attempt
  socket_snapshot || { fail 'не удалось прочитать netstat.exe.'; return 1; }
  snapshot=$REPLY
  if [[ -n ${PG_DIAG_LOCAL_PORT:-} ]]; then
    [[ $PG_DIAG_LOCAL_PORT =~ ^[1-9][0-9]{3,4}$ ]] &&
      (( 10#$PG_DIAG_LOCAL_PORT >= 1024 && 10#$PG_DIAG_LOCAL_PORT <= 65535 )) || {
      fail 'PG_DIAG_LOCAL_PORT должен быть портом 1024–65535.'; return 1;
    }
    port_in_snapshot "$PG_DIAG_LOCAL_PORT" "$snapshot" && {
      fail 'выбранный локальный порт занят; другие процессы не остановлены.'; return 1;
    }
    REPLY=$PG_DIAG_LOCAL_PORT
    return 0
  fi
  for (( attempt=0; attempt<32; attempt++ )); do
    candidate=$(( 49152 + RANDOM % 16384 ))
    if ! port_in_snapshot "$candidate" "$snapshot"; then REPLY=$candidate; return 0; fi
  done
  fail 'не найден свободный локальный порт.'
}

wait_for_core() {
  local port=$1 deadline=$(( SECONDS + 8 ))
  while (( SECONDS < deadline )); do
    kill -0 "$XRAY_PID" 2>/dev/null || return 1
    socket_snapshot || return 1
    if port_in_snapshot "$port" "$REPLY"; then
      kill -0 "$XRAY_PID" 2>/dev/null && return 0
    fi
    sleep 0.2
  done
  return 1
}

cleanup() {
  local result=$? i
  trap - EXIT INT TERM
  if [[ -n ${XRAY_PID:-} ]]; then
    kill "$XRAY_PID" 2>/dev/null || :
    for (( i=0; i<10; i++ )); do
      kill -0 "$XRAY_PID" 2>/dev/null || break
      sleep 0.2
    done
    if kill -0 "$XRAY_PID" 2>/dev/null; then kill -KILL "$XRAY_PID" 2>/dev/null || :; fi
    wait "$XRAY_PID" 2>/dev/null || :
  fi
  if [[ -n ${SAVED_TTY:-} ]]; then stty "$SAVED_TTY" </dev/tty 2>/dev/null || :; fi
  return "$result"
}

show_help() {
  cat <<'HELP'
Проверка обычного VLESS/TCP Vision через VPS-мост отдельным Windows Xray.
Перед запуском выключите все VPN и блок при обрыве. Права администратора не нужны.
Запуск в Git Bash: bash diagnose-vless-windows-direct.sh [путь-к-xray.exe]
По умолчанию ядро берётся из установленного Private Gateway.
Принимается TCP/Vision профиль 87.199.131.176:443; ссылка вводится скрыто.
Временный SOCKS слушает только 127.0.0.1; службы, маршруты и системный прокси не меняются.
Дополнительно можно задать PG_DIAG_LOCAL_PORT (свободный порт 1024–65535).
HELP
}

main() {
  set +x
  set -u
  set -o pipefail
  export LC_ALL=C
  if [[ $# == 1 && $1 == --help ]]; then show_help; return 0; fi
  [[ $# -le 1 ]] || { fail 'укажите только путь к xray.exe.'; return 1; }
  local xray=${1:-${PG_DIAG_XRAY:-}} candidate profile config response curl_exit port version_output
  local quoted_uuid quoted_sni quoted_fp quoted_pbk quoted_sid quoted_spx
  if [[ -z $xray ]]; then
    for candidate in \
      '/c/Program Files/Private Gateway/cores/xray/xray.exe' \
      '/c/Program Files/Private Gateway/Service/cores/xray/xray.exe' \
      '/c/Program Files/Private Gateway Service/cores/xray/xray.exe'; do
      if [[ -f $candidate ]]; then xray=$candidate; break; fi
    done
  fi
  [[ -n $xray && -f $xray ]] || { fail 'xray.exe не найден; укажите путь к установленному ядру.'; return 1; }
  for candidate in curl.exe netstat.exe stty; do
    command -v "$candidate" >/dev/null 2>&1 || { fail "не найдена программа $candidate."; return 1; }
  done
  version_output=$("$xray" version 2>/dev/null) || { fail 'не удалось проверить версию Xray.'; return 1; }
  printf 'XRAY_VERSION=%s\n' "${version_output%%$'\n'*}"
  XRAY_PID=''
  SAVED_TTY=$(stty -g </dev/tty 2>/dev/null) || { fail 'нужен обычный интерактивный Git Bash.'; return 1; }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  IFS= read -r -s -p 'Вставьте TCP/Vision ссылку моста (ввод скрыт): ' profile </dev/tty || {
    fail 'не удалось прочитать профиль из терминала.'; return 1;
  }
  printf '\n' >/dev/tty
  parse_profile "$profile" || { fail 'нужен обычный TCP/REALITY/Vision профиль 87.199.131.176:443.'; return 1; }
  unset profile
  printf 'STAGE=profile_valid\n'
  json_quote "$PROFILE_UUID" || return 1; quoted_uuid=$REPLY
  json_quote "$PROFILE_SNI" || return 1; quoted_sni=$REPLY
  json_quote "$PROFILE_FP" || return 1; quoted_fp=$REPLY
  json_quote "$PROFILE_PBK" || return 1; quoted_pbk=$REPLY
  json_quote "$PROFILE_SID" || return 1; quoted_sid=$REPLY
  json_quote "$PROFILE_SPX" || return 1; quoted_spx=$REPLY
  choose_port || return 1
  port=$REPLY
  printf -v config '{"log":{"loglevel":"none","access":"none"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],"outbounds":[{"tag":"vpn","protocol":"vless","settings":{"vnext":[{"address":"87.199.131.176","port":%d,"users":[{"id":%s,"encryption":"none","flow":"xtls-rprx-vision"}]}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":%s,"fingerprint":%s,"publicKey":%s,"shortId":%s,"spiderX":%s}}}]}' \
    "$port" "$PROFILE_PORT" "$quoted_uuid" "$quoted_sni" "$quoted_fp" "$quoted_pbk" "$quoted_sid" "$quoted_spx"
  unset PROFILE_UUID PROFILE_PORT PROFILE_SNI PROFILE_FP PROFILE_PBK PROFILE_SID PROFILE_SPX
  unset quoted_uuid quoted_sni quoted_fp quoted_pbk quoted_sid quoted_spx
  if ! printf '%s' "$config" | "$xray" run -test -format json -config stdin: >/dev/null 2>&1; then
    fail 'Xray отклонил временную конфигурацию.'; return 1
  fi
  printf 'STAGE=config_valid\n'
  printf '%s' "$config" | "$xray" run -config stdin: >/dev/null 2>&1 &
  XRAY_PID=$!
  unset config
  wait_for_core "$port" || { fail 'временный Xray не открыл локальный SOCKS.'; return 1; }
  printf 'STAGE=xray_running\n'
  if response=$(curl.exe -q -4 --silent --fail --http1.1 --proto '=https' \
      --proxy "socks5h://127.0.0.1:$port" --noproxy '' \
      --connect-timeout 15 --max-time 30 --max-filesize 64 \
      'https://api.ipify.org/' 2>/dev/null); then
    curl_exit=0
  else
    curl_exit=$?
  fi
  printf 'CURL_EXIT=%d\n' "$curl_exit"
  (( curl_exit == 0 )) || { fail 'HTTPS через отдельный VLESS-клиент не прошёл.'; return 1; }
  kill -0 "$XRAY_PID" 2>/dev/null || { fail 'временный Xray завершился во время HTTPS-проверки.'; return 1; }
  valid_ipv4 "$response" || { fail 'сервис проверки IP вернул неверный адрес.'; return 1; }
  printf 'PUBLIC_IP=%s\n' "$response"
  [[ $response == 162.35.172.6 ]] || { fail 'адрес выхода не совпал с адресом VPS.'; return 1; }
  printf 'RESULT=PASS\n'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
