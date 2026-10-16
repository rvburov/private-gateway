#!/usr/bin/env bash
# Every pre-commit check, run inside the image from deploy/checks/Dockerfile with PostgreSQL 18
# next to it. Start it with `bash scripts/dev.sh test`; it is not meant to run on the host.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${TEST_DATABASE_URL:?TEST_DATABASE_URL is required}"
: "${PRIVATE_GATEWAY_REAL_XRAY_BINARY:?PRIVATE_GATEWAY_REAL_XRAY_BINARY is required}"

step() { printf '\n==> %s\n' "$*"; }

step 'sqlc 1.30.0: generated code matches queries and migrations'
sqlc diff

step 'Go: vet'
go vet ./...

step 'Go: tests with the race detector, PostgreSQL 18 and real Xray'
# -count=1: the build cache volume survives between runs, but cached test results would let the
# PostgreSQL and Xray integration tests pass without running.
go test -race -count=1 -json ./... | python3 scripts/go-test-report.py

step 'Go: build commands'
go build -o "$(mktemp -d)" ./cmd/...

step 'Web panel: tests and production build'
(cd web && npm test && npm run build)

step 'GitHub Actions workflows (actionlint 1.7.12)'
actionlint .github/workflows/*.yml

step 'Release, deployment and server list scripts'
for script in scripts/*.sh deploy/docker/*.sh deploy/browser/*.sh; do
  bash -n "$script"
done
python3 -m py_compile scripts/*.py deploy/docker/*.py deploy/browser/*.py
for test in scripts/test-release.py scripts/test-vpn-smoke.py scripts/test-change-reality-target.py \
  scripts/test-rotate-admin-token.py scripts/test-admin-credentials.py scripts/test-servers.py; do
  python3 "$test"
done

printf '\nAll checks passed.\n'
printf 'Not run here: production image builds, Caddy and HAProxy routing (test-browser-proxy.py,\n'
printf 'test-edge-router.py, test-first-deploy.sh); GitHub Actions runs them for master.\n'
