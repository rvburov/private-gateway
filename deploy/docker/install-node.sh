#!/usr/bin/env bash
# Установить удалённый узел из проверенного релиза без секретов панели и БД.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'EOF'
Первый запуск: sudo bash install-node.sh --archive release.tar.gz --env-file node.env
Проверка после «Применить» в панели: sudo bash install-node.sh --resume --archive release.tar.gz

node.env: обычный файл root:root 0600 с метаданными узла и локальными ключами.
Одноразовый токен читается скрыто с терминала (либо одной строкой stdin).
После первого запуска нажмите «Применить» для этого узла в панели.
--resume использует сохранённые env, релиз и токен агента, не запрашивая новый код.
Если первая регистрация не завершилась, --resume скрыто запросит новый одноразовый код.
EOF
}
fail() { printf 'Node installation stopped: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
archive=''
source_env=''
resume=false
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --resume) resume=true; shift ;;
    --archive|--env-file)
      (($# >= 2)) || fail "Missing value after $1"
      if [[ $1 == --archive ]]; then archive=$2; else source_env=$2; fi
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $EUID == 0 ]] || fail 'Run with sudo.'
[[ -f $archive ]] || fail 'Укажите архив релиза.'
if [[ $resume == true ]]; then
  [[ -z $source_env ]] || fail 'При --resume не передавайте новый env-файл.'
else
  [[ -f $source_env && ! -L $source_env ]] || fail 'Укажите обычный закрытый node.env.'
  [[ $(stat -c '%u %a' "$source_env") == '0 600' ]] || fail 'node.env должен принадлежать root и иметь режим 0600.'
fi
[[ -f $script_dir/verify-release.py ]] || fail 'Release verifier is missing.'
[[ -f $(dirname -- "$archive")/release.tar.gz.sha256 ]] || fail 'Release checksum is missing.'
python3 - "$archive" "$(dirname -- "$archive")/release.tar.gz.sha256" <<'PY'
import hashlib, pathlib, re, sys
archive, checksum = map(pathlib.Path, sys.argv[1:])
expected = checksum.read_text().strip()
if (archive.name != 'release.tar.gz' or
        not re.fullmatch(r'[0-9a-f]{64}  release\.tar\.gz', expected) or
        hashlib.file_digest(archive.open('rb'), 'sha256').hexdigest() != expected[:64]):
    raise SystemExit('Release checksum mismatch.')
PY
version=$(python3 "$script_dir/verify-release.py" --tar "$archive")
[[ $version =~ ^[0-9a-f]{40}$ ]] || fail 'Release identity is invalid.'
tar -xOf "$archive" docker/install-node.sh | cmp -s - "$script_dir/install-node.sh" \
  || fail 'Installer does not match the verified release.'
tar -xOf "$archive" docker/verify-release.py | cmp -s - "$script_dir/verify-release.py" \
  || fail 'Verifier does not match the verified release.'
command -v docker >/dev/null && docker compose version >/dev/null 2>&1 \
  || fail 'Docker Engine and Docker Compose v2 are required.'
docker info >/dev/null || fail 'Docker daemon is unavailable.'

install -d -m 0700 /etc/private-gateway-node
exec 9>/run/lock/private-gateway-node-install.lock
flock -n 9 || fail 'Another node installation is running.'
env_file=/etc/private-gateway-node/stack.env
release=/opt/private-gateway-node/releases/$version
state_dir=/var/lib/private-gateway-node
token_file=$state_dir/agent.token
needs_enroll=false
if [[ $resume == true ]]; then
  [[ -f $env_file && ! -L $env_file && $(stat -c '%u %a' "$env_file") == '0 600' ]] \
    || fail 'Сохранённый stack.env отсутствует или имеет неверные права.'
  [[ -f $release/manifest.json ]] || fail 'Сохранённый релиз отсутствует.'
  python3 - "$release" "$version" <<'PY'
import hashlib, json, pathlib, sys
root, version = pathlib.Path(sys.argv[1]), sys.argv[2]
manifest = json.loads((root / 'manifest.json').read_text())
if manifest.get('version') != version:
    raise SystemExit('Сохранённый релиз не совпадает с архивом.')
for name, digest in manifest['files'].items():
    path = root / name
    if not path.is_file() or path.is_symlink() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise SystemExit('Сохранённый релиз повреждён.')
PY
  if [[ ! -e $state_dir && ! -L $state_dir ]]; then
    install -d -o 10001 -g 10001 -m 0700 "$state_dir"
  fi
  [[ -d $state_dir && ! -L $state_dir && $(stat -c '%u %a' "$state_dir") == '10001 700' ]] \
    || fail 'Каталог состояния агента отсутствует или имеет неверные права.'
  if [[ -f $token_file && ! -L $token_file ]]; then
    [[ $(stat -c '%u %a' "$token_file") == '10001 600' ]] \
      || fail 'Сохранённый токен агента имеет неверные права.'
  elif [[ ! -e $token_file && ! -L $token_file ]]; then
    needs_enroll=true
  else
    fail 'Сохранённый токен агента имеет небезопасный тип.'
  fi
