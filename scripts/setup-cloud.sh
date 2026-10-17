#!/usr/bin/env bash
set -euo pipefail
toolchain_root="${GATEWAY_TOOLCHAIN_ROOT:-/workspace/.toolchains}"
case "$(uname -m)" in
  x86_64) arch=amd64; go_digest=42d158b4d8f7b61ac0a830567c940a86098fb7aac52e467a5ebec03ef5cc2f8d; sqlc_digest=468aecee071bfe55e97fcbcac52ea0208eeca444f67736f3b8f0f3d6a106132e ;;
  aarch64) arch=arm64; go_digest=4a97373d49fcacdcf3694fea368a500b00ee3e963974f3e7514132717632f052; sqlc_digest=dd9ab43b022ba3b3402054f99d7ae6e5efea33c949e869c3c66b214415e0c82d ;;
  *) echo 'Unsupported architecture' >&2; exit 1 ;;
esac
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$toolchain_root/bin"
if ! test -x "$toolchain_root/go/bin/go" || ! "$toolchain_root/go/bin/go" version | rg -q 'go1.26.9 '; then
  curl -fsSL --proto '=https' --tlsv1.2 "https://go.dev/dl/go1.26.9.linux-${arch}.tar.gz" -o "$temporary/go.tar.gz"
  printf '%s  %s\n' "$go_digest" "$temporary/go.tar.gz" | sha256sum -c -
  tar -xzf "$temporary/go.tar.gz" -C "$temporary"
  if test -e "$toolchain_root/go"; then mv "$toolchain_root/go" "$temporary/previous-go"; fi
  mv "$temporary/go" "$toolchain_root/go"
fi
if ! test -x "$toolchain_root/bin/sqlc" || ! "$toolchain_root/bin/sqlc" version | rg -q 'v1.30.0'; then
  curl -fsSL --proto '=https' --tlsv1.2 "https://github.com/sqlc-dev/sqlc/releases/download/v1.30.0/sqlc_1.30.0_linux_${arch}.tar.gz" -o "$temporary/sqlc.tar.gz"
  printf '%s  %s\n' "$sqlc_digest" "$temporary/sqlc.tar.gz" | sha256sum -c -
  tar -xzf "$temporary/sqlc.tar.gz" -C "$toolchain_root/bin" sqlc
fi
if ! test -x "$toolchain_root/xray/xray"; then
  bash "$(dirname "$0")/install-xray.sh" "$toolchain_root/xray"
fi
export PATH="$toolchain_root/go/bin:$toolchain_root/bin:$toolchain_root/xray:$PATH"
export GOCACHE=/workspace/.cache/go-build GOPATH=/workspace/.cache/go GOMODCACHE=/workspace/.cache/go-mod
mkdir -p "$GOCACHE" "$GOPATH" "$GOMODCACHE"
cd "$(dirname "$0")/.."
# This large module's proxy redirect is blocked in some cloud policies. Native
# Git uses the official source, and Go still checks the public checksum database.
GOPROXY=direct go mod download github.com/klauspost/compress@v1.20.1
go mod download
(cd web && npm ci)
echo 'Go, sqlc, Xray and the module and npm caches are ready. Checks and the local stack run in Docker: bash scripts/dev.sh test, bash scripts/dev.sh start.'
