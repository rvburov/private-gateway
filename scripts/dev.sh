#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
repo_dir="$PWD"
toolchain_root="${GATEWAY_TOOLCHAIN_ROOT:-/workspace/.toolchains}"
export npm_config_cache="${npm_config_cache:-/workspace/.cache/npm}"
if test -x "$toolchain_root/go/bin/go"; then
  export PATH="$toolchain_root/go/bin:$toolchain_root/bin:$toolchain_root/xray:$PATH"
  export GOCACHE=/workspace/.cache/go-build GOPATH=/workspace/.cache/go GOMODCACHE=/workspace/.cache/go-mod
fi
umask 077
mkdir -p .local bin
load_env() {
  if ! test -f .env; then echo 'Run scripts/dev.sh prepare first' >&2; exit 1; fi
  set -a
  source .env
  set +a
}
stop_api() {
  if test -f .local/api.pid; then
    pid=$(cat .local/api.pid)
    if python3 scripts/owns-process.py "$pid" "$repo_dir/bin/gateway-api"; then
      kill "$pid"
      for attempt in $(seq 1 150); do
        if ! kill -0 "$pid" 2>/dev/null || [[ "$(ps -o stat= -p "$pid" 2>/dev/null || true)" == Z* ]]; then break; fi
        sleep 0.1
      done
      if kill -0 "$pid" 2>/dev/null && [[ "$(ps -o stat= -p "$pid" 2>/dev/null || true)" != Z* ]]; then echo 'API did not stop; PID retained' >&2; exit 1; fi
    fi
    rm -f .local/api.pid
  fi
}
# Build context for the checks image: tracked files with their current edits plus new files that
# are not ignored, which is what the next commit would store, with the repository's LF line endings
# whatever core.autocrlf says. A temporary index leaves the real one untouched, and ignored files
# such as .env never reach Docker. The index lives in this checkout's git directory, a path that
# Git for Windows understands, unlike the /tmp of Git Bash.
checks_context() (
  index="$(git rev-parse --absolute-git-dir)/checks-$BASHPID.index"
  trap 'rm -f "$index" "$index.lock"' EXIT
  GIT_INDEX_FILE="$index" git read-tree HEAD
  GIT_INDEX_FILE="$index" git -c core.safecrlf=false add --all
  tree=$(GIT_INDEX_FILE="$index" git write-tree)
  git -c core.autocrlf=false archive --format=tar "$tree"
)
build_checks_image() {
  if ! command -v docker >/dev/null; then
    echo 'Docker is required: Docker Desktop on Windows or macOS, Docker Engine on Linux' >&2
    exit 1
  fi
  # One image and compose project per checkout, so parallel agent worktrees do not collide.
  checks_name=$(basename "$repo_dir" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9\n' '-')
  export CHECKS_IMAGE="private-gateway-checks:$checks_name"
  checks_context | docker build --file deploy/checks/Dockerfile --tag "$CHECKS_IMAGE" -
}
case "${1:-}" in
  prepare)
    stop_api
    go build -o bin/gateway-api ./cmd/gateway-api
    go build -o bin/gatewayctl ./cmd/gatewayctl
    if ! test -e .env; then
      python3 - "$repo_dir" "$(command -v xray || true)" <<'PY'
import json,os,pathlib,secrets,shlex,subprocess,sys
repo=pathlib.Path(sys.argv[1])
keys=json.loads(subprocess.check_output([str(repo/'bin/gateway-api'),'keygen']))
password=secrets.token_hex(24)
values={
 'POSTGRES_PASSWORD':password,
 'GATEWAY_DATABASE_URL':f'postgres://gateway:{password}@127.0.0.1:5432/gateway?sslmode=disable',
 'GATEWAY_API_TOKEN':keys['api_token'],'GATEWAY_MASTER_KEY':keys['master_key'],
 'GATEWAY_REALITY_PRIVATE_KEY':keys['reality_private_key'],'GATEWAY_SHORT_ID':keys['short_id'],
 'GATEWAY_LISTEN':'127.0.0.1:8080','GATEWAY_HOST':'127.0.0.1','GATEWAY_PORT':'8443',
 'GATEWAY_SERVER_NAME':'www.cloudflare.com','GATEWAY_REALITY_TARGET':'www.cloudflare.com:443',
 'GATEWAY_XRAY_LISTEN':'127.0.0.1','GATEWAY_XRAY_CONFIG':str(repo/'.local/xray.json'),
 'GATEWAY_XRAY_BINARY':sys.argv[2] or 'xray',
 'GATEWAY_XRAY_RESTART':json.dumps(['bash',str(repo/'scripts/xray-service.sh'),'restart']),
 'GATEWAY_WEB_DIR':str(repo/'web/dist')}
with open(repo/'.env','x') as f:
 for k,v in values.items(): f.write(k+'='+shlex.quote(v)+'\n')
os.chmod(repo/'.env',0o600)
print('Created local .env with random development credentials (values hidden).')
PY
    fi
    load_env
    docker compose --env-file .env -f compose.dev.yml up -d --wait postgres
    bin/gateway-api migrate
    (cd web && npm ci && npm run build)
    ;;
  start)
    load_env
    docker compose --env-file .env -f compose.dev.yml up -d --wait postgres
    bin/gateway-api migrate
    if test -f .local/api.pid; then
      pid=$(cat .local/api.pid)
      if python3 scripts/owns-process.py "$pid" "$repo_dir/bin/gateway-api" && kill -0 "$pid" 2>/dev/null; then
        curl -fsS --noproxy '*' "http://$GATEWAY_LISTEN/readyz"
        exit 0
      fi
    fi
    nohup "$repo_dir/bin/gateway-api" serve >.local/api.log 2>&1 &
    pid=$!
    printf '%s\n' "$pid" >.local/api.pid
    for attempt in $(seq 1 100); do
      if curl -fsS --noproxy '*' "http://$GATEWAY_LISTEN/readyz" >/dev/null 2>&1; then
        if kill -0 "$pid" 2>/dev/null && rg -q 'gateway API listening' .local/api.log; then echo 'Gateway API and web panel are ready on loopback.'; exit 0; fi
      fi
      if ! kill -0 "$pid" 2>/dev/null; then echo 'API startup failed; inspect .local/api.log' >&2; exit 1; fi
      sleep 0.1
    done
    echo 'API readiness timed out; inspect .local/api.log' >&2
    exit 1
    ;;
  test)
    # Every check in Docker, so a Windows PC, a Linux host and CI run the same toolchain.
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    project="private-gateway-checks-$checks_name"
    # The stack is throwaway: --volumes only drops its own tmpfs database, never the dev or production one.
    trap "docker compose --project-name $project --file deploy/checks/compose.yml down --volumes --remove-orphans >/dev/null 2>&1" EXIT
    docker compose --project-name "$project" --file deploy/checks/compose.yml run --rm checks
    ;;
  sqlc)
    # sqlc 1.30.0 from the checks image regenerates internal/storage/db; the files come back as
    # a tar stream, so no host folder is mounted into the container.
    build_checks_image
    docker run --rm --entrypoint bash "$CHECKS_IMAGE" -c 'sqlc generate >&2 && tar -c internal/storage/db' | tar -x
    echo 'Regenerated internal/storage/db with sqlc 1.30.0.'
    ;;
  stop)
    load_env
    stop_api
    bash scripts/xray-service.sh stop
    docker compose --env-file .env -f compose.dev.yml stop postgres
    ;;
  *) echo 'usage: scripts/dev.sh prepare|start|test|sqlc|stop' >&2; exit 1 ;;
esac
