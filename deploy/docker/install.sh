#!/usr/bin/env bash
# Установить проверенный Docker-стек; прежнюю установку на systemd заменить только после проверок.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'EOF'
Usage: sudo bash install/install.sh --host SERVER_IPV4 --domain PANEL_DOMAIN --deploy-key PUBLIC_KEY [--archive FILE] [--replace-legacy]

The domain must already resolve to this server. The installation creates a new
database and new keys. --replace-legacy stops the old systemd services after
the new images, database, API and browser worker have passed preflight checks.
Old data is backed up and retained until the new connection is tested.
EOF
}

fail() { printf 'Docker installation stopped: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
archive="$script_dir/../release.tar.gz"
host=''
domain=''
replace_legacy=false
deploy_key=''
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --replace-legacy) replace_legacy=true; shift ;;
    --host|--domain|--archive|--deploy-key)
      (($# >= 2)) || fail "Missing value after $1"
      case "$1" in
        --host) host=$2 ;;
        --domain) domain=$2 ;;
        --archive) archive=$2 ;;
        --deploy-key) deploy_key=$2 ;;
      esac
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $EUID == 0 ]] || fail 'Run the installer using sudo.'
[[ -n $host && -n $domain && -f $archive && -f $deploy_key ]] || fail 'Provide --host, --domain, --deploy-key and an existing release archive.'
[[ -f $script_dir/verify-release.py ]] || fail 'The release verifier is missing.'
public_key=$(python3 - "$deploy_key" <<'PY'
from pathlib import Path
import re
import sys

value = Path(sys.argv[1]).read_text().strip()
if not re.fullmatch(r'ssh-ed25519 [A-Za-z0-9+/]+={0,2}(?: [^\r\n]*)?', value):
    raise SystemExit('Expected one ED25519 SSH public key.')
print(' '.join(value.split()[:2]))
PY
)
exec 9>/run/lock/private-gateway-docker-install.lock
flock -n 9 || fail 'Another Docker installation is already running.'

python3 - "$host" "$domain" <<'PY'
import ipaddress
import platform
import re
import socket
import sys

host, domain = sys.argv[1:]
try:
    ipaddress.IPv4Address(host)
except ValueError:
    raise SystemExit('The gateway host must be a public IPv4 address.')
