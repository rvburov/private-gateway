#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
repo_dir="$PWD"
umask 077
mkdir -p .local
# One checks image and one set of compose projects per checkout, so parallel agent worktrees do
# not collide.
checkout_name=$(basename "$repo_dir" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9\n' '-')
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
  export CHECKS_IMAGE="private-gateway-checks:$checkout_name"
  checks_context | docker build --file deploy/checks/Dockerfile --tag "$CHECKS_IMAGE" -
}
# The local stack: the production compose file of a release built from this checkout, with
# deploy/dev/compose.yml on top. Its secrets, release and data stay in .local/dev and in Docker
# volumes of this checkout's project; port 8080 serves one checkout's stack at a time.
dev=.local/dev
dev_compose() {
  docker compose --project-name "private-gateway-dev-$checkout_name" --env-file "$dev/stack.env" \
    --file "$dev/release/docker/compose.yml" --file deploy/dev/compose.yml "$@"
}
require_dev_stack() {
  if [[ ! -f $dev/stack.env || ! -f $dev/release/docker/compose.yml ]]; then
    echo 'Start the local stack first: bash scripts/dev.sh start' >&2
    exit 1
  fi
}
random_hex() { od -An -vtx1 -N"$1" /dev/urandom | tr -d ' \n'; }
random_base64() { head -c "$1" /dev/urandom | base64 | tr -d '\n'; }
# Runs in the API container: the password arrives on stdin and is stored as PBKDF2 only.
admin_python=$(cat <<'PY'
import hashlib, json, os, subprocess, sys
login, password = sys.stdin.read().split("\n")[:2]
if not 14 <= len(password) <= 128:
    raise SystemExit("The password must have 14 to 128 characters.")
salt = os.urandom(16)
digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600000)
request = {"username": login, "password_hash": f"pbkdf2_sha256:600000:{salt.hex()}:{digest.hex()}"}
subprocess.run(["/usr/local/bin/gateway-api", "admin-set"], input=json.dumps(request).encode(), check=True)
PY
)
case "${1:-}" in
  start)
    # The API with the panel, PostgreSQL and the Xray worker in Docker, built from this checkout
    # with its uncommitted edits. A repeated start rebuilds them and keeps the data.
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    mkdir -p "$dev"
    rm -rf "$dev/release.next"
    mkdir "$dev/release.next"
    MSYS_NO_PATHCONV=1 docker run --rm --env GATEWAY_RELEASE_VERSION="$(printf '0%.0s' {1..40})" \
      --mount type=volume,src=private-gateway-checks-go-build,dst=/home/checks/.cache/go-build \
      --entrypoint bash "$CHECKS_IMAGE" -c 'bash scripts/build-release.sh /tmp/release >&2 &&
        mkdir /tmp/payload && tar -xzf /tmp/release/release.tar.gz -C /tmp/payload && tar -C /tmp/payload -c .' \
      | tar -x -C "$dev/release.next"
    rm -rf "$dev/release"
    mv "$dev/release.next" "$dev/release"
    if [[ ! -f $dev/stack.env ]]; then
      {
        printf 'PG_RELEASE_SHA=%s\n' "$(printf '0%.0s' {1..40})"
        printf 'PG_API_IMAGE=dev\nPG_XRAY_IMAGE=dev\nPG_BROWSER_IMAGE=dev\n'
        printf 'POSTGRES_PASSWORD=%s\n' "$(random_hex 32)"
        printf 'GATEWAY_API_TOKEN=%s\n' "$(random_hex 32)"
        printf 'GATEWAY_MASTER_KEY=%s\n' "$(random_base64 32)"
        printf 'GATEWAY_REALITY_PRIVATE_KEY=%s\n' "$(random_base64 32 | tr '+/' '-_' | tr -d '=')"
        printf 'GATEWAY_SHORT_ID=%s\n' "$(random_hex 8)"
        printf 'BROWSER_WORKER_TOKEN=%s\n' "$(random_hex 32)"
        printf 'GATEWAY_HOST=127.0.0.1\nPANEL_DOMAIN=localhost\n'
        # The production compose file requires them; deploy/dev/compose.yml replaces their use.
        printf 'GATEWAY_STATE_DIR=/unused\nGATEWAY_CADDYFILE=/unused\n'
      } > "$dev/stack.env"
      echo 'Created .local/dev/stack.env with random local secrets (values hidden).'
    fi
    dev_compose build api xray
    dev_compose run --rm --no-deps state
    dev_compose up --detach --wait postgres
    dev_compose run --rm --no-deps api migrate
    dev_compose up --detach --wait api xray
    curl --fail --silent --show-error --noproxy '*' --max-time 5 --output /dev/null http://127.0.0.1:8080/readyz
    echo 'The panel is on http://127.0.0.1:8080/. Set its login and password: bash scripts/dev.sh admin'
    ;;
  admin)
    # The administrator of the local stack; 2FA is enrolled in the browser at the first login,
    # as in production. The password reaches the container on stdin only.
    require_dev_stack
    read -r -p 'Administrator login [admin]: ' login
    read -r -s -p 'Password, 14 to 128 characters: ' password
    echo
    read -r -s -p 'Repeat the password: ' repeat
    echo
    [[ $password == "$repeat" ]] || { echo 'The passwords differ.' >&2; exit 1; }
    printf '%s\n%s\n' "${login:-admin}" "$password" | dev_compose exec -T api python3 -c "$admin_python"
    # New credentials end the sessions of the previous ones.
    dev_compose up --detach --no-deps --force-recreate --wait api
    echo 'Open http://127.0.0.1:8080/ and enroll the authenticator.'
    ;;
  ctl)
    # gatewayctl inside the API container, where its token works over loopback.
    require_dev_stack
    shift
    # MSYS_NO_PATHCONV keeps Git Bash on Windows from rewriting the container path.
    MSYS_NO_PATHCONV=1 dev_compose exec -T api /usr/local/bin/gatewayctl "$@"
    ;;
  smoke)
    # Creates, applies, revokes and applies a device through the API with the real Xray worker.
    require_dev_stack
    dev_compose exec -T api python3 - < scripts/smoke-api.py
    ;;
  stop)
    # Stops the local stack and keeps its database and Xray state.
    require_dev_stack
    dev_compose stop
    ;;
  test)
    # Every check in Docker, so a Windows PC, a Linux host and CI run the same toolchain.
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    project="private-gateway-checks-$checkout_name"
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
  release)
    # The Linux amd64 release of the current commit, built with the same toolchain as the checks;
    # release.tar.gz, its checksum and the installer come back as a tar stream into OUTPUT_DIR.
    output=${2:?usage: scripts/dev.sh release OUTPUT_DIR}
    if [[ -n $(git status --porcelain) ]]; then
      echo 'Commit first: a release carries the commit SHA, so it is built from committed files only' >&2
      exit 1
    fi
    version=$(git rev-parse HEAD)
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    mkdir -p "$output"
    # MSYS_NO_PATHCONV keeps Git Bash on Windows from rewriting the container path in --mount.
    MSYS_NO_PATHCONV=1 docker run --rm --env GATEWAY_RELEASE_VERSION="$version" \
      --mount type=volume,src=private-gateway-checks-go-build,dst=/home/checks/.cache/go-build \
      --entrypoint bash "$CHECKS_IMAGE" \
      -c 'bash scripts/build-release.sh /tmp/release >&2 && tar -C /tmp/release -c .' | tar -x -C "$output"
    echo "Release $version is in $output."
    ;;
  *) echo 'usage: scripts/dev.sh test|sqlc|release DIR|start|admin|ctl ARGS|smoke|stop' >&2; exit 1 ;;
esac
