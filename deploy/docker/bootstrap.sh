#!/usr/bin/env bash
# Разовая подготовка VPS из небольшого артефакта первой установки Actions.
set -Eeuo pipefail
umask 077

usage() {
  printf '%s\n' 'Usage: sudo bash bootstrap.sh --ref FULL_COMMIT_SHA --host SERVER_IPV4 --domain PANEL_DOMAIN --deploy-key PUBLIC_KEY'
}
fail() { printf 'Docker bootstrap stopped: %s\n' "$*" >&2; exit 1; }
ref='' host='' domain='' deploy_key=''
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --ref|--host|--domain|--deploy-key)
      (($# >= 2)) || fail "Missing value after $1"
      case "$1" in
        --ref) ref=$2 ;;
        --host) host=$2 ;;
        --domain) domain=$2 ;;
        --deploy-key) deploy_key=$2 ;;
      esac
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $EUID == 0 ]] || fail 'Run the bootstrap using sudo.'
[[ $ref =~ ^[0-9a-f]{40}$ ]] || fail 'Provide a full commit SHA with --ref.'
[[ -f $deploy_key ]] || fail 'The SSH public key file is missing.'
[[ ! -e /etc/private-gateway-docker/stack.env ]] || fail 'Docker is already installed.'
bundle=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
[[ -f $bundle/commit.sha && $(cat "$bundle/commit.sha") == "$ref" ]] \
  || fail 'The bootstrap bundle does not match --ref.'
for file in bootstrap-receiver.sh verify-release.py SHA256SUMS; do
  [[ -f $bundle/$file && ! -L $bundle/$file ]] || fail "Missing regular bundle file: $file"
done
(cd "$bundle" && sha256sum -c --status SHA256SUMS) \
  || fail 'Bootstrap bundle checksum verification failed.'
python3 - "$host" "$domain" "$deploy_key" <<'PY'
import ipaddress
from pathlib import Path
import re
import sys

host, domain, key_path = sys.argv[1:]
ipaddress.IPv4Address(host)
if len(domain) > 253 or '.' not in domain or not all(
    re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?', label)
    for label in domain.split('.')
):
    raise SystemExit('Invalid panel domain')
key = Path(key_path).read_text().strip()
if not re.fullmatch(r'ssh-ed25519 [A-Za-z0-9+/]+={0,2}(?: [^\r\n]*)?', key):
    raise SystemExit('Expected one ED25519 SSH public key')
PY

exec 9>/run/lock/private-gateway-docker-bootstrap-setup.lock
flock -n 9 || fail 'Another bootstrap setup is running.'
temporary=$(mktemp -d /var/tmp/private-gateway-bootstrap-setup.XXXXXXXX)
trap 'rm -rf -- "$temporary"' EXIT
cp -- "$bundle/bootstrap-receiver.sh" "$temporary/receiver.sh"
cp -- "$bundle/verify-release.py" "$temporary/verify-release.py"
bash -n "$temporary/receiver.sh"
python3 -m py_compile "$temporary/verify-release.py"

install -d -m 0700 /etc/private-gateway-docker /var/backups/private-gateway
install -d -m 0755 /usr/local/lib/private-gateway-docker
if ! id gateway-deploy >/dev/null 2>&1; then
  useradd --system --user-group --home-dir /var/lib/private-gateway-deploy \
    --no-create-home --shell /bin/sh --password '*' gateway-deploy
fi
install -d -o root -g root -m 0755 /var/lib/private-gateway-deploy /var/lib/private-gateway-deploy/.ssh
[[ ! -L /etc/sudoers.d/private-gateway-deploy && ! -L /var/lib/private-gateway-deploy/.ssh/authorized_keys ]] \
  || fail 'The existing deployment access contains an unexpected symlink.'

backup=$(mktemp -d /var/backups/private-gateway/bootstrap-access.XXXXXXXX)
sudoers=/etc/sudoers.d/private-gateway-deploy
keys=/var/lib/private-gateway-deploy/.ssh/authorized_keys
helper=/usr/local/sbin/private-gateway-docker-deploy
verifier=/usr/local/lib/private-gateway-docker/verify-release.py
for item in sudoers keys helper verifier; do
  case "$item" in
    sudoers) path=$sudoers ;;
    keys) path=$keys ;;
    helper) path=$helper ;;
    verifier) path=$verifier ;;
  esac
  if [[ -e $path ]]; then cp -p "$path" "$backup/$item"; fi
done
configured=false
rollback() {
  if [[ $configured == true ]]; then return; fi
  for item in sudoers keys helper verifier; do
    case "$item" in
      sudoers) path=$sudoers ;;
      keys) path=$keys ;;
      helper) path=$helper ;;
      verifier) path=$verifier ;;
    esac
    if [[ -f $backup/$item ]]; then cp -p "$backup/$item" "$path"; else rm -f -- "$path"; fi
  done
  rm -f -- /etc/private-gateway-docker/bootstrap.conf /etc/private-gateway-docker/deploy-key.pub
}
trap 'rollback; rm -rf -- "$temporary"' EXIT

install -m 0600 "$deploy_key" /etc/private-gateway-docker/deploy-key.pub
printf 'BOOTSTRAP_HOST=%s\nBOOTSTRAP_DOMAIN=%s\n' "$host" "$domain" \
  > /etc/private-gateway-docker/bootstrap.conf
chmod 0600 /etc/private-gateway-docker/bootstrap.conf
install -m 0644 "$temporary/verify-release.py" "$verifier"
install -m 0755 "$temporary/receiver.sh" "$helper"
sudoers_tmp=$(mktemp /etc/sudoers.d/.private-gateway-deploy.XXXXXX)
printf '%s\n' 'gateway-deploy ALL=(root) NOPASSWD: /usr/local/sbin/private-gateway-docker-deploy ""' > "$sudoers_tmp"
chmod 0440 "$sudoers_tmp"
visudo -cf "$sudoers_tmp"
mv -f "$sudoers_tmp" "$sudoers"
keys_tmp=$(mktemp /var/lib/private-gateway-deploy/.ssh/.authorized_keys.XXXXXX)
python3 - "$deploy_key" <<'PY' > "$keys_tmp"
from pathlib import Path
import sys

key = ' '.join(Path(sys.argv[1]).read_text().split()[:2])
print(f'command="sudo -n /usr/local/sbin/private-gateway-docker-deploy",restrict {key}')
PY
chmod 0644 "$keys_tmp"
mv -f "$keys_tmp" "$keys"
configured=true
printf 'VPS is ready to receive the first Docker release from GitHub Actions.\n'
printf 'Previous deployment access backup: %s\n' "$backup"