if len(domain) > 253 or '.' not in domain or not all(re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?', label) for label in domain.split('.')):
    raise SystemExit('The panel domain must be a valid DNS name.')
if host not in {entry[4][0] for entry in socket.getaddrinfo(domain, None, socket.AF_INET)}:
    raise SystemExit('The panel domain does not resolve to the requested server IPv4 address.')
os_release = platform.freedesktop_os_release()
if os_release.get('ID') != 'ubuntu' or os_release.get('VERSION_ID') != '26.04' or platform.machine() != 'x86_64':
    raise SystemExit('This installer supports Ubuntu 26.04 amd64.')
PY

[[ ! -e /etc/private-gateway-docker/stack.env ]] || fail 'A Docker installation already exists; use its update procedure.'
if [[ -e /etc/private-gateway/gateway.env && $replace_legacy != true ]]; then
  fail 'The legacy installation exists. Pass --replace-legacy to make a fresh Docker installation.'
fi
if [[ $replace_legacy == true && ! -f /etc/private-gateway/gateway.env ]]; then
  fail 'No legacy installation was found; omit --replace-legacy.'
fi

checksum_file="$(dirname -- "$archive")/release.tar.gz.sha256"
[[ -f $checksum_file ]] || fail 'The release checksum file is missing.'
python3 - "$archive" "$checksum_file" <<'PY'
import hashlib
import pathlib
import re
import sys

archive, checksum = map(pathlib.Path, sys.argv[1:])
expected = checksum.read_text().strip()
if not re.fullmatch(r'[0-9a-f]{64}  release\.tar\.gz', expected):
    raise SystemExit('Invalid release checksum file.')
if archive.name != 'release.tar.gz' or hashlib.file_digest(archive.open('rb'), 'sha256').hexdigest() != expected[:64]:
    raise SystemExit('Release checksum mismatch.')
PY
version=$(python3 "$script_dir/verify-release.py" --tar "$archive")
[[ $version =~ ^[0-9a-f]{40}$ ]] || fail 'Release verification did not return a commit SHA.'
tar -xOf "$archive" docker/install.sh | cmp -s - "$script_dir/install.sh" \
  || fail 'Installer does not match the verified release; use files from one CI artifact.'
tar -xOf "$archive" docker/verify-release.py | cmp -s - "$script_dir/verify-release.py" \
  || fail 'Verifier does not match the verified release; use files from one CI artifact.'

if ! command -v docker >/dev/null || ! docker compose version >/dev/null 2>&1; then
  apt-get update -o APT::Update::Error-Mode=any
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker.io docker-compose-v2
fi
systemctl enable --now docker.service
docker info >/dev/null || fail 'Docker daemon is not ready.'
if [[ -n $(docker ps --all --quiet --filter label=com.docker.compose.project=private-gateway) ]] \
    || docker volume inspect private-gateway_postgres-data >/dev/null 2>&1; then
  fail 'A private-gateway Docker project or database volume already exists; inspect it before installing.'
fi

release="/opt/private-gateway-docker/releases/$version"
[[ ! -e $release ]] || fail "Release directory already exists: $release"
env_file=/etc/private-gateway-docker/stack.env
compose_file="$release/docker/compose.yml"
compose() { docker compose --env-file "$env_file" --file "$compose_file" "$@"; }
# Проверки конфигурации идут в тех же закреплённых образах, что и стек.
image_of() {
  compose config --format json \
    | python3 -c 'import json, sys; print(json.load(sys.stdin)["services"][sys.argv[1]]["image"])' "$1"
}
backup=''
access_backup=''
old_api=false old_xray=false old_caddy=false old_postgres=false cutover=false success=false initialized=false
if systemctl is-active --quiet gateway-api.service; then old_api=true; fi
if systemctl is-active --quiet private-gateway-xray.service; then old_xray=true; fi
if systemctl is-active --quiet caddy.service; then old_caddy=true; fi
if systemctl is-active --quiet postgresql.service; then old_postgres=true; fi
rollback() {
  if [[ $success == true || $initialized != true ]]; then return; fi
  if [[ -n $access_backup ]]; then
    for item in helper verifier sudoers keys; do
      case "$item" in
        helper) path=/usr/local/sbin/private-gateway-docker-deploy ;;
        verifier) path=/usr/local/lib/private-gateway-docker/verify-release.py ;;
        sudoers) path=/etc/sudoers.d/private-gateway-deploy ;;
        keys) path=/var/lib/private-gateway-deploy/.ssh/authorized_keys ;;
      esac
      if [[ -f $access_backup/$item ]]; then
        cp -p "$access_backup/$item" "$path" || true
      else
        rm -f -- "$path" || true
      fi
    done
  fi
  if [[ $cutover == true ]]; then
    printf 'Docker cutover failed. Restoring the previous services.\n' >&2
    compose stop edge caddy xray || true
    if [[ $old_postgres == true ]]; then
      systemctl enable postgresql.service || true
      systemctl start postgresql.service || true
    fi
    if [[ $old_api == true ]]; then systemctl enable gateway-api.service || true; fi
    if [[ $old_xray == true ]]; then systemctl enable private-gateway-xray.service || true; fi
    if [[ $old_caddy == true ]]; then systemctl enable caddy.service || true; fi
    if [[ $old_xray == true ]]; then systemctl start private-gateway-xray.service || true; fi
    if [[ $old_api == true ]]; then systemctl start gateway-api.service || true; fi
    if [[ $old_caddy == true ]]; then systemctl start caddy.service || true; fi
  fi
  if [[ -f $env_file && -f $compose_file ]]; then compose down --volumes --remove-orphans || true; fi
  rm -f -- "$env_file" /etc/private-gateway-docker/Caddyfile /etc/private-gateway-docker/haproxy.cfg
  rm -f -- /opt/private-gateway-docker/current /etc/private-gateway-docker/installed.version
  rm -rf -- "$release" /var/lib/private-gateway-docker/xray
  printf 'Incomplete Docker installation removed; the legacy backup, if created, remains at %s\n' "$backup" >&2
}
trap rollback EXIT
initialized=true
install -d -m 0755 /opt/private-gateway-docker /opt/private-gateway-docker/releases
install -d -m 0700 /etc/private-gateway-docker /var/lib/private-gateway-docker
install -d -o 10001 -g 10001 -m 0700 /var/lib/private-gateway-docker/xray
install -d -m 0755 "$release"
tar --extract --gzip --file "$archive" --directory "$release" --no-same-owner --no-same-permissions
chmod 0555 "$release"/bin/gateway-api "$release"/bin/gatewayctl "$release"/bin/xray
# Теги по содержимому: образ сохраняет свой тег, а update.sh — его контейнер, пока не изменятся его файлы.
image_tags=$(python3 "$release/docker/image-tags.py" "$release")

