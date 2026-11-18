#!/usr/bin/env bash
# Подготовить доступ администратора и установить выбранную роль отдельного VPS.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'HELP'
Подготовка доступа:
  sudo bash prepare-vps.sh --role bridge|node --user ИМЯ --public-key КЛЮЧ.pub --access-only
Установка из артефакта:
  sudo bash prepare-vps.sh --role bridge|node --user ИМЯ --public-key КЛЮЧ.pub \
    --archive release.tar.gz --config bridge.json|node.env [--check]
Создание ключей нового VPN-узла без node.env:
  sudo bash prepare-vps.sh --role node --user ИМЯ --public-key КЛЮЧ.pub --archive release.tar.gz \
    --initialize-node --node-host АДРЕС [--server-name www.cloudflare.com --tcp-port 8443 --xhttp-port 8444]
После добавления открытых параметров в панель:
  sudo bash prepare-vps.sh --role node --user ИМЯ --public-key КЛЮЧ.pub --archive release.tar.gz \
    --node-host АДРЕС --node-id UUID --control-url https://ПАНЕЛЬ:8443
Продолжение регистрации VPN-узла после «Применить» в панели:
  sudo bash prepare-vps.sh --role node --user ИМЯ --public-key КЛЮЧ.pub \
    --archive release.tar.gz --resume

bridge: Ubuntu 22.04/24.04/26.04 amd64; node: Ubuntu 26.04 amd64.
--check проверяет артефакт и конфигурацию без изменения доступа, пакетов и служб.
--access-only создаёт пользователя, добавляет ED25519-ключ и sudo NOPASSWD: ALL.
Первый запуск требует root либо действующего sudo; настройка SSH и firewall не меняется.
HELP
}
fail() { printf 'Подготовка VPS остановлена: %s\n' "$*" >&2; exit 1; }
# В тестах функция переопределяется после source; рабочий запуск всегда использует /.
system_path() { printf '%s\n' "$1"; }
require_root() {
  if [[ $EUID != 0 ]]; then
    command -v sudo >/dev/null || fail 'Нужен root или пользователь с установленным sudo.'
    exec sudo bash "$prepare_script" "$@"
  fi
}
read_os() {
  # Файл ОС задаётся самой системой, пользовательские конфигурации не source-ятся.
  . "$(system_path /etc/os-release)"
  [[ $ID == ubuntu && $VERSION_ID =~ ^(22\.04|24\.04|26\.04)$ ]] \
    || fail 'Поддерживается Ubuntu 22.04, 24.04 или 26.04.'
  [[ $(uname -s) == Linux && $(uname -m) =~ ^(x86_64|amd64)$ ]] \
    || fail 'Нужен Linux amd64.'
  if [[ $role == node && $access_only != true && $VERSION_ID != 26.04 ]]; then
    fail 'Полноценный VPN-узел поддерживается на Ubuntu 26.04 amd64.'
  fi
  [[ ${VERSION_CODENAME:-} =~ ^[a-z]+$ ]] || fail 'В /etc/os-release отсутствует кодовое имя Ubuntu.'
  ubuntu_codename=$VERSION_CODENAME
}

validate_key() {
  python3 - "$1" <<'PY'
import base64
from pathlib import Path
import struct
import sys

path = Path(sys.argv[1])
if not path.is_file() or path.is_symlink() or not 1 <= path.stat().st_size <= 4096:
    raise SystemExit('Нужен обычный файл одного публичного ED25519-ключа до 4096 байт.')
text = path.read_text().strip()
parts = text.split(maxsplit=2)
if len(parts) < 2 or parts[0] != 'ssh-ed25519' or any(ord(c) < 32 or ord(c) == 127 for c in text):
    raise SystemExit('Нужен один публичный ED25519-ключ без параметров authorized_keys.')
try:
    blob = base64.b64decode(parts[1], validate=True)
except ValueError:
    raise SystemExit('Неверная кодировка публичного ключа.') from None
expected = struct.pack('>I', 11) + b'ssh-ed25519' + struct.pack('>I', 32)
if len(blob) != len(expected) + 32 or not blob.startswith(expected):
    raise SystemExit('Неверное содержимое публичного ED25519-ключа.')
print(' '.join(parts))
PY
}

