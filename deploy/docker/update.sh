#!/usr/bin/env bash
# Принудительная команда SSH для проверенных Docker-релизов. Без аргументов и доступа к оболочке.
set -Eeuo pipefail
umask 077

fail() { printf 'Docker deployment stopped: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 ]] || fail 'The updater must run as root.'
[[ $# == 0 ]] || fail 'Arguments are not accepted.'
IFS= read -r expected || fail 'Expected a commit SHA before the release archive.'
[[ $expected =~ ^[0-9a-f]{40}$ ]] || fail 'Expected a full commit SHA before the release archive.'
env_file=/etc/private-gateway-docker/stack.env
caddyfile=/etc/private-gateway-docker/Caddyfile
haproxy_cfg=/etc/private-gateway-docker/haproxy.cfg
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
  rm -f -- "$caddyfile.next" "$haproxy_cfg.next"
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
cp -p "$caddyfile" "$backup/Caddyfile"
if [[ -f $haproxy_cfg ]]; then
  cp -p "$haproxy_cfg" "$backup/haproxy.cfg"
fi
if [[ -f /var/lib/private-gateway-docker/xray/xray.json ]]; then
  cp -p /var/lib/private-gateway-docker/xray/xray.json "$backup/xray.json"
fi
install -d -m 0755 "$release"
tar -xzf "$archive" -C "$release" --no-same-owner --no-same-permissions
chmod 0555 "$release"/bin/gateway-api "$release"/bin/gatewayctl "$release"/bin/xray
# Теги по содержимому: образ сохраняет свой тег, а его контейнер продолжает работать, пока не
# изменятся его файлы. Релиз без помощника тегов собирает теги коммита, как велит его compose.
image_tags=()
if [[ -f $release/docker/image-tags.py ]]; then
  tag_lines=$(python3 "$release/docker/image-tags.py" "$release")
  mapfile -t image_tags <<< "$tag_lines"
  [[ ${#image_tags[@]} == 3 ]] || fail 'The release image tags are invalid.'
  for tag in "${image_tags[@]}"; do
    [[ $tag =~ ^PG_(API|XRAY|BROWSER)_IMAGE=[0-9a-f]{32}$ ]] || fail 'The release image tags are invalid.'
  done
fi
compose_old() { docker compose --env-file "$env_file" -f "$old_compose" "$@"; }
compose_new() {
  env PG_RELEASE_SHA="$version" "${image_tags[@]}" docker compose --env-file "$env_file" -f "$new_compose" "$@"
}
# Проверки конфигурации идут в тех же закреплённых образах, что и стек.
image_of() {
  compose_new config --format json \
    | python3 -c 'import json, sys; print(json.load(sys.stdin)["services"][sys.argv[1]]["image"])' "$1"
}
# Заполнить шаблон для домена панели рядом с рабочим файлом; рабочий файл не трогается.
stage_config() {
python3 - "$1" "$2" "$3" "$domain" <<'PY'
import os
from pathlib import Path
import sys

source, staged = map(Path, sys.argv[1:3])
content = source.read_text().replace('__PANEL_DOMAIN__', sys.argv[4])
if '__' in content:
    raise SystemExit(f'{source.name} contains an unresolved placeholder.')
staged.unlink(missing_ok=True)
with os.fdopen(os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), 'w') as output:
    output.write(content)
os.chmod(staged, int(sys.argv[3], 8))
PY
}

# Сборка образов и проверки конфигурации идут до короткой остановки API.
compose_new config --quiet
edge_enabled=false
old_edge=false
panel_port=8443
services=(postgres api browser xray caddy)
old_services=(api browser xray caddy)
# Списки читаются целиком: grep -q выходит на первом совпадении, и pipefail превратил бы SIGPIPE пишущего в «нет».
old_service_list=$(compose_old config --services)
new_service_list=$(compose_new config --services)
# При переносе опубликованного порта между службами сначала освобождается старый владелец.
# Та же проверка нужна при откате: работающий новый edge иначе удержит 8443 старого Caddy.
port_owner() {
  python3 -c 'import json, sys
services = json.load(sys.stdin)["services"]
print(",".join(sorted(name for name, service in services.items()
    if any(str(port.get("published")) == "8443" for port in service.get("ports", [])))))'
}
old_port_owner=$(compose_old config --format json | port_owner)
new_port_owner=$(compose_new config --format json | port_owner)
port_transfer=false
if [[ $old_port_owner != "$new_port_owner" ]]; then port_transfer=true; fi
fallback_port=$(compose_new config --format json | python3 -c \
  'import json, sys; print(json.load(sys.stdin)["services"]["api"].get("environment", {}).get("GATEWAY_FALLBACK_PORT", "0"))')
old_fallback_port=$(compose_old config --format json | python3 -c \
  'import json, sys; print(json.load(sys.stdin)["services"]["api"].get("environment", {}).get("GATEWAY_FALLBACK_PORT", "0"))')
[[ $fallback_port == 0 || $fallback_port == 8443 ]] || fail 'Unsupported fallback port in Docker deployment.'
[[ $old_fallback_port == 0 || $old_fallback_port == 8443 ]] || fail 'Unsupported fallback port in the installed release.'
if grep -qx edge <<< "$old_service_list"; then
  old_edge=true
  old_services+=(edge)
fi
if grep -qx edge <<< "$new_service_list"; then
  edge_enabled=true
  panel_port=443
  services+=(edge)
  [[ -f $release/docker/haproxy.cfg.template ]] || fail 'HAProxy template is missing.'
  compose_new pull postgres caddy edge
else
  compose_new pull postgres caddy
fi
# Пересобранный тег получил бы новый ID образа, и compose пересоздал бы контейнер, который не менялся.
for service in api xray browser; do
  if ! docker image inspect "$(image_of "$service")" >/dev/null 2>&1; then compose_new build "$service"; fi
done
domain=$(sed -n 's/^PANEL_DOMAIN=//p' "$env_file")
[[ $domain =~ ^[A-Za-z0-9.-]+$ ]] || fail 'Panel domain is invalid.'
# Неизменный файл конфигурации остаётся как есть, а с ним и контейнер, который его читает.
stage_config "$release/docker/Caddyfile.template" "$caddyfile.next" 600
if cmp -s "$caddyfile.next" "$caddyfile"; then
  rm -f -- "$caddyfile.next"
else
  # Для имени под собственным центром сертификации Caddy (.localhost, .internal) проверка создаёт этот центр в /data.
  docker run --rm --network none --read-only --tmpfs /data --cap-drop=ALL --cap-add=NET_BIND_SERVICE \
    -v "$caddyfile.next:/etc/caddy/Caddyfile:ro" \
    --entrypoint caddy "$(image_of caddy)" validate --config /etc/caddy/Caddyfile
fi
if [[ $edge_enabled == true ]]; then
  stage_config "$release/docker/haproxy.cfg.template" "$haproxy_cfg.next" 644
  if cmp -s "$haproxy_cfg.next" "$haproxy_cfg"; then
    rm -f -- "$haproxy_cfg.next"
  else
    docker run --rm --network none --read-only --cap-drop=ALL \
      -v "$haproxy_cfg.next:/usr/local/etc/haproxy/haproxy.cfg:ro" \
      --entrypoint haproxy "$(image_of edge)" -c -f /usr/local/etc/haproxy/haproxy.cfg
  fi
fi
compose_old stop api
database_saved=false
api_stopped=true
env_changed=false
caddy_changed=false
haproxy_changed=false
stack_started=false
pointer_changed=false
vless_failed=false
success=false
# Настоящий VLESS-трафик через основной или резервный порт с профилем первого активного устройства — его проверяет
# vpn-smoke.py указанного релиза. Пересозданный Xray получает новый адрес, и HAProxy заново
# находит его за секунды.
vless_works() {
  local attempt
  local port_args=()
  if [[ -n ${2:-} ]]; then port_args=(--port "$2"); fi
  for attempt in 1 2 3; do
    if python3 "$1/docker/vpn-smoke.py" "${port_args[@]}"; then return 0; fi
    if ((attempt < 3)); then sleep 5; fi
  done
  return 1
}
rollback() {
  if [[ $success == true || $api_stopped != true ]]; then return; fi
  printf 'Deployment failed. Restoring the previous database and release. Backup: %s\n' "$backup" >&2
  if [[ $env_changed == true ]]; then cp -p "$backup/stack.env" "$env_file"; fi
  if [[ $caddy_changed == true ]]; then cp -p "$backup/Caddyfile" "$caddyfile"; fi
  # До HAProxy прежний релиз сам публиковал свои службы на TCP 443.
  if [[ $edge_enabled == true && $old_edge != true ]]; then compose_new stop edge || true; fi
  if [[ $port_transfer == true && $stack_started == true ]]; then
    compose_new stop caddy || true
    if [[ $edge_enabled == true ]]; then compose_new stop edge || true; fi
  fi
  if [[ $haproxy_changed == true ]]; then
    if [[ -f $backup/haproxy.cfg ]]; then
      cp -p "$backup/haproxy.cfg" "$haproxy_cfg"
    else
      rm -f "$haproxy_cfg"
    fi
  fi
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
  compose_old up -d --wait "${old_services[@]}" || true
  # Контейнеры, запущенные этой выкладкой, читают заменённые файлы; новые прочитают восстановленные.
  if [[ $stack_started == true && $caddy_changed == true ]]; then
    compose_old up -d --wait --no-deps --force-recreate caddy || true
  fi
  if [[ $stack_started == true && $haproxy_changed == true && $old_edge == true ]]; then
    compose_old up -d --wait --no-deps --force-recreate edge || true
  fi
  if [[ $vless_failed == true ]]; then
    if vless_works "$old_release" && \
        { [[ $old_fallback_port == 0 ]] || vless_works "$old_release" "$old_fallback_port"; }; then
      printf 'VLESS traffic passes again with the previous release.\n' >&2
    else
      printf 'VLESS traffic fails with the previous release too: check the network and the REALITY target.\n' >&2
    fi
  fi
  printf 'Check Docker health and the backup before another deployment.\n' >&2
}
trap 'rollback; cleanup' EXIT
compose_old exec -T postgres pg_dump -Fc -U gateway gateway > "$backup/gateway.dump"
[[ -s $backup/gateway.dump ]] || fail 'Database backup is empty.'
database_saved=true

compose_new run --rm --no-deps api migrate
python3 - "$env_file" "PG_RELEASE_SHA=$version" "${image_tags[@]}" <<'PY'
import os
from pathlib import Path
import sys
import tempfile

path = Path(sys.argv[1])
lines = path.read_text().splitlines()
for assignment in sys.argv[2:]:
    name = assignment.split('=', 1)[0]
    matches = [i for i, line in enumerate(lines) if line.startswith(name + '=')]
    if len(matches) > 1 or (name == 'PG_RELEASE_SHA' and not matches):
        raise SystemExit(f'Expected exactly one {name} in stack.env')
    if matches:
        lines[matches[0]] = assignment
    else:
        lines.append(assignment)
with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, prefix='.stack.', delete=False) as output:
    output.write('\n'.join(lines) + '\n')
    staged = output.name
os.chmod(staged, 0o600)
os.replace(staged, path)
PY
env_changed=true
# Примонтированный файл сохраняет то содержимое, с которым запустился его контейнер, поэтому
# заменённому файлу нужен новый контейнер. Изменение файла HAProxy или опубликованных портов
# пересоздаёт edge и рвёт VPN-подключения.
if [[ -f $caddyfile.next ]]; then
  mv -f -- "$caddyfile.next" "$caddyfile"
  caddy_changed=true
fi
if [[ -f $haproxy_cfg.next ]]; then
  mv -f -- "$haproxy_cfg.next" "$haproxy_cfg"
  haproxy_changed=true
fi
stack_started=true
if [[ $port_transfer == true ]]; then
  compose_old stop caddy
  if [[ $old_edge == true ]]; then compose_old stop edge; fi
fi
compose_new up -d --wait "${services[@]}"
if [[ $caddy_changed == true ]]; then compose_new up -d --wait --no-deps --force-recreate caddy; fi
if [[ $haproxy_changed == true ]]; then compose_new up -d --wait --no-deps --force-recreate edge; fi
compose_new exec -T api /usr/local/bin/gatewayctl config apply
ready=false
for _ in $(seq 1 30); do
  if curl --noproxy '*' --fail --silent --show-error --max-time 4 \
      --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/readyz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
if [[ $ready != true ]]; then
  printf 'HTTPS readiness failed; final TLS request and Caddy logs follow.\n' >&2
  curl --noproxy '*' --verbose --output /dev/null --max-time 10 \
    --write-out 'HTTP %{http_code}; TLS verification %{ssl_verify_result}\n' \
    --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/readyz" \
    2>&1 | tail -n 45 >&2 || true
  compose_new logs --no-color --tail=80 caddy >&2 || true
  if [[ $edge_enabled == true ]]; then compose_new logs --no-color --tail=80 edge >&2 || true; fi
  fail 'Panel HTTPS readiness check failed.'
fi
panel=$(curl --noproxy '*' --fail --silent --show-error --max-time 10 \
  --resolve "$domain:$panel_port:127.0.0.1" "https://$domain:$panel_port/") \
  || fail 'Panel HTML could not be loaded through HTTPS.'
[[ $panel == *'<div id="root"></div>'* ]] || fail 'Panel HTML is missing; check API image file permissions.'
unset panel
browser_auth_status=$(curl --noproxy '*' --silent --show-error --output /dev/null \
  --write-out '%{http_code}' --max-time 10 --resolve "$domain:$panel_port:127.0.0.1" \
  "https://$domain:$panel_port/browser/vnc.html") || fail 'Browser access check failed.'
[[ $browser_auth_status == 403 ]] || fail 'Browser is not protected by administrator authentication.'
if [[ $fallback_port != 0 ]]; then
  curl --noproxy '*' --fail --silent --show-error --max-time 10 \
    --resolve "$domain:$fallback_port:127.0.0.1" "https://$domain:$fallback_port/readyz" >/dev/null \
    || fail 'Fallback HTTPS readiness check failed.'
  fallback_browser_status=$(curl --noproxy '*' --silent --show-error --output /dev/null \
    --write-out '%{http_code}' --max-time 10 --resolve "$domain:$fallback_port:127.0.0.1" \
    "https://$domain:$fallback_port/browser/vnc.html") || fail 'Fallback browser access check failed.'
  [[ $fallback_browser_status == 403 ]] || fail 'Fallback browser is not protected by administrator authentication.'
fi
gateway_status=$(compose_new exec -T api /usr/local/bin/gatewayctl status)
printf '%s\n' "$gateway_status"
active_devices=$(awk '$1 == "ACTIVE" && $2 == "DEVICES" {print $3}' <<< "$gateway_status")
[[ $active_devices =~ ^[0-9]+$ ]] || fail 'The number of active devices could not be read.'
# Ответ панели ничего не доказывает о VPN-клиентах: их трафик тоже должен проходить.
if ((active_devices == 0)); then
  printf 'VLESS traffic check skipped: there is no active device yet.\n'
elif ! vless_works "$release"; then
  vless_failed=true
  fail 'VLESS traffic does not pass through the new release.'
elif [[ $fallback_port != 0 ]] && ! vless_works "$release" "$fallback_port"; then
  vless_failed=true
  fail 'VLESS traffic does not pass through the fallback port.'
fi
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
