#!/usr/bin/env bash
# Install the verified Docker stack, replacing a legacy systemd installation only after checks.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'EOF'
Usage: sudo bash install/install.sh --host SERVER_IPV4 --domain PANEL_DOMAIN [--archive FILE] [--replace-legacy]

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
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --replace-legacy) replace_legacy=true; shift ;;
    --host|--domain|--archive)
      (($# >= 2)) || fail "Missing value after $1"
      case "$1" in
        --host) host=$2 ;;
        --domain) domain=$2 ;;
        --archive) archive=$2 ;;
      esac
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $EUID == 0 ]] || fail 'Run the installer using sudo.'
[[ -n $host && -n $domain && -f $archive ]] || fail 'Provide --host, --domain and an existing release archive.'
[[ -f $script_dir/verify-release.py ]] || fail 'The release verifier is missing.'
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
if docker ps --all --quiet --filter label=com.docker.compose.project=private-gateway | grep -q . \
    || docker volume inspect private-gateway_postgres-data >/dev/null 2>&1; then
  fail 'A private-gateway Docker project or database volume already exists; inspect it before installing.'
fi

release="/opt/private-gateway-docker/releases/$version"
[[ ! -e $release ]] || fail "Release directory already exists: $release"
env_file=/etc/private-gateway-docker/stack.env
compose_file="$release/docker/compose.yml"
compose() { docker compose --env-file "$env_file" --file "$compose_file" "$@"; }
backup=''
old_api=false old_xray=false old_caddy=false cutover=false success=false initialized=false
if systemctl is-active --quiet gateway-api.service; then old_api=true; fi
if systemctl is-active --quiet private-gateway-xray.service; then old_xray=true; fi
if systemctl is-active --quiet caddy.service; then old_caddy=true; fi
rollback() {
  if [[ $success == true || $initialized != true ]]; then return; fi
  if [[ $cutover == true ]]; then
    printf 'Docker cutover failed. Restoring the previous services.\n' >&2
    compose stop caddy xray || true
    if [[ $old_xray == true ]]; then systemctl start private-gateway-xray.service || true; fi
    if [[ $old_api == true ]]; then systemctl start gateway-api.service || true; fi
    if [[ $old_caddy == true ]]; then systemctl start caddy.service || true; fi
  fi
  if [[ -f $env_file && -f $compose_file ]]; then compose down --volumes --remove-orphans || true; fi
  rm -f -- "$env_file" /etc/private-gateway-docker/Caddyfile
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

python3 - "$host" "$domain" "$version" "$release/bin/gateway-api" <<'PY'
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

host, domain, version, api = sys.argv[1:]
keys = json.loads(subprocess.check_output([api, 'keygen']))
values = {
    'PG_RELEASE_SHA': version,
    'POSTGRES_PASSWORD': secrets.token_hex(32),
    'GATEWAY_API_TOKEN': keys['api_token'],
    'GATEWAY_MASTER_KEY': keys['master_key'],
    'GATEWAY_REALITY_PRIVATE_KEY': keys['reality_private_key'],
    'GATEWAY_SHORT_ID': keys['short_id'],
    'BROWSER_WORKER_TOKEN': secrets.token_hex(32),
    'GATEWAY_HOST': host,
    'PANEL_DOMAIN': domain,
    'GATEWAY_SERVER_NAME': 'www.microsoft.com',
    'GATEWAY_REALITY_TARGET': 'www.microsoft.com:443',
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
compose pull postgres caddy
compose build api
compose build xray
compose build browser

api_token=$(sed -n 's/^GATEWAY_API_TOKEN=//p' "$env_file")
export CADDY_PASSWORD="$api_token"
browser_hash=$(docker run --rm --env CADDY_PASSWORD --entrypoint /bin/sh \
  caddy:2.10.2-alpine -c 'exec caddy hash-password --plaintext "$CADDY_PASSWORD"')
unset CADDY_PASSWORD
python3 - "$release/docker/Caddyfile.template" /etc/private-gateway-docker/Caddyfile "$domain" "$browser_hash" <<'PY'
from pathlib import Path
import re
import sys

source, destination = map(Path, sys.argv[1:3])
domain, hashed = sys.argv[3:5]
if not re.fullmatch(r'\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}', hashed):
    raise SystemExit('Caddy did not return a valid bcrypt hash.')
text = source.read_text().replace('__PANEL_DOMAIN__', domain).replace('__BROWSER_AUTH_HASH__', hashed)
destination.write_text(text)
destination.chmod(0o600)
PY
unset api_token browser_hash

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
compose up --detach --wait xray caddy
compose exec -T api /usr/local/bin/gatewayctl config apply

ready=false
for _ in $(seq 1 60); do
  if curl --noproxy '*' --fail --silent --show-error --max-time 4 \
      --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
[[ $ready == true ]] || fail 'Public HTTPS did not become ready. Check DNS, TCP 80/8443 and Caddy logs.'
compose exec -T api /usr/local/bin/gatewayctl browser start
compose exec -T browser python3 /app/smoke.py --port 6080
compose exec -T api /usr/local/bin/gatewayctl browser stop
compose exec -T api /usr/local/bin/gatewayctl status

if [[ $replace_legacy == true ]]; then
  systemctl disable gateway-api.service private-gateway-xray.service caddy.service postgresql.service
  systemctl stop postgresql.service
fi
ln -sfn "$release" /opt/private-gateway-docker/current
printf '%s\n' "$version" > /etc/private-gateway-docker/installed.version
success=true
printf '\nDocker installation verified. Panel: https://%s:8443/\nGateway: %s:443\n' "$domain" "$host"
printf 'The new admin token is in %s; read it locally and never send it to chat.\n' "$env_file"
if [[ -n $backup ]]; then
  printf 'Legacy files and users remain for rollback until the client connection is tested. Backup: %s\n' "$backup"
fi