python3 - "$host" "$domain" "$version" "$release/bin/gateway-api" "$image_tags" <<'PY'
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys

host, domain, version, api, image_tags = sys.argv[1:]
tags = dict(line.split('=', 1) for line in image_tags.splitlines())
if (set(tags) != {'PG_API_IMAGE', 'PG_XRAY_IMAGE', 'PG_BROWSER_IMAGE'}
        or not all(re.fullmatch(r'[0-9a-f]{32}', tag) for tag in tags.values())):
    raise SystemExit('The release image tags are invalid.')
keys = json.loads(subprocess.check_output([api, 'keygen']))
values = {
    'PG_RELEASE_SHA': version,
    **tags,
    'POSTGRES_PASSWORD': secrets.token_hex(32),
    'GATEWAY_API_TOKEN': keys['api_token'],
    'GATEWAY_MASTER_KEY': keys['master_key'],
    'GATEWAY_REALITY_PRIVATE_KEY': keys['reality_private_key'],
    'GATEWAY_SHORT_ID': keys['short_id'],
    'BROWSER_WORKER_TOKEN': secrets.token_hex(32),
    'GATEWAY_HOST': host,
    'PANEL_DOMAIN': domain,
    'GATEWAY_SERVER_NAME': 'www.cloudflare.com',
    'GATEWAY_REALITY_TARGET': 'www.cloudflare.com:443',
    'GATEWAY_STATE_DIR': '/var/lib/private-gateway-docker/xray',
    'GATEWAY_CADDYFILE': '/etc/private-gateway-docker/Caddyfile',
}
path = Path('/etc/private-gateway-docker/stack.env')
with path.open('x') as output:
    for name, value in values.items():
        output.write(f'{name}={value}\n')
os.chmod(path, 0o600)
PY

compose config --quiet
ports=$(compose config --format json | python3 "$release/docker/release-ports.py")
read -r panel_port vpn_port fallback_port < <(python3 -c \
  'import json, sys; p=json.load(sys.stdin); print(p["panel"], p["vpn"], p["fallback"])' <<< "$ports")
compose pull postgres caddy edge
compose build api
compose build xray
compose build browser

python3 - "$release/docker/Caddyfile.template" /etc/private-gateway-docker/Caddyfile "$domain" <<'PY'
from pathlib import Path
import re
import sys

source, destination = map(Path, sys.argv[1:3])
domain = sys.argv[3]
if not re.fullmatch(r'[A-Za-z0-9.-]+', domain):
    raise SystemExit('Panel domain is invalid.')
text = source.read_text().replace('__PANEL_DOMAIN__', domain)
if '__' in text:
    raise SystemExit('Caddyfile template contains an unresolved placeholder.')