ensure_base_tools() {
  local missing=false tool
  for tool in python3 sudo visudo adduser runuser curl ip ss flock; do
    command -v "$tool" >/dev/null || missing=true
  done
  if [[ $missing == true ]]; then
    [[ $check != true ]] || fail 'Для --check нужны python3, sudo, adduser, curl, iproute2 и util-linux.'
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates curl python3 sudo adduser openssh-client iproute2 util-linux
  fi
}

verify_bundle() {
  local item checksum source_archive=$archive
  [[ -f $source_archive && ! -L $source_archive && $(basename -- "$source_archive") == release.tar.gz ]] \
    || fail 'Нужен обычный архив release.tar.gz.'
  checksum=$(dirname -- "$source_archive")/release.tar.gz.sha256
  [[ -f $checksum && ! -L $checksum ]] || fail 'Рядом с архивом отсутствует обычный release.tar.gz.sha256.'
  # Проверяем и запускаем неизменяемую закрытую копию входящих файлов.
  install -m 0600 "$source_archive" "$temporary/release.tar.gz"
  install -m 0600 "$checksum" "$temporary/release.tar.gz.sha256"
  for item in prepare-vps.sh verify-release.py install-bridge.sh install-node.sh setup-node.py; do
    [[ -f $script_dir/$item && ! -L $script_dir/$item ]] || fail "Отсутствует обычный файл install/$item."
    install -m 0600 "$script_dir/$item" "$temporary/$item"
  done
  archive=$temporary/release.tar.gz
  python3 - "$archive" "$temporary/release.tar.gz.sha256" <<'CHECKSUM'
import hashlib
from pathlib import Path
import re
import sys
archive, checksum = map(Path, sys.argv[1:])
expected = checksum.read_text().strip()
if not re.fullmatch(r'[0-9a-f]{64}  release\.tar\.gz', expected):
    raise SystemExit('Неверный формат контрольной суммы релиза.')
digest = hashlib.sha256()
with archive.open('rb') as source:
    for block in iter(lambda: source.read(1024 * 1024), b''):
        digest.update(block)
if digest.hexdigest() != expected[:64]:
    raise SystemExit('Контрольная сумма релиза не совпадает.')
CHECKSUM
  release_sha=$(python3 "$temporary/verify-release.py" --tar "$archive")
  [[ $release_sha =~ ^[0-9a-f]{40}$ ]] || fail 'Неверный идентификатор релиза.'
  for item in prepare-vps.sh verify-release.py install-bridge.sh install-node.sh setup-node.py; do
    tar -xOf "$archive" "docker/$item" | cmp -s - "$temporary/$item" \
      || fail "Файл $item не совпадает с проверенным релизом."
  done
  printf 'RELEASE_OK sha=%s\n' "$release_sha"
}

