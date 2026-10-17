#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
repo_dir="$PWD"
umask 077
mkdir -p .local
# Свой образ проверок и свой набор проектов compose у каждой копии, чтобы параллельные рабочие
# папки агентов не мешали друг другу.
checkout_name=$(basename "$repo_dir" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9\n' '-')
# Контекст сборки образа проверок: отслеживаемые файлы с текущими правками и новые неигнорируемые —
# то, что сохранит следующий коммит, — с переводами строк репозитория (LF), что бы ни говорил
# core.autocrlf. Временный индекс не трогает настоящий, а игнорируемые файлы вроде .env в Docker не
# попадают. Индекс лежит в git-каталоге этой копии: этот путь понимает Git для Windows, в отличие
# от /tmp в Git Bash.
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
# Локальный стек: production-файл compose из релиза, собранного из этой копии, и поверх него
# deploy/dev/compose.yml. Его секреты, релиз и данные лежат в .local/dev и в томах Docker проекта
# этой копии; порт 8080 одновременно обслуживает стек только одной копии.
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
# Выполняется в контейнере API: пароль приходит через stdin и сохраняется только как PBKDF2.
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
    # API с панелью, PostgreSQL и Xray worker в Docker, собранные из этой копии вместе с
    # незакоммиченными правками. Повторный start пересобирает их и сохраняет данные.
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
        # Их требует production-файл compose; deploy/dev/compose.yml заменяет их использование.
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
    # Администратор локального стека; 2FA подключается в браузере при первом входе, как в
    # production. Пароль попадает в контейнер только через stdin.
    require_dev_stack
    read -r -p 'Administrator login [admin]: ' login
    read -r -s -p 'Password, 14 to 128 characters: ' password
    echo
    read -r -s -p 'Repeat the password: ' repeat
    echo
    [[ $password == "$repeat" ]] || { echo 'The passwords differ.' >&2; exit 1; }
    printf '%s\n%s\n' "${login:-admin}" "$password" | dev_compose exec -T api python3 -c "$admin_python"
    # Новые учётные данные завершают сеансы прежних.
    dev_compose up --detach --no-deps --force-recreate --wait api
    echo 'Open http://127.0.0.1:8080/ and enroll the authenticator.'
    ;;
  ctl)
    # gatewayctl внутри контейнера API, где его токен работает через loopback.
    require_dev_stack
    shift
    # MSYS_NO_PATHCONV не даёт Git Bash на Windows переписать путь в контейнере.
    MSYS_NO_PATHCONV=1 dev_compose exec -T api /usr/local/bin/gatewayctl "$@"
    ;;
  smoke)
    # Создаёт, применяет, отзывает и снова применяет устройство через API с настоящим Xray worker.
    require_dev_stack
    dev_compose exec -T api python3 - < scripts/smoke-api.py
    ;;
  stop)
    # Останавливает локальный стек, сохраняя его базу и состояние Xray.
    require_dev_stack
    dev_compose stop
    ;;
  test)
    # Все проверки в Docker, чтобы ПК с Windows, хост с Linux и CI работали с одними инструментами.
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    project="private-gateway-checks-$checkout_name"
    # Стек одноразовый: --volumes удаляет только его собственную базу в tmpfs, никогда — локальную или production.
    trap "docker compose --project-name $project --file deploy/checks/compose.yml down --volumes --remove-orphans >/dev/null 2>&1" EXIT
    docker compose --project-name "$project" --file deploy/checks/compose.yml run --rm checks
    ;;
  sqlc)
    # sqlc 1.30.0 из образа проверок заново создаёт internal/storage/db; файлы возвращаются
    # потоком tar, поэтому папки хоста в контейнер не монтируются.
    build_checks_image
    docker run --rm --entrypoint bash "$CHECKS_IMAGE" -c 'sqlc generate >&2 && tar -c internal/storage/db' | tar -x
    echo 'Regenerated internal/storage/db with sqlc 1.30.0.'
    ;;
  release)
    # Релиз текущего коммита для Linux amd64, собранный теми же инструментами, что и проверки;
    # release.tar.gz, его контрольная сумма и установщик возвращаются потоком tar в OUTPUT_DIR.
    output=${2:?usage: scripts/dev.sh release OUTPUT_DIR}
    if [[ -n $(git status --porcelain) ]]; then
      echo 'Commit first: a release carries the commit SHA, so it is built from committed files only' >&2
      exit 1
    fi
    version=$(git rev-parse HEAD)
    build_checks_image
    docker volume create private-gateway-checks-go-build >/dev/null
    mkdir -p "$output"
    # MSYS_NO_PATHCONV не даёт Git Bash на Windows переписать путь контейнера в --mount.
    MSYS_NO_PATHCONV=1 docker run --rm --env GATEWAY_RELEASE_VERSION="$version" \
      --mount type=volume,src=private-gateway-checks-go-build,dst=/home/checks/.cache/go-build \
      --entrypoint bash "$CHECKS_IMAGE" \
      -c 'bash scripts/build-release.sh /tmp/release >&2 && tar -C /tmp/release -c .' | tar -x -C "$output"
    echo "Release $version is in $output."
    ;;
  *) echo 'usage: scripts/dev.sh test|sqlc|release DIR|start|admin|ctl ARGS|smoke|stop' >&2; exit 1 ;;
esac