destination.write_text(text)
destination.chmod(0o600)
PY
# Для имени под собственным центром сертификации Caddy (.localhost, .internal) проверка создаёт этот центр в /data.
docker run --rm --network none --read-only --tmpfs /data --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
  -v /etc/private-gateway-docker/Caddyfile:/etc/caddy/Caddyfile:ro \
  --entrypoint caddy "$(image_of caddy)" validate --config /etc/caddy/Caddyfile

python3 - "$release/docker/haproxy.cfg.template" /etc/private-gateway-docker/haproxy.cfg "$domain" <<'PY'
from pathlib import Path
import sys

source, destination = map(Path, sys.argv[1:3])
content = source.read_text().replace('__PANEL_DOMAIN__', sys.argv[3])
if '__' in content:
    raise SystemExit('HAProxy template contains an unresolved placeholder.')
destination.write_text(content)
destination.chmod(0o644)
PY
docker run --rm --network none --read-only --cap-drop=ALL \
  -v /etc/private-gateway-docker/haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro \
  --entrypoint haproxy "$(image_of edge)" -c -f /usr/local/etc/haproxy/haproxy.cfg

compose up --detach --wait postgres browser
compose run --rm --no-deps api migrate
compose up --detach --wait api

if [[ $replace_legacy == true ]]; then
  backup="/var/backups/private-gateway/$(date -u +%Y%m%dT%H%M%SZ)-before-docker"
  install -d -m 0700 "$backup"
  cp -a /etc/private-gateway/gateway.env "$backup/gateway.env"
  if systemctl is-active --quiet postgresql.service; then
    runuser -u postgres -- pg_dump -Fc --dbname=gateway > "$backup/gateway.dump"
    chmod 0600 "$backup/gateway.dump"
  fi
  printf 'Legacy configuration and database backup: %s\n' "$backup"
fi

cutover=true
if [[ $replace_legacy == true ]]; then
  systemctl stop gateway-api.service private-gateway-xray.service caddy.service
fi
compose up --detach --wait xray caddy edge
compose exec -T api /usr/local/bin/gatewayctl config apply

ready=false
for _ in $(seq 1 60); do
  if curl --noproxy '*' --fail --silent --show-error --max-time 4 \
      --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/readyz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
if [[ $ready != true ]]; then
  printf 'HTTPS readiness failed; final TLS request and Caddy logs follow.\n' >&2
  curl --noproxy '*' --verbose --output /dev/null --max-time 10 \
    --write-out 'HTTP %{http_code}; TLS verification %{ssl_verify_result}\n' \
    --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/readyz" \
    2>&1 | tail -n 45 >&2 || true
  compose logs --no-color --tail=80 caddy edge >&2 || true
  fail "Public HTTPS did not become ready. Check DNS, TCP 80/$panel_port and Caddy/edge logs."
