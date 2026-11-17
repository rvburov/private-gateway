#!/usr/bin/env bash
# Разовая проверка TCP/Vision через внешний SOCKS без изменения работающих служб.

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
  [[ $authority =~ ^(162\.35\.172\.6|rb-gateway-3708079\.duckdns\.org):(443|8443)$ ]] || return 1
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

cleanup() {
  local i
  trap - EXIT INT TERM
  if [[ -n ${XRAY_PID:-} ]]; then
    kill "$XRAY_PID" 2>/dev/null || :
    for (( i=0; i<10; i++ )); do
      kill -0 "$XRAY_PID" 2>/dev/null || break
      sleep 0.2
    done
    if kill -0 "$XRAY_PID" 2>/dev/null; then
      kill -KILL "$XRAY_PID" 2>/dev/null || :
    fi
    wait "$XRAY_PID" 2>/dev/null || :
  fi
}

show_help() {
  cat <<'HELP'
Проверка VLESS/TCP Vision через IPRoyal SOCKS с Windows без изменения VPN-служб.
Запуск в Git Bash: bash scripts/diagnose-vless-windows-chain.sh [путь-к-xray.exe]
Нужны действующий TCP/Vision профиль Server-gateway и логин/пароль IPRoyal.
Ввод скрыт; профиль и пароли не сохраняются и не передаются в аргументах процессов.
HELP
}

