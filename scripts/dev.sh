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
    load_env
    export TEST_DATABASE_URL="$GATEWAY_DATABASE_URL" PRIVATE_GATEWAY_REAL_XRAY_BINARY="$GATEWAY_XRAY_BINARY"
    go test -race ./...
    go vet ./...
    (cd web && npm test && npm run build)
    ;;
  stop)
    load_env
    stop_api
    bash scripts/xray-service.sh stop
    docker compose --env-file .env -f compose.dev.yml stop postgres
    ;;
  *) echo 'usage: scripts/dev.sh prepare|start|test|stop' >&2; exit 1 ;;
esac