fi
panel=$(curl --noproxy '*' --fail --silent --show-error --max-time 10 \
  --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/") \
  || fail 'Panel HTML could not be loaded through HTTPS.'
[[ $panel == *'<div id="root"></div>'* ]] || fail 'Panel HTML is missing; check API image file permissions.'
unset panel
browser_auth_status=$(curl --noproxy '*' --silent --show-error --output /dev/null \
  --write-out '%{http_code}' --max-time 10 --resolve "$domain:$panel_port:127.0.0.1" \
  "https://$domain:$panel_port/browser/vnc.html") || fail 'Browser access check failed.'
[[ $browser_auth_status == 403 ]] || fail 'Browser is not protected by administrator authentication.'
if [[ $fallback_port != 0 ]]; then
  curl --noproxy '*' --fail --silent --show-error --max-time 10 \
    --resolve "$domain:$fallback_port:127.0.0.1" "https://$domain:$fallback_port/readyz" >/dev/null \
    || fail 'Fallback HTTPS readiness check failed.'
  fallback_browser_status=$(curl --noproxy '*' --silent --show-error --output /dev/null \
    --write-out '%{http_code}' --max-time 10 --resolve "$domain:$fallback_port:127.0.0.1" \
    "https://$domain:$fallback_port/browser/vnc.html") || fail 'Fallback browser access check failed.'
  [[ $fallback_browser_status == 403 ]] || fail 'Fallback browser is not protected by administrator authentication.'
fi
compose exec -T api /usr/local/bin/gatewayctl browser start
compose exec -T browser python3 /app/smoke.py --port 6080
compose exec -T api /usr/local/bin/gatewayctl browser stop
compose exec -T api /usr/local/bin/gatewayctl status

if [[ $replace_legacy == true ]]; then
  systemctl disable gateway-api.service private-gateway-xray.service caddy.service postgresql.service
  systemctl stop postgresql.service
fi
# Использовать отдельный ключ SSH, разрешив с ним только принадлежащий root обновлятор Docker.
install -d -m 0755 /usr/local/lib/private-gateway-docker
access_stage=$(mktemp -d /var/tmp/private-gateway-access.XXXXXXXX)
for item in helper verifier sudoers keys; do
  case "$item" in
    helper) path=/usr/local/sbin/private-gateway-docker-deploy ;;
    verifier) path=/usr/local/lib/private-gateway-docker/verify-release.py ;;
    sudoers) path=/etc/sudoers.d/private-gateway-deploy ;;
    keys) path=/var/lib/private-gateway-deploy/.ssh/authorized_keys ;;
  esac
  if [[ -f $path ]]; then cp -p "$path" "$access_stage/$item"; fi
done
access_backup=$access_stage
install -m 0755 "$release/docker/update.sh" /usr/local/sbin/.private-gateway-docker-deploy.next
mv -f /usr/local/sbin/.private-gateway-docker-deploy.next /usr/local/sbin/private-gateway-docker-deploy
install -m 0644 "$release/docker/verify-release.py" /usr/local/lib/private-gateway-docker/.verify-release.next
mv -f /usr/local/lib/private-gateway-docker/.verify-release.next /usr/local/lib/private-gateway-docker/verify-release.py
if ! id gateway-deploy >/dev/null 2>&1; then
  useradd --system --user-group --home-dir /var/lib/private-gateway-deploy \
    --no-create-home --shell /bin/sh --password '*' gateway-deploy
fi
install -d -o root -g root -m 0755 /var/lib/private-gateway-deploy /var/lib/private-gateway-deploy/.ssh
sudoers_tmp=$(mktemp /etc/sudoers.d/.private-gateway-deploy.XXXXXX)
printf '%s\n' 'gateway-deploy ALL=(root) NOPASSWD: /usr/local/sbin/private-gateway-docker-deploy ""' > "$sudoers_tmp"
chmod 0440 "$sudoers_tmp"
visudo -cf "$sudoers_tmp"
mv -f "$sudoers_tmp" /etc/sudoers.d/private-gateway-deploy
keys_tmp=$(mktemp /var/lib/private-gateway-deploy/.ssh/.authorized_keys.XXXXXX)
printf 'command="sudo -n /usr/local/sbin/private-gateway-docker-deploy",restrict %s\n' "$public_key" > "$keys_tmp"
chmod 0644 "$keys_tmp"
mv -f "$keys_tmp" /var/lib/private-gateway-deploy/.ssh/authorized_keys
unset public_key
ln -sfn "$release" /opt/private-gateway-docker/current
printf '%s\n' "$version" > /etc/private-gateway-docker/installed.version
success=true
rm -rf -- "$access_backup"
printf '\nDocker installation verified. Panel: https://%s:%s/\nGateway: %s:%s\n' "$domain" "$panel_port" "$host" "$vpn_port"
printf 'The internal CLI token is in %s; keep it on this server. Set administrator credentials with admin-credentials.py.\n' "$env_file"
if [[ -n $backup ]]; then
  printf 'Legacy files and users remain for rollback until the client connection is tested. Backup: %s\n' "$backup"
fi
