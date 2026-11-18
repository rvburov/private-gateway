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

# Имя адаптера Windows бывает кириллическим. Сохраняем UTF-8, запрещаем
# управляющие ASCII-байты; строгий ASCII-разбор полей VLESS остаётся прежним.
json_quote_interface() {
  local input=$1 output='"' character i
  [[ -n $input && ${#input} -le 256 ]] || return 1
  for (( i=0; i<${#input}; i++ )); do
    character=${input:i:1}
    [[ $character != [[:cntrl:]] ]] || return 1
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

# Сравниваются адреса приложения, но не его TUN, WFP или HTTP-клиент .NET.
# Тело ответа и ошибки curl не печатаются: для trace оставляем только проверенный IP.
check_app_targets() {
  local port=$1 label url response curl_exit metrics body http tcp tls total line trace_ip
  local reached result failed=0
  printf 'APP_TARGETS_BEGIN\n'
  for label in health trace; do
    case $label in
      health) url='https://cp.cloudflare.com/generate_204' ;;
      trace) url='https://1.1.1.1/cdn-cgi/trace' ;;
    esac
    printf 'APP_TARGET=%s URL=%s\n' "$label" "$url"
    # Без --fail: приложение считает ответом любой HTTP-код проверочного сайта.
    if response=$(curl.exe -q -4 --silent --http1.1 --proto '=https' \
        --proxy "socks5h://127.0.0.1:$port" --noproxy '' \
        --connect-timeout 10 --max-time 10 --max-filesize 4096 \
        --write-out '\nPG_METRICS:%{http_code}:%{time_connect}:%{time_appconnect}:%{time_total}' \
        "$url" 2>/dev/null); then
      curl_exit=0
    else
      curl_exit=$?
    fi
    printf 'APP_CURL_EXIT=%d\n' "$curl_exit"
    metrics=${response##*$'\n'}
    if [[ $response != *$'\n'* || ! $metrics =~ ^PG_METRICS:([0-9]{3}):([0-9]+[.][0-9]+):([0-9]+[.][0-9]+):([0-9]+[.][0-9]+)$ ]]; then
      printf 'APP_METRICS=INVALID\nAPP_TARGET_RESULT=FAIL\n'
      failed=1
      continue
    fi
    http=${BASH_REMATCH[1]}
    tcp=${BASH_REMATCH[2]}
    tls=${BASH_REMATCH[3]}
    total=${BASH_REMATCH[4]}
    body=${response%$'\n'*}
    printf 'APP_HTTP=%s APP_TCP=%s APP_TLS=%s APP_TOTAL=%s\n' "$http" "$tcp" "$tls" "$total"
    reached=no
    [[ $http =~ ^[1-5][0-9]{2}$ ]] && reached=yes
    printf 'APP_HTTP_REACHED=%s\n' "$reached"
    result=FAIL
    if [[ $label == health ]]; then
      # .NET заканчивает проверку по заголовкам. Тайм-аут чтения тела curl после
      # полученного HTTP-ответа не объявляем отказом такой проверки приложения.
      [[ $reached == yes ]] && result=PASS
    elif (( curl_exit == 0 )) && [[ $http == 2[0-9][0-9] ]]; then
      trace_ip=''
      while IFS= read -r line; do
        line=${line%$'\r'}
        if [[ $line == ip=* ]] && valid_ipv4 "${line#ip=}"; then
          trace_ip=${line#ip=}
          break
        fi
      done <<< "$body"
      if [[ -n $trace_ip ]]; then
        printf 'APP_PUBLIC_IP=%s\n' "$trace_ip"
        [[ $trace_ip == 162.35.172.6 ]] && result=PASS
      fi
    fi
    kill -0 "$XRAY_PID" 2>/dev/null || result=FAIL
    printf 'APP_TARGET_RESULT=%s\n' "$result"
    [[ $result == PASS ]] || failed=1
  done
  if (( failed == 0 )); then
    printf 'APP_TARGETS_RESULT=PASS\n'
    return 0
  fi
  printf 'APP_TARGETS_RESULT=FAIL\n'
  return 1
}

show_help() {
  cat <<'HELP'
Проверка обычного VLESS/TCP Vision через VPS-мост отдельным Windows Xray.
Перед запуском выключите все VPN и блок при обрыве. Права администратора не нужны.
Запуск в Git Bash: bash diagnose-vless-windows-direct.sh [--app-targets] [--interface ИМЯ] [путь-к-xray.exe]
По умолчанию ядро берётся из установленного Private Gateway.
Принимается TCP/Vision профиль 87.199.131.176:443; ссылка вводится скрыто.
Временный SOCKS слушает только 127.0.0.1; службы, маршруты и системный прокси не меняются.
Дополнительно можно задать PG_DIAG_LOCAL_PORT (свободный порт 1024–65535).
--app-targets: после api.ipify.org проверить адреса Cloudflare из приложения (по 10 с).
--interface ИМЯ: привязать только сокет временного Xray к точному имени адаптера Windows.
HELP
}

main() {
  set +x
  set -u
  set -o pipefail
  export LC_ALL=C
  if [[ $# == 1 && $1 == --help ]]; then show_help; return 0; fi
  local app_targets=false requested_xray='' argument interface_json='' outside_binding=off
  while (( $# > 0 )); do
    argument=$1
    case $argument in
      --app-targets)
        [[ $app_targets == false ]] || { fail 'параметр --app-targets указан повторно.'; return 1; }
        app_targets=true ;;
      --interface)
        [[ $outside_binding == off && $# -ge 2 && $2 != --* ]] || {
          fail 'после --interface нужно одно имя адаптера; повтор параметра не допускается.'; return 1;
        }
        json_quote_interface "$2" || { fail 'недопустимое имя адаптера Windows.'; return 1; }
        interface_json=',"sockopt":{"interface":'"$REPLY"'}'
        outside_binding=on
        shift ;;
      --*) fail 'неизвестный параметр; используйте --help.'; return 1 ;;
      *)
        [[ -z $requested_xray ]] || { fail 'укажите только один путь к xray.exe.'; return 1; }
        requested_xray=$argument ;;
    esac
    shift
  done
  local xray=${requested_xray:-${PG_DIAG_XRAY:-}} candidate profile config response curl_exit port version_output
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
  printf 'OUTSIDE_BINDING=%s\n' "$outside_binding"
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
  printf -v config '{"log":{"loglevel":"none","access":"none"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],"outbounds":[{"tag":"vpn","protocol":"vless","settings":{"vnext":[{"address":"87.199.131.176","port":%d,"users":[{"id":%s,"encryption":"none","flow":"xtls-rprx-vision"}]}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":%s,"fingerprint":%s,"publicKey":%s,"shortId":%s,"spiderX":%s}%s}}]}' \
    "$port" "$PROFILE_PORT" "$quoted_uuid" "$quoted_sni" "$quoted_fp" "$quoted_pbk" "$quoted_sid" "$quoted_spx" "$interface_json"
  unset interface_json
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
  if [[ $app_targets == true ]]; then
    printf 'BASELINE_RESULT=PASS\n'
    check_app_targets "$port" || return 1
  fi
  printf 'RESULT=PASS\n'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
