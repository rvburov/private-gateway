#!/usr/bin/env bash
# Forced SSH command for verified Docker releases. No arguments or shell access.
set -Eeuo pipefail
umask 077

fail() { printf 'Docker deployment stopped: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 ]] || fail 'The updater must run as root.'
[[ $# == 0 ]] || fail 'Arguments are not accepted.'
IFS= read -r expected || fail 'Expected a commit SHA before the release archive.'
[[ $expected =~ ^[0-9a-f]{40}$ ]] || fail 'Expected a full commit SHA before the release archive.'
env_file=/etc/private-gateway-docker/stack.env
current=/opt/private-gateway-docker/current
[[ -f $env_file && -L $current ]] || fail 'Install the Docker stack before enabling automatic deployment.'
[[ -f /usr/local/lib/private-gateway-docker/verify-release.py ]] || fail 'The installed release verifier is missing.'
exec 9>/run/lock/private-gateway-docker-install.lock
flock -n 9 || fail 'Another Docker installation or deployment is running.'

temporary=$(mktemp -d /var/tmp/private-gateway-deploy.XXXXXXXX)
archive=$temporary/release.tar.gz
cleanup() {
  if [[ ${success:-false} != true && -n ${release:-} && -d $release \
        && $(readlink -f "$current" 2>/dev/null || true) != "$release" ]]; then
    rm -rf -- "$release"
  fi
  rm -rf -- "$temporary"
}
trap cleanup EXIT
head -c 268435457 > "$archive"
[[ $(stat -c %s "$archive") -le 268435456 ]] || fail 'Release archive exceeds 256 MiB.'
version=$(python3 /usr/local/lib/private-gateway-docker/verify-release.py --tar "$archive")
[[ $version == "$expected" ]] || fail 'Release SHA does not match the successful CI run.'
old_version=$(cat /etc/private-gateway-docker/installed.version)
[[ $old_version =~ ^[0-9a-f]{40}$ ]] || fail 'Installed version is invalid.'
[[ $(readlink -f "$current") == "/opt/private-gateway-docker/releases/$old_version" ]] \
  || fail 'Installed release pointer and version disagree.'
if [[ $version == "$old_version" ]]; then
  printf 'Release %s is already installed.\n' "$version"
  exit 0
fi
release="/opt/private-gateway-docker/releases/$version"
[[ ! -e $release ]] || fail 'Release directory already exists; inspect the previous deployment attempt.'
old_release=$(readlink -f "$current")
old_compose=$old_release/docker/compose.yml
new_compose=$release/docker/compose.yml
install -d -m 0700 /var/backups/private-gateway
backup=$(mktemp -d "/var/backups/private-gateway/docker-$(date -u +%Y%m%dT%H%M%SZ)-$version-XXXXXXXX")
cp -p "$env_file" "$backup/stack.env"
if [[ -f /var/lib/private-gateway-docker/xray/xray.json ]]; then
  cp -p /var/lib/private-gateway-docker/xray/xray.json "$backup/xray.json"
fi
install -d -m 0755 "$release"
tar -xzf "$archive" -C "$release" --no-same-owner --no-same-permissions
chmod 0555 "$release"/bin/gateway-api "$release"/bin/gatewayctl "$release"/bin/xray
compose_old() { docker compose --env-file "$env_file" -f "$old_compose" "$@"; }
compose_new() { PG_RELEASE_SHA="$version" docker compose --env-file "$env_file" -f "$new_compose" "$@"; }

# Image builds happen before the brief API interruption.
compose_new config --quiet
compose_new pull postgres caddy
compose_new build api xray browser
compose_old stop api
database_saved=false
api_stopped=true
env_changed=false
pointer_changed=false
success=false
rollback() {
  if [[ $success == true || $api_stopped != true ]]; then return; fi
  printf 'Deployment failed. Restoring the previous database and release. Backup: %s\n' "$backup" >&2
  if [[ $env_changed == true ]]; then cp -p "$backup/stack.env" "$env_file"; fi
  compose_new stop api || true
  if [[ $pointer_changed == true ]]; then
    ln -s "$old_release" "$current.rollback"
    mv -Tf "$current.rollback" "$current"
    printf '%s\n' "$old_version" > /etc/private-gateway-docker/installed.version
  fi
  if [[ -f $backup/xray.json ]]; then
    cp -p "$backup/xray.json" /var/lib/private-gateway-docker/xray/xray.json
  fi
  if [[ $database_saved == true ]]; then
    compose_old up -d --wait postgres || true
    compose_old exec -T postgres psql -v ON_ERROR_STOP=1 -U gateway -d postgres \
      -c 'DROP DATABASE IF EXISTS gateway WITH (FORCE)' || true
    compose_old exec -T postgres psql -v ON_ERROR_STOP=1 -U gateway -d postgres \
      -c 'CREATE DATABASE gateway OWNER gateway' || true
    compose_old exec -T postgres pg_restore -e -U gateway -d gateway < "$backup/gateway.dump" || true
  fi
  compose_old up -d --wait api browser xray caddy || true
  printf 'Check Docker health and the backup before another deployment.\n' >&2
}
trap 'rollback; cleanup' EXIT
compose_old exec -T postgres pg_dump -Fc -U gateway gateway > "$backup/gateway.dump"
[[ -s $backup/gateway.dump ]] || fail 'Database backup is empty.'
database_saved=true

compose_new run --rm --no-deps api migrate
python3 - "$env_file" "$version" <<'PY'
import os
from pathlib import Path
import sys
import tempfile

path = Path(sys.argv[1])
version = sys.argv[2]
lines = path.read_text().splitlines()
matches = [i for i, line in enumerate(lines) if line.startswith('PG_RELEASE_SHA=')]
if len(matches) != 1:
    raise SystemExit('Expected exactly one PG_RELEASE_SHA in stack.env')
lines[matches[0]] = f'PG_RELEASE_SHA={version}'
with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, prefix='.stack.', delete=False) as output:
    output.write('\n'.join(lines) + '\n')
    staged = output.name
os.chmod(staged, 0o600)
os.replace(staged, path)
PY
env_changed=true
compose_new up -d --wait postgres api browser xray caddy
compose_new exec -T api /usr/local/bin/gatewayctl config apply
domain=$(sed -n 's/^PANEL_DOMAIN=//p' "$env_file")
[[ $domain =~ ^[A-Za-z0-9.-]+$ ]] || fail 'Panel domain is invalid.'
ready=false
for _ in $(seq 1 30); do
  if curl --noproxy '*' --fail --silent --show-error --max-time 4 \
      --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
if [[ $ready != true ]]; then
  printf 'HTTPS readiness failed; final TLS request and Caddy logs follow.\n' >&2
  curl --noproxy '*' --verbose --output /dev/null --max-time 10 \
    --write-out 'HTTP %{http_code}; TLS verification %{ssl_verify_result}\n' \
    --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz" \
    2>&1 | tail -n 45 >&2 || true
  compose_new logs --no-color --tail=80 caddy >&2 || true
  fail 'Panel HTTPS readiness check failed.'
fi
compose_new exec -T api /usr/local/bin/gatewayctl status
ln -s "$release" "$current.next"
mv -Tf "$current.next" "$current"
pointer_changed=true
printf '%s\n' "$version" > /etc/private-gateway-docker/installed.version
install -m 0755 "$release/docker/update.sh" /usr/local/sbin/.private-gateway-docker-deploy.next
mv -f /usr/local/sbin/.private-gateway-docker-deploy.next /usr/local/sbin/private-gateway-docker-deploy
install -m 0644 "$release/docker/verify-release.py" /usr/local/lib/private-gateway-docker/.verify-release.next
mv -f /usr/local/lib/private-gateway-docker/.verify-release.next /usr/local/lib/private-gateway-docker/verify-release.py
success=true
printf 'Docker deployment verified: %s. Backup: %s\n' "$version" "$backup"
