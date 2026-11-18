#!/usr/bin/env bash
# Подготовить доступ с компьютера и установить мост или выходной VPN-узел.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'EOF'
Запуск на компьютере: bash install/setup-vps.sh --role bridge|node
На Windows используйте Git Bash; на macOS/Linux — терминал bash.

Параметры (незаданные значения скрипт запросит):
  --role bridge|node    Мост либо полноценный выходной VPN-узел.
  --host HOST           Публичный IPv4 или DNS-имя нового VPS.
  --port PORT           SSH-порт, по умолчанию 22.
  --initial-user USER   Первоначальный пользователь хостинга, по умолчанию root.
  --initial-key FILE    Уже имеющийся ключ первоначального доступа (если нужен).
  --user USER           Пользователь для дальнейшего управления, по умолчанию pgadmin.
  --artifact DIR        Распакованный private-gateway-linux-amd64.
  --config FILE         bridge*.json из панели; готовый node.env — только для опытных пользователей.
  --key FILE            Отдельный ключ автоматизации Ed25519 без парольной фразы.
  --node-id UUID        Идентификатор выходного узла из панели (не секрет).
  --control-url URL     HTTPS-адрес панели управления (не секрет).
  --server-name SNI     REALITY SNI нового узла, по умолчанию www.cloudflare.com.
  --tcp-port PORT       Внешний TCP/Vision-порт нового узла, по умолчанию 8443.
  --xhttp-port PORT     Отдельный XHTTP-порт нового узла, по умолчанию 8444.
  --resume              Завершить настройку узла после «Применить» в панели, без нового env.
  --check               Проверить только локальные параметры и комплект файлов.

Ключ создаётся без парольной фразы; существующий ключ не перезаписывается.
На первом этапе возможны несколько запросов пароля пользователя VPS и sudo.
Имеющийся ключ --initial-key также может спросить свою парольную фразу.
У нового отдельного ключа автоматизации парольной фразы нет.
Проверка ключа хоста SSH остаётся включённой: сверьте отпечаток с консолью хостинга.
Пароли и одноразовый токен узла не передаются аргументами и не сохраняются в журнал.
--check не создаёт ключ, не подключается по SSH и не меняет VPS.
EOF
}
fail() { printf 'Настройка остановлена: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
role='' host='' port='' initial_user='' initial_key='' target_user='' artifact='' config='' key=''
node_id='' control_url='' server_name='www.cloudflare.com' tcp_port=8443 xhttp_port=8444
resume=false check=false
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --resume) resume=true; shift ;;
    --check) check=true; shift ;;
    --role|--host|--port|--initial-user|--initial-key|--user|--artifact|--config|--key|--node-id|--control-url|--server-name|--tcp-port|--xhttp-port)
      (($# >= 2)) || fail "Нет значения после $1"
      case "$1" in
        --role) role=$2 ;; --host) host=$2 ;; --port) port=$2 ;;
        --initial-user) initial_user=$2 ;; --user) target_user=$2 ;;
        --initial-key) initial_key=$2 ;;
        --artifact) artifact=$2 ;; --config) config=$2 ;; --key) key=$2 ;;
        --node-id) node_id=$2 ;; --control-url) control_url=$2 ;;
        --server-name) server_name=$2 ;; --tcp-port) tcp_port=$2 ;; --xhttp-port) xhttp_port=$2 ;;
      esac
      shift 2 ;;
    *) fail "Неизвестный параметр: $1" ;;
  esac
done