else
  [[ ! -e $env_file && ! -e $release ]] || fail 'Узел уже подготовлен; используйте --resume.'
  if [[ -n $(docker ps --all --quiet --filter label=com.docker.compose.project=private-gateway-node) ]]; then
    fail 'Контейнеры узла уже существуют.'
  fi
  install -d -m 0755 /opt/private-gateway-node /opt/private-gateway-node/releases "$release"
  tar --extract --gzip --file "$archive" --directory "$release" --no-same-owner --no-same-permissions
  chmod 0555 "$release"/bin/gateway-node "$release"/bin/xray
fi

# Установщик добавляет только эти пути и теги образов.
if [[ $resume != true ]]; then
python3 - "$source_env" "$env_file" "$release" "$version" <<'PY'
import pathlib, re, subprocess, sys
source, target, release, version = sys.argv[1:]
values = {}
for line in pathlib.Path(source).read_text().splitlines():
    if not line or line.startswith('#'):
        continue
    if '=' not in line:
        raise SystemExit('node.env contains a malformed line')
    name, value = line.split('=', 1)
    if not re.fullmatch(r'[A-Z][A-Z0-9_]*', name) or name in values or '\x00' in value:
        raise SystemExit('node.env contains an invalid or duplicate name')
    values[name] = value
tags = dict(line.split('=', 1) for line in
            subprocess.check_output(['python3', release + '/docker/image-tags.py', release], text=True).splitlines())
values.update(PG_RELEASE_SHA=version, PG_XRAY_IMAGE=tags['PG_XRAY_IMAGE'],
              GATEWAY_STATE_DIR='/var/lib/private-gateway-node',
              GATEWAY_CADDYFILE='/etc/private-gateway-node/Caddyfile')
with open(target, 'x') as output:
    for name, value in values.items():
        output.write(f'{name}={value}\n')
PY
chmod 0600 "$env_file"
install -d -o 10001 -g 10001 -m 0700 "$state_dir"
fi

files=(--env-file "$env_file" -f "$release/docker/compose.node.yml")
if grep -qx 'GATEWAY_XHTTP_ENABLED=1' "$env_file" || grep -qx 'GATEWAY_XHTTP_H3_ENABLED=1' "$env_file"; then
  files+=(-f "$release/docker/compose.xhttp.yml")
fi
if grep -qx 'GATEWAY_XHTTP_ENABLED=1' "$env_file"; then
  files+=(-f "$release/docker/compose.xhttp-legacy.yml")
fi
if grep -qx 'GATEWAY_XHTTP_H3_ENABLED=1' "$env_file"; then
  files+=(-f "$release/docker/compose.node-h3.yml")
  python3 "$release/docker/render-node-caddy.py" "$release/docker/Caddyfile.node.template" \
    /etc/private-gateway-node/Caddyfile "$env_file"
fi
compose() { docker compose "${files[@]}" "$@"; }
compose config --quiet || fail 'Node Compose configuration is invalid.'
complete=$resume
cleanup() {
  if [[ $complete != true ]]; then
    compose down --remove-orphans >/dev/null 2>&1 || true
    printf 'Incomplete node containers removed; private env and agent state remain for inspection.\n' >&2
  fi
}
trap cleanup EXIT
compose build node-agent xray
if grep -qx 'GATEWAY_XHTTP_H3_ENABLED=1' "$env_file"; then compose pull caddy; fi

if [[ $resume != true || $needs_enroll == true ]]; then
  join_token=''
  if [[ -t 0 ]]; then
    IFS= read -r -s -p 'Одноразовый токен узла: ' join_token < /dev/tty || fail 'Не удалось прочитать токен.'
    printf '\n' >&2
  else
    IFS= read -r join_token || [[ -n $join_token ]] || fail 'Не удалось прочитать токен.'
  fi
  [[ $join_token =~ ^[A-Za-z0-9_-]{43}$ ]] || fail 'Неверный формат токена.'
  printf '%s\n' "$join_token" | compose run --rm -T --no-deps node-agent enroll
  unset join_token
fi

workers=(xray)
if grep -qx 'GATEWAY_XHTTP_ENABLED=1' "$env_file" || grep -qx 'GATEWAY_XHTTP_H3_ENABLED=1' "$env_file"; then
  workers+=(xray-xhttp)
fi
# Процессы ждут первую конфигурацию агента; на первом запуске её может ещё не быть.
compose up -d --no-deps "${workers[@]}"
compose up -d --no-deps node-agent
complete=true
if [[ $resume != true && ! -f /var/lib/private-gateway-node/xray.json ]]; then
  printf 'Агент запущен. Нажмите «Применить» для узла в панели, затем запустите --resume.\n'
  exit 0
fi
compose up -d --wait --wait-timeout 180 --no-deps "${workers[@]}" || fail 'Agent did not apply healthy Xray configurations.'
if grep -qx 'GATEWAY_XHTTP_H3_ENABLED=1' "$env_file"; then
  compose up -d --wait --wait-timeout 180 --no-deps caddy || fail 'H3 Caddy service is not healthy.'
fi
printf 'Удалённый узел: настроенные процессы Xray работают. Сверьте применённую ревизию в панели.\n'
