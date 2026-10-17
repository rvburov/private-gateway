#!/usr/bin/env bash
# Прогнать поток SSH первой выкладки и подлинность архива, прежде чем трогать VPS.
set -euo pipefail

archive=${1:?Pass the release.tar.gz path}
image=${2:?Pass the built API image}
repository=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
version=$(python3 "$repository/scripts/verify-release.py" --tar "$archive")
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
printf 'BOOTSTRAP_HOST=127.0.0.1\nBOOTSTRAP_DOMAIN=gateway.example.test\n' > "$temporary/bootstrap.conf"
printf '%s\n' 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFw93RU6Xzb2UJEPnTVXhyuvAw4MYQZuEooLREfOZIv0 test' \
  > "$temporary/deploy-key.pub"

receive() {
  { printf '%s\n' "$1"; cat "$archive"; } | docker run --rm -i --user 0 --network none \
    --add-host gateway.example.test:127.0.0.1 \
    --mount "type=bind,src=$repository/deploy/docker/bootstrap-receiver.sh,dst=/receiver.sh,readonly" \
    --mount "type=bind,src=$repository/scripts/verify-release.py,dst=/usr/local/lib/private-gateway-docker/verify-release.py,readonly" \
    --mount "type=bind,src=$temporary/bootstrap.conf,dst=/etc/private-gateway-docker/bootstrap.conf,readonly" \
    --mount "type=bind,src=$temporary/deploy-key.pub,dst=/etc/private-gateway-docker/deploy-key.pub,readonly" \
    --entrypoint /bin/bash "$image" /receiver.sh
}

# Полный проверенный релиз должен дойти до предварительных проверок установщика. Тестовый
# образ — Debian, поэтому установщик только для Ubuntu останавливается, ничего не изменив.
if output=$(receive "$version" 2>&1); then
  printf 'First-deploy preflight unexpectedly succeeded.\n' >&2
  exit 1
fi
[[ $output == *'This installer supports Ubuntu 26.04 amd64.'* ]] || {
  printf 'First-deploy handoff failed: %s\n' "$output" >&2
  exit 1
}

if output=$(receive "$(printf '0%.0s' {1..40})" 2>&1); then
  printf 'A mismatched release SHA was accepted.\n' >&2
  exit 1
fi
[[ $output == *'Release SHA does not match the successful CI run.'* ]] || {
  printf 'Wrong SHA failed for an unexpected reason: %s\n' "$output" >&2
  exit 1
}
printf 'First-deploy release transport and SHA checks passed.\n'