main() {
  set +x
  set -u
  set -o pipefail
  export LC_ALL=C
  if [[ $# == 1 && $1 == --help ]]; then show_help; return 0; fi
  [[ $# -le 1 ]] || { fail 'укажите только путь к xray.exe.'; return 1; }

  local xray='' candidate profile proxy_user proxy_pass config response curl_exit
  local quoted_uuid quoted_sni quoted_fp quoted_pbk quoted_sid quoted_spx quoted_user quoted_pass
  local port
  if [[ $# == 1 ]]; then
    xray=$1
  else
    for candidate in \
      '/c/Program Files/Private Gateway/cores/xray/xray.exe' \
      '/c/Program Files/Private Gateway/Service/cores/xray/xray.exe' \
      '/c/Program Files/Private Gateway Service/cores/xray/xray.exe'; do
      if [[ -f $candidate ]]; then xray=$candidate; break; fi
    done
  fi
  [[ -n $xray && -f $xray ]] || { fail 'xray.exe не найден; укажите путь к установленному ядру.'; return 1; }
  command -v curl.exe >/dev/null 2>&1 || { fail 'curl.exe не найден.'; return 1; }

  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  XRAY_PID=''

  IFS= read -r -s -p 'Вставьте действующий VLESS TCP/Vision профиль: ' profile </dev/tty || {
    fail 'не удалось прочитать профиль из терминала.'; return 1;
  }
  printf '\n' >/dev/tty
  parse_profile "$profile" || { fail 'профиль не соответствует этому TCP/REALITY/Vision серверу.'; return 1; }
  unset profile
  printf 'STAGE=profile_valid\n'

  IFS= read -r -s -p 'Логин IPRoyal: ' proxy_user </dev/tty || {
    fail 'не удалось прочитать логин из терминала.'; return 1;
  }
  printf '\n' >/dev/tty
  IFS= read -r -s -p 'Пароль IPRoyal: ' proxy_pass </dev/tty || {
    fail 'не удалось прочитать пароль из терминала.'; return 1;
  }
  printf '\n' >/dev/tty
  [[ -n $proxy_user && -n $proxy_pass && ${#proxy_user} -le 255 &&
     ${#proxy_pass} -le 255 && $proxy_user != *:* ]] || {
    fail 'неверный формат учётных данных SOCKS.'; return 1;
  }

  json_quote "$PROFILE_UUID" || { fail 'неверный профиль.'; return 1; }; quoted_uuid=$REPLY
  json_quote "$PROFILE_SNI" || { fail 'неверный профиль.'; return 1; }; quoted_sni=$REPLY
  json_quote "$PROFILE_FP" || { fail 'неверный профиль.'; return 1; }; quoted_fp=$REPLY
  json_quote "$PROFILE_PBK" || { fail 'неверный профиль.'; return 1; }; quoted_pbk=$REPLY
  json_quote "$PROFILE_SID" || { fail 'неверный профиль.'; return 1; }; quoted_sid=$REPLY
  json_quote "$PROFILE_SPX" || { fail 'неверный профиль.'; return 1; }; quoted_spx=$REPLY
  json_quote "$proxy_user" || { fail 'учётные данные SOCKS содержат неподдерживаемые символы.'; return 1; }; quoted_user=$REPLY
  json_quote "$proxy_pass" || { fail 'учётные данные SOCKS содержат неподдерживаемые символы.'; return 1; }; quoted_pass=$REPLY
  unset proxy_user proxy_pass PROFILE_UUID PROFILE_SNI PROFILE_FP PROFILE_PBK PROFILE_SID PROFILE_SPX

  port=$(( 49152 + RANDOM % 16384 ))
  printf -v config '{"log":{"loglevel":"none","access":"none"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth","udp":false}}],"outbounds":[{"tag":"vpn","protocol":"vless","settings":{"vnext":[{"address":"162.35.172.6","port":%d,"users":[{"id":%s,"encryption":"none","flow":"xtls-rprx-vision"}]}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"serverName":%s,"fingerprint":%s,"publicKey":%s,"shortId":%s,"spiderX":%s},"sockopt":{"dialerProxy":"carrier"}}},{"tag":"carrier","protocol":"socks","settings":{"address":"155.103.141.121","port":12324,"user":%s,"pass":%s}}]}' \
    "$port" "$PROFILE_PORT" "$quoted_uuid" "$quoted_sni" "$quoted_fp" "$quoted_pbk" "$quoted_sid" "$quoted_spx" "$quoted_user" "$quoted_pass"
  unset PROFILE_PORT quoted_uuid quoted_sni quoted_fp quoted_pbk quoted_sid quoted_spx quoted_user quoted_pass

  if ! printf '%s' "$config" | "$xray" run -test -format json -config stdin: >/dev/null 2>&1; then
    fail 'Xray отклонил временную конфигурацию.'; return 1
  fi
  printf 'STAGE=config_valid\n'
  printf '%s' "$config" | "$xray" run -config stdin: >/dev/null 2>&1 &
  XRAY_PID=$!
  unset config
  sleep 1
  if ! kill -0 "$XRAY_PID" 2>/dev/null; then
    fail 'временный Xray завершился при запуске.'; return 1
  fi
  printf 'STAGE=xray_running\n'

  if response=$(curl.exe -q --silent --fail --http1.1 --proto '=https' \
      --proxy "socks5h://127.0.0.1:$port" --noproxy '' \
      --connect-timeout 8 --max-time 25 --max-filesize 64 \
      'https://api.ipify.org/' 2>/dev/null); then
    curl_exit=0
  else
    curl_exit=$?
  fi
  printf 'CURL_EXIT=%d\n' "$curl_exit"
  if (( curl_exit != 0 )); then
    fail 'HTTPS через временный VLESS-клиент не прошёл.'; return 1
  fi
  if ! kill -0 "$XRAY_PID" 2>/dev/null; then
    fail 'временный Xray завершился во время HTTPS-проверки.'; return 1
  fi
  valid_ipv4 "$response" || { fail 'сервис проверки IP вернул неверный адрес.'; return 1; }
  printf 'PUBLIC_IP=%s\n' "$response"
  [[ $response == 162.35.172.6 ]] || { fail 'адрес выхода не совпал с адресом VPS.'; return 1; }
  printf 'RESULT=PASS\n'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  ( main "$@" )
fi
