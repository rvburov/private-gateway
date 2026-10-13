#!/usr/bin/env bash
# Restricted SSH receiver used only until the first Docker installation succeeds.
set -Eeuo pipefail
umask 077

fail() { printf 'First Docker deployment stopped: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 && $# == 0 ]] || fail 'The receiver only accepts a release on stdin.'
config=/etc/private-gateway-docker/bootstrap.conf
verifier=/usr/local/lib/private-gateway-docker/verify-release.py
[[ -f $config && -f $verifier ]] || fail 'Run the one-time server bootstrap first.'
[[ ! -e /etc/private-gateway-docker/stack.env ]] || fail 'Docker is already installed; inspect the deployment account.'
IFS= read -r expected || fail 'Expected a commit SHA before the release archive.'
[[ $expected =~ ^[0-9a-f]{40}$ ]] || fail 'Expected a full commit SHA.'
exec 8>/run/lock/private-gateway-docker-bootstrap.lock
flock -n 8 || fail 'Another first deployment is running.'

temporary=$(mktemp -d /var/tmp/private-gateway-bootstrap.XXXXXXXX)
trap 'rm -rf -- "$temporary"' EXIT
archive=$temporary/release.tar.gz
head -c 268435457 > "$archive"
[[ $(stat -c %s "$archive") -le 268435456 ]] || fail 'Release archive exceeds 256 MiB.'
version=$(python3 "$verifier" --tar "$archive")
[[ $version == "$expected" ]] || fail 'Release SHA does not match the successful CI run.'

install -d -m 0700 "$temporary/install"
tar -xOf "$archive" docker/install.sh > "$temporary/install/install.sh"
tar -xOf "$archive" docker/verify-release.py > "$temporary/install/verify-release.py"
chmod 0600 "$temporary/install/install.sh" "$temporary/install/verify-release.py"
(cd "$temporary" && sha256sum release.tar.gz > release.tar.gz.sha256)

# Values in this root-owned file were validated by bootstrap.sh.
# shellcheck disable=SC1090
source "$config"
args=(--host "$BOOTSTRAP_HOST" --domain "$BOOTSTRAP_DOMAIN"
      --archive "$archive" --deploy-key /etc/private-gateway-docker/deploy-key.pub)
if [[ -f /etc/private-gateway/gateway.env ]]; then args+=(--replace-legacy); fi
bash "$temporary/install/install.sh" "${args[@]}"
rm -f -- "$config"
printf 'First Docker deployment verified: %s\n' "$version"