validate_config() {
  if [[ $role == node && $resume != true && -e $(system_path /etc/private-gateway-node/stack.env) ]]; then
    fail 'VPN-узел уже подготовлен; продолжите с --resume без новой конфигурации.'
  fi
  if [[ $resume == true ]]; then
    [[ $role == node ]] || fail '--resume предусмотрен только для VPN-узла.'
    [[ -z $config ]] || fail 'При --resume не передавайте новую конфигурацию.'
    local installed_env
    installed_env=$(system_path /etc/private-gateway-node/stack.env)
    [[ -f $installed_env && ! -L $installed_env ]] || fail 'Для --resume отсутствует сохранённая конфигурация узла.'
    return
  fi
  if [[ $role == node && -z $config ]]; then
    node_generated=true
    python3 "$temporary/setup-node.py" environment \
      --state-dir "$(system_path /etc/private-gateway-node-setup)" \
      --node-host "$node_host" --node-id "$node_id" --control-url "$control_url" --check --check-ports
    return
  fi
  [[ -f $config && ! -L $config ]] || fail 'Нужен обычный файл конфигурации из панели.'
  install -m 0600 "$config" "$temporary/config"
  if [[ $role == bridge ]]; then
    tar -xOf "$temporary/release.tar.gz" docker/bridge.py > "$temporary/bridge.py"
    python3 - "$temporary/bridge.py" "$temporary/config" <<'PY'
import importlib.util
import sys
spec = importlib.util.spec_from_file_location('prepare_bridge', sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
module.read_config(sys.argv[2])
PY
  else
    python3 - "$temporary/setup-node.py" "$temporary/config" <<'PY'
import importlib.util
import sys
spec = importlib.util.spec_from_file_location('prepare_node', sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
values = module.validate_env(sys.argv[2])
module.preflight_ports(values)
PY
  fi
}

prepare_access() {
  local record uid gid home shell home_path ssh_dir keys sudoers sudoers_tmp previous=false
  if ! record=$(getent passwd "$admin_user"); then
    adduser --disabled-password --gecos '' --home "/home/$admin_user" --shell /bin/bash "$admin_user"
    record=$(getent passwd "$admin_user") || fail 'Созданный пользователь не найден.'
  fi
  IFS=: read -r _ _ uid gid _ home shell <<< "$record"
  [[ $uid =~ ^[0-9]+$ && $uid -ge 1000 && $uid != 65534 ]] || fail 'Нужен обычный пользователь с UID не меньше 1000.'
  [[ $home == /* && $home != / && $home != /root && $home != *'/../'* && $home != */.. ]] \
    || fail 'У пользователя неверный домашний каталог.'
  [[ $shell != */nologin && $shell != */false ]] || fail 'Пользователь не допускает вход через shell.'
  home_path=$(system_path "$home")
  [[ -d $home_path && ! -L $home_path && $(stat -c %u "$home_path") == "$uid" ]] \
    || fail 'Домашний каталог должен принадлежать пользователю и не быть ссылкой.'
  ssh_dir=$home_path/.ssh
  keys=$ssh_dir/authorized_keys
  [[ ! -L $ssh_dir && ( ! -e $ssh_dir || -d $ssh_dir ) ]] || fail '.ssh имеет небезопасный тип.'
  [[ ! -L $keys && ( ! -e $keys || -f $keys ) ]] || fail 'authorized_keys имеет небезопасный тип.'
  install -d -o "$uid" -g "$gid" -m 0700 "$ssh_dir"
  python3 - "$keys" "$public_key" "$uid" "$gid" <<'PY'
import os
from pathlib import Path
import sys
import tempfile

path, key, uid, gid = sys.argv[1:]
path = Path(path)
existing = path.read_text() if path.exists() else ''
identity = key.split()[:2]
if not any(line.split()[:2] == identity for line in existing.splitlines()):
    existing += ('\n' if existing and not existing.endswith('\n') else '') + key + '\n'
fd, name = tempfile.mkstemp(prefix='.authorized_keys-', dir=path.parent)
try:
    with os.fdopen(fd, 'w') as output:
        output.write(existing)
        output.flush()
        os.fsync(output.fileno())
    os.chown(name, int(uid), int(gid))
    os.chmod(name, 0o600)
    os.replace(name, path)
finally:
    if os.path.exists(name):
        os.unlink(name)
PY
  sudoers=$(system_path "/etc/sudoers.d/private-gateway-admin-$admin_user")
  [[ -d $(dirname -- "$sudoers") && ! -L $(dirname -- "$sudoers") && ! -L $sudoers \
      && ( ! -e $sudoers || -f $sudoers ) ]] || fail 'Небезопасный каталог или файл sudoers.'
  if [[ -f $sudoers ]]; then
    install -m 0600 "$sudoers" "$temporary/sudoers.previous"
    previous=true
  fi
  sudoers_tmp=$(mktemp "$(dirname -- "$sudoers")/.private-gateway-admin.XXXXXXXX")
  printf '%s ALL=(ALL:ALL) NOPASSWD: ALL\n' "$admin_user" > "$sudoers_tmp"
  chmod 0440 "$sudoers_tmp"
  if ! visudo -cf "$sudoers_tmp"; then
    rm -f -- "$sudoers_tmp"
    fail 'Новый файл sudoers не прошёл visudo.'
  fi
  mv -f -- "$sudoers_tmp" "$sudoers"
  local sudo_uid=''
  if ! visudo -c || ! sudo_uid=$(runuser -u "$admin_user" -- sudo -n id -u) || [[ $sudo_uid != 0 ]]; then
    if [[ $previous == true ]]; then
      install -m 0440 "$temporary/sudoers.previous" "$sudoers"
    else
      rm -f -- "$sudoers"
    fi
    fail 'sudo без пароля не подтверждён; прежний файл sudoers восстановлен.'
  fi
  printf 'ACCESS_READY user=%s\n' "$admin_user"
}

ensure_docker() {
  if command -v docker >/dev/null; then
    docker compose version >/dev/null 2>&1 || fail 'Docker уже установлен, но нет Compose v2; установка не заменяется автоматически.'
    docker info >/dev/null || fail 'Docker установлен, но daemon недоступен; установка не заменяется автоматически.'
    printf 'DOCKER_READY existing=true\n'
    return
  fi
  local package
  for package in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
    if [[ $(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true) == 'install ok installed' ]]; then
      fail 'Есть несовместимые пакеты Docker/containerd; они не удаляются автоматически.'
    fi
  done
  [[ ! -L $(system_path /etc/apt/keyrings) && ! -L $(system_path /etc/apt/keyrings/docker.asc) ]] \
    || fail 'Docker APT keyring имеет небезопасный тип.'
  install -d -m 0755 "$(system_path /etc/apt/keyrings)"
  curl -q --fail --silent --show-error --proto '=https' --tlsv1.2 \
    https://download.docker.com/linux/ubuntu/gpg --output "$temporary/docker.asc"
  [[ -s $temporary/docker.asc ]] || fail 'Не удалось скачать ключ репозитория Docker.'
  install -m 0644 "$temporary/docker.asc" "$(system_path /etc/apt/keyrings/docker.asc)"
  local sources
  sources=$(system_path /etc/apt/sources.list.d/docker.sources)
  [[ ! -L $sources && ( ! -e $sources || -f $sources ) ]] || fail 'Docker APT sources имеет небезопасный тип.'
  cat > "$temporary/docker.sources" <<SOURCES
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $ubuntu_codename
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
SOURCES
  install -m 0644 "$temporary/docker.sources" "$sources"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  docker compose version >/dev/null && docker info >/dev/null \
    || fail 'Docker Engine и Compose v2 не готовы после установки.'
  printf 'DOCKER_READY existing=false\n'
}

install_role() {
  if [[ $role == bridge ]]; then
    bash "$temporary/install-bridge.sh" --archive "$temporary/release.tar.gz" --config "$temporary/config"
  elif [[ $resume == true ]]; then
    bash "$temporary/install-node.sh" --archive "$temporary/release.tar.gz" --resume
  else
    if [[ $node_generated == true ]]; then
      python3 "$temporary/setup-node.py" environment \
        --state-dir "$(system_path /etc/private-gateway-node-setup)" \
        --node-host "$node_host" --node-id "$node_id" --control-url "$control_url"
      install -m 0600 "$(system_path /etc/private-gateway-node-setup/node.env)" "$temporary/config"
    fi
    bash "$temporary/install-node.sh" --archive "$temporary/release.tar.gz" --env-file "$temporary/config"
  fi
}

initialize_node() {
  if [[ -e $(system_path /etc/private-gateway-node/stack.env) || -L $(system_path /etc/private-gateway-node/stack.env) ]]; then
    fail 'VPN-узел уже установлен; используйте --resume. Существующие ключи не изменены.'
  fi
  tar -xOf "$temporary/release.tar.gz" bin/xray > "$temporary/xray"
  chmod 0700 "$temporary/xray"
  python3 "$temporary/setup-node.py" initialize \
    --state-dir "$(system_path /etc/private-gateway-node-setup)" --xray "$temporary/xray" \
    --node-host "$node_host" --server-name "$server_name" --tcp-port "$tcp_port" --xhttp-port "$xhttp_port"
}

main() {
  prepare_script=$(readlink -f -- "${BASH_SOURCE[0]}")
  script_dir=$(dirname -- "$prepare_script")
  role='' admin_user='' key_file='' archive='' config=''
  access_only=false resume=false check=false initialize=false node_generated=false
  node_host='' node_id='' control_url='' server_name='www.cloudflare.com' tcp_port=8443 xhttp_port=8444
  local original=("$@")
  while (($#)); do
    case "$1" in
      --help|-h) usage; return 0 ;;
      --access-only) access_only=true; shift ;;
      --resume) resume=true; shift ;;
      --check) check=true; shift ;;
      --initialize-node) initialize=true; shift ;;
      --role|--user|--public-key|--archive|--config|--node-host|--node-id|--control-url|--server-name|--tcp-port|--xhttp-port)
        (($# >= 2)) || fail "Нет значения после $1."
        case "$1" in
          --role) role=$2 ;; --user) admin_user=$2 ;; --public-key) key_file=$2 ;;
          --archive) archive=$2 ;; --config) config=$2 ;;
          --node-host) node_host=$2 ;; --node-id) node_id=$2 ;; --control-url) control_url=$2 ;;
          --server-name) server_name=$2 ;; --tcp-port) tcp_port=$2 ;; --xhttp-port) xhttp_port=$2 ;;
        esac
        shift 2 ;;
      *) fail "Неизвестный параметр: $1" ;;
    esac
  done
  [[ $role == bridge || $role == node ]] || fail '--role должен быть bridge или node.'
  [[ $admin_user =~ ^[a-z_][a-z0-9_-]{0,30}$ && $admin_user != root ]] || fail 'Укажите имя обычного пользователя: латинские буквы, цифры, _ или -.'
  [[ -f $key_file && ! -L $key_file ]] || fail 'Укажите файл публичного ключа через --public-key.'
  if [[ $access_only == true ]]; then
    [[ -z $archive && -z $config && $resume == false && $check == false && $initialize == false \
       && -z $node_host && -z $node_id && -z $control_url ]] \
      || fail '--access-only нельзя совмещать с архивом, конфигурацией, --resume или --check.'
  else
    [[ -n $archive ]] || fail 'Укажите --archive release.tar.gz.'
    if [[ $initialize == true ]]; then
      [[ $role == node && -n $node_host && -z $node_id && -z $control_url && -z $config && $resume == false && $check == false ]] \
        || fail '--initialize-node требует node и --node-host без конфигурации, --resume или --check.'
    elif [[ $resume == true ]]; then
      [[ $role == node && -z $config && -z $node_host && -z $node_id && -z $control_url ]] \
        || fail '--resume допускается только для node без новой конфигурации и метаданных.'
    elif [[ $role == node && -z $config ]]; then
      [[ -n $node_host && -n $node_id && -n $control_url ]] \
        || fail 'Для node укажите --node-host, --node-id и --control-url после --initialize-node.'
    else
      [[ -n $config && -z $node_host && -z $node_id && -z $control_url ]] \
        || fail 'Укажите --config из панели без дополнительных метаданных.'
    fi
  fi
  require_root "${original[@]}"
  read_os
  ensure_base_tools
  public_key=$(validate_key "$key_file")
  temporary=$(mktemp -d)
  trap 'rm -rf -- "$temporary"' EXIT
  if [[ $check != true ]]; then
    exec 9>"$(system_path /run/lock/private-gateway-prepare.lock)"
    flock -n 9 || fail 'Уже выполняется другая подготовка VPS.'
  fi
  if [[ $access_only != true ]]; then
    verify_bundle
    if [[ $initialize == true ]]; then
      initialize_node
      return
    fi
    validate_config
    if [[ $check == true ]]; then
      printf 'PREPARE_CHECK_OK role=%s\n' "$role"
      return
    fi
  fi
  prepare_access
  if [[ $access_only == true ]]; then return; fi
  ensure_docker
  install_role
  printf 'PREPARE_DONE role=%s\n' "$role"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