prompt() {
  local prompt_text=$1 prompt_default=${2:-} prompt_answer
  if [[ -n $prompt_default ]]; then
    IFS= read -r -p "$prompt_text [$prompt_default]: " prompt_answer || fail 'Ввод прерван.'
    REPLY=${prompt_answer:-$prompt_default}
  else
    IFS= read -r -p "$prompt_text: " REPLY || fail 'Ввод прерван.'
  fi
}
[[ -n $role ]] || { prompt 'Роль VPS: bridge — мост, node — выходной VPN-узел' bridge; role=$REPLY; }
[[ $role == bridge || $role == node ]] || fail 'Роль должна быть bridge либо node.'
[[ -n $host ]] || { prompt 'Публичный IPv4 или DNS-имя нового VPS'; host=$REPLY; }
[[ -n $port ]] || { prompt 'SSH-порт' 22; port=$REPLY; }
[[ -n $initial_user ]] || { prompt 'Первоначальный пользователь из данных хостинга' root; initial_user=$REPLY; }
[[ -n $target_user ]] || { prompt 'Пользователь для дальнейшего управления' pgadmin; target_user=$REPLY; }

valid_host() {
  local host_label host_octet
  [[ ${#host} -le 253 && $host != *$'\n'* && $host != *$'\r'* ]] || return 1
  if [[ $host =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    local IFS=.
    for host_octet in $host; do
      [[ ${#host_octet} -le 3 && $host_octet =~ ^(0|[1-9][0-9]*)$ ]] || return 1
      ((10#$host_octet <= 255)) || return 1
    done
    return 0
  fi
  [[ $host == *.* && $host != .* && $host != *. && $host != *..* ]] || return 1
  local IFS=.
  for host_label in $host; do
    [[ ${#host_label} -le 63 && $host_label =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
}
valid_host || fail 'Укажите IPv4 без ведущих нулей либо DNS-имя без схемы, пути и порта.'
[[ $port =~ ^[0-9]{1,5}$ ]] && ((10#$port >= 1 && 10#$port <= 65535)) \
  || fail 'SSH-порт должен быть числом от 1 до 65535.'
port=$((10#$port))
[[ $initial_user =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || fail 'Неверное имя первоначального SSH-пользователя.'
[[ $target_user =~ ^[a-z_][a-z0-9_-]{0,30}$ && $target_user != root ]] \
  || fail 'Для управления укажите обычного пользователя Linux, не root.'
[[ $resume != true || $role == node ]] || fail '--resume предназначен только для выходного узла node.'
[[ $resume != true || -z $config ]] || fail 'При --resume новый конфигурационный файл не передаётся.'
if [[ -n $initial_key ]]; then
  [[ -s $initial_key && -f $initial_key && ! -L $initial_key ]] || fail 'Не найден обычный файл первоначального SSH-ключа.'
  initial_key=$(cd -- "$(dirname -- "$initial_key")" && printf '%s/%s' "$(pwd -P)" "${initial_key##*/}")
fi

artifact_default="$HOME/Downloads/private-gateway-linux-amd64"
if [[ ${script_dir##*/} == install && -f $script_dir/../release.tar.gz ]]; then
  artifact_default=$(cd -- "$script_dir/.." && pwd -P)
fi
[[ -n $artifact ]] || { prompt 'Папка распакованного релиза' "$artifact_default"; artifact=$REPLY; }
[[ -d $artifact ]] || fail "Нет папки релиза: $artifact"
artifact=$(cd -- "$artifact" && pwd -P)

if [[ $role == bridge && -z $config ]]; then
  candidates=()
  candidate_count=0
  shopt -s nullglob
  for config_dir in "$HOME/Downloads" "$artifact"; do
    if [[ $role == bridge ]]; then
      config_files=("$config_dir"/bridge.json "$config_dir"/bridge-*.json)
    else
      config_files=("$config_dir"/node.env "$config_dir"/node-*.env)
    fi
    for config_candidate in "${config_files[@]}"; do
      [[ -s $config_candidate && -f $config_candidate && ! -L $config_candidate ]] || continue
      config_seen=false
      for ((candidate_index=0; candidate_index<candidate_count; candidate_index++)); do
        [[ $config_candidate != "${candidates[candidate_index]}" ]] || config_seen=true
      done
      if [[ $config_seen != true ]]; then
        candidates[candidate_count]=$config_candidate
        candidate_count=$((candidate_count + 1))
      fi
    done
  done
  shopt -u nullglob
  config_default=''
  if ((candidate_count == 1)); then
    config_default=${candidates[0]}
  elif ((candidate_count > 1)); then
    printf 'Найдено несколько конфигураций; укажите нужный файл полностью:\n'
    printf '  %s\n' "${candidates[@]}"
  fi
  prompt 'Файл конфигурации, скачанный из панели' "$config_default"
  config=$REPLY
fi
[[ $role != bridge || -n $config ]] || fail 'Для моста укажите один конфигурационный файл явно.'
if [[ -n $config ]]; then
  [[ -s $config && -f $config && ! -L $config ]] || fail "Нет обычного непустого файла конфигурации: $config"
  config=$(cd -- "$(dirname -- "$config")" && printf '%s/%s' "$(pwd -P)" "${config##*/}")
fi
[[ -n $key ]] || key="$HOME/.ssh/private_gateway_${role}_${host}_ed25519"
[[ $key == /* ]] || key="$PWD/$key"
[[ $key != *$'\n'* && $key != *$'\r'* ]] || fail 'Путь ключа содержит перевод строки.'

valid_node_fields() {
  [[ -z $node_id || $node_id =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]] \
    || fail 'Идентификатор узла должен быть UUID из панели.'
  node_id=$(printf '%s' "$node_id" | tr 'A-F' 'a-f')
  while [[ $control_url == */ ]]; do control_url=${control_url%/}; done
  [[ -z $control_url || $control_url =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$ ]] \
    || fail 'Нужен HTTPS-адрес панели без логина, пароля, пути, query и fragment.'
  [[ $server_name =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ && ${#server_name} -le 253 ]] \
    || fail 'Неверное имя REALITY SNI.'
  local node_port
  for node_port in "$tcp_port" "$xhttp_port"; do
    [[ $node_port =~ ^[0-9]{1,5}$ ]] && ((10#$node_port >= 1 && 10#$node_port <= 65535)) \
      || fail 'Порты TCP/Vision и XHTTP должны быть от 1 до 65535.'
  done
  tcp_port=$((10#$tcp_port)); xhttp_port=$((10#$xhttp_port))
  [[ $tcp_port != "$xhttp_port" ]] || fail 'TCP/Vision и XHTTP нужны разные внешние порты.'
  [[ $tcp_port != "$port" && $xhttp_port != "$port" && $tcp_port != 22 && $xhttp_port != 22 ]] \
    || fail 'TCP/Vision и XHTTP не должны занимать SSH-порт VPS или порт 22.'
}
[[ $role != node ]] || valid_node_fields

for setup_tool in ssh scp ssh-keygen mktemp tar cmp tee date tr; do
  command -v "$setup_tool" >/dev/null || fail "Не найдена программа $setup_tool. На Windows установите Git for Windows и запустите Git Bash."
done
if command -v sha256sum >/dev/null; then
  checksum_command=(sha256sum)
elif command -v shasum >/dev/null; then
  checksum_command=(shasum -a 256)
else
  fail 'Не найдена sha256sum или shasum для проверки релиза.'
fi
installer="install-$role.sh"
helper_files=(prepare-vps.sh verify-release.py install-bridge.sh install-node.sh setup-node.py)
for setup_file in release.tar.gz release.tar.gz.sha256 "${helper_files[@]/#/install/}"; do
  [[ -s $artifact/$setup_file && -f $artifact/$setup_file && ! -L $artifact/$setup_file ]] \
    || fail "Нет файла $artifact/$setup_file. Скачайте и распакуйте новый артефакт релиза."
done
checksum_line=$(cat -- "$artifact/release.tar.gz.sha256")
[[ $checksum_line =~ ^([0-9a-f]{64})\ \ release\.tar\.gz$ ]] || fail 'Неверный формат release.tar.gz.sha256.'
checksum_expected=${BASH_REMATCH[1]}
checksum_actual=$("${checksum_command[@]}" "$artifact/release.tar.gz") || fail 'Не удалось вычислить SHA-256 архива.'
[[ ${checksum_actual%% *} == "$checksum_expected" ]] || fail 'SHA-256 release.tar.gz не совпадает.'
for setup_file in "${helper_files[@]}"; do
  (set -o pipefail; tar -xOf "$artifact/release.tar.gz" "docker/$setup_file" | cmp -s - "$artifact/install/$setup_file") \
    || fail "install/$setup_file не совпадает с архивом релиза. Скачайте артефакт заново."
done
printf 'LOCAL_ARTIFACT_OK\n'
if [[ $check == true ]]; then
  printf 'LOCAL_SETUP_CHECK_OK role=%s\n' "$role"
  exit 0
fi

log_root="$HOME/.private-gateway/setup"
mkdir -p -- "$log_root"
log_dir=$(mktemp -d "$log_root/$(date -u +%Y%m%dT%H%M%SZ)-$role-$host.XXXXXX")
setup_log="$log_dir/setup.log"
exec > >(tee -a "$setup_log") 2>&1
printf 'SETUP_LOG=%s\n' "$setup_log"

local_work=$(mktemp -d)
access_stage='' install_stage='' key_ready=false
shell_quote() { printf "'%s'" "${1//\'/\'\\\'\'}"; }
remote_command() {
  local remote_arg
  for remote_arg in "$@"; do shell_quote "$remote_arg"; printf ' '; done
}
ssh_base=(-F /dev/null -o ControlPath=none -o ConnectTimeout=15 -o ServerAliveInterval=15 -o ServerAliveCountMax=3)
ssh_initial=("${ssh_base[@]}")
[[ -z $initial_key ]] || ssh_initial+=(-o IdentitiesOnly=yes -i "$initial_key")
ssh_key=(-o IdentitiesOnly=yes -o IdentityAgent=none -o PreferredAuthentications=publickey -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o BatchMode=yes -i "$key")
initial_destination="$initial_user@$host"
destination="$target_user@$host"
cleanup() {
  local setup_exit=$?
  trap - EXIT
  if [[ $key_ready == true ]]; then
    if [[ -n $install_stage ]]; then
      ssh "${ssh_base[@]}" "${ssh_key[@]}" -n -p "$port" "$destination" \
        "$(remote_command rm -rf -- "$install_stage")" >/dev/null 2>&1 || true
    fi
    if [[ -n $access_stage ]]; then
      ssh "${ssh_base[@]}" "${ssh_key[@]}" -n -p "$port" "$destination" \
        "$(remote_command sudo -n rm -rf -- "$access_stage")" >/dev/null 2>&1 || true
    fi
  elif [[ -n $access_stage ]]; then
    printf 'Временные открытые файлы доступа остались на VPS: %s\n' "$access_stage" >&2
  fi
  rm -rf -- "$local_work"
  exit "$setup_exit"
}
trap cleanup EXIT

key_existed=false
if [[ -e $key || -e $key.pub || -L $key || -L $key.pub ]]; then
  key_existed=true
  [[ -s $key && -f $key && ! -L $key && -s $key.pub && -f $key.pub && ! -L $key.pub ]] \
    || fail 'Путь ключа уже занят неполным комплектом или ссылкой; укажите новый --key, файлы не перезаписаны.'
  printf 'Используется существующий отдельный ключ: %s\n' "$key"
else
  key_dir=$(dirname -- "$key")
  if [[ ! -d $key_dir ]]; then mkdir -p -- "$key_dir"; chmod 700 -- "$key_dir"; fi
  ssh-keygen -q -t ed25519 -a 100 -N '' -f "$key" -C private-gateway-automation \
    || fail 'Не удалось создать отдельный SSH-ключ.'
fi
ssh-keygen -y -P '' -f "$key" > "$local_work/derived.pub" 2>/dev/null \
  || fail 'Ключ не читается без парольной фразы. Укажите отдельный --key; существующий файл не изменён.'
IFS=' ' read -r public_kind public_data public_comment < "$key.pub" || fail 'Не удалось прочитать публичный ключ.'
IFS=' ' read -r derived_kind derived_data derived_comment < "$local_work/derived.pub" || fail 'Не удалось проверить пару ключей.'
[[ $public_kind == ssh-ed25519 && $derived_kind == "$public_kind" && $derived_data == "$public_data" ]] \
  || fail 'Публичный и закрытый ключи не составляют пару Ed25519.'
chmod 600 -- "$key"
printf 'LOCAL_KEY_OK\n'

access_test_command="test \"\$(id -un)\" = $(shell_quote "$target_user") && printf 'SSH_KEY_OK\\n' && test \"\$(sudo -n id -u)\" = 0 && printf 'SUDO_PASSWORDLESS_OK\\n'"
if [[ $key_existed == true ]] && ssh "${ssh_base[@]}" "${ssh_key[@]}" -n -p "$port" "$destination" "$access_test_command"; then
  key_ready=true
  printf 'EXISTING_ACCESS_OK\n'
else
if [[ -z $initial_key && -t 0 ]]; then
  prompt 'Путь уже имеющегося ключа хостинга; Enter — обычный вход SSH'
  initial_key=$REPLY
  if [[ -n $initial_key ]]; then
    [[ -s $initial_key && -f $initial_key && ! -L $initial_key ]] || fail 'Не найден обычный файл первоначального SSH-ключа.'
    initial_key=$(cd -- "$(dirname -- "$initial_key")" && printf '%s/%s' "$(pwd -P)" "${initial_key##*/}")
    ssh_initial+=(-o IdentitiesOnly=yes -i "$initial_key")
  fi
fi
printf '\nПервоначальный доступ: %s, порт %s. Возможны повторные запросы пароля VPS.\n' "$initial_destination" "$port"
printf 'При первом входе сверьте показанный отпечаток ключа хоста с консолью хостинга.\n'
access_stage=$(ssh "${ssh_initial[@]}" -p "$port" "$initial_destination" \
  'umask 077; mktemp -d /tmp/private-gateway-access.XXXXXXXX') \
  || fail 'Первоначальное SSH-подключение не выполнено.'
access_stage=${access_stage%$'\r'}
[[ $access_stage =~ ^/tmp/private-gateway-access\.[A-Za-z0-9]{8}$ ]] \
  || { access_stage=''; fail 'VPS вернул неожиданный путь временной папки.'; }
scp -O "${ssh_initial[@]}" -P "$port" "$artifact/install/prepare-vps.sh" "$initial_destination:$access_stage/prepare-vps.sh" \
  || fail 'Не удалось передать подготовку доступа.'
scp -O "${ssh_initial[@]}" -P "$port" "$key.pub" "$initial_destination:$access_stage/automation.pub" \
  || fail 'Не удалось передать публичный SSH-ключ.'
access_args=(bash "$access_stage/prepare-vps.sh" --role "$role" --user "$target_user" --public-key "$access_stage/automation.pub" --access-only)
if [[ $initial_user != root ]]; then access_args=(sudo -H "${access_args[@]}"); fi
ssh "${ssh_initial[@]}" -tt -p "$port" "$initial_destination" "$(remote_command "${access_args[@]}")" \
  || fail 'Подготовка пользователя, ключа или sudo не завершена.'

ssh "${ssh_base[@]}" "${ssh_key[@]}" -n -p "$port" "$destination" \
  "$access_test_command" \
  || fail 'Новый вход по ключу или sudo без пароля не прошёл проверку.'
key_ready=true
fi
install_stage=$(ssh "${ssh_base[@]}" "${ssh_key[@]}" -n -p "$port" "$destination" \
  'umask 077; mktemp -d /tmp/private-gateway-install.XXXXXXXX') \
  || fail 'Не удалось создать закрытую временную папку установки.'
install_stage=${install_stage%$'\r'}
[[ $install_stage =~ ^/tmp/private-gateway-install\.[A-Za-z0-9]{8}$ ]] \
  || { install_stage=''; fail 'VPS вернул неожиданный путь папки установки.'; }
for setup_file in release.tar.gz release.tar.gz.sha256 "${helper_files[@]/#/install/}"; do
  scp -O "${ssh_base[@]}" "${ssh_key[@]}" -P "$port" "$artifact/$setup_file" "$destination:$install_stage/${setup_file##*/}" \
    || fail "Не удалось передать ${setup_file##*/}."
done
scp -O "${ssh_base[@]}" "${ssh_key[@]}" -P "$port" "$key.pub" "$destination:$install_stage/automation.pub" \
  || fail 'Не удалось передать публичный ключ для установщика.'
install_args=(sudo -n bash "$install_stage/prepare-vps.sh" --role "$role" --user "$target_user" --public-key "$install_stage/automation.pub" --archive "$install_stage/release.tar.gz")
if [[ $resume == true ]]; then
  install_args+=(--resume)
elif [[ -n $config ]]; then
  scp -O "${ssh_base[@]}" "${ssh_key[@]}" -P "$port" "$config" "$destination:$install_stage/input.conf" \
    || fail 'Не удалось передать конфигурацию в закрытую папку установки.'
  install_args+=(--config "$install_stage/input.conf")
else
  initialize_args=("${install_args[@]}" --initialize-node --node-host "$host" --server-name "$server_name" --tcp-port "$tcp_port" --xhttp-port "$xhttp_port")
  ssh "${ssh_base[@]}" "${ssh_key[@]}" -tt -p "$port" "$destination" "$(remote_command "${initialize_args[@]}")" \
    || fail 'Не удалось подготовить отдельные ключи TCP/Vision и XHTTP на VPS.'
  printf '\nНа сайте откройте «Серверы», добавьте выходной узел и перенесите показанные выше открытые параметры.\n'
  printf 'Закрытые ключи остаются на новом VPS. Из панели понадобятся UUID узла и одноразовый токен.\n'
  [[ -n $node_id ]] || { prompt 'UUID добавленного узла из панели'; node_id=$REPLY; }
  [[ -n $control_url ]] || { prompt 'HTTPS-адрес панели, например https://panel.example.org:8443'; control_url=$REPLY; }
  valid_node_fields
  install_args+=(--node-host "$host" --node-id "$node_id" --control-url "$control_url" --server-name "$server_name" --tcp-port "$tcp_port" --xhttp-port "$xhttp_port")
fi
printf '\nУстановка роли %s. Для node одноразовый токен вводится скрыто в терминале VPS.\n' "$role"
ssh "${ssh_base[@]}" "${ssh_key[@]}" -tt -p "$port" "$destination" "$(remote_command "${install_args[@]}")" \
  || fail 'Установка остановилась; сообщение VPS приведено выше.'
printf 'SETUP_DONE role=%s\n' "$role"
printf 'SETUP_LOG=%s\n' "$setup_log"
printf 'Ключ управления на компьютере: %s\n' "$key"
printf 'Вход: ssh -i %s -o IdentitiesOnly=yes -p %s %s\n' "$(shell_quote "$key")" "$port" "$destination"
if [[ $role == node && $resume != true ]]; then
  printf 'В панели нажмите «Применить» для узла, затем повторите эту команду с --resume без --config.\n'
else
  printf 'Импортируйте обычный профиль в клиент и проверьте выходной IP. Готовность служб не подтверждает VLESS-аутентификацию.\n'
fi
