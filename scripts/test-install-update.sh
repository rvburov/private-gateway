#!/usr/bin/env bash
# Установка, обновление и откат production-стека на этой машине так, как их получает VPS:
# bootstrap.sh, затем доставки CI через принудительную команду учётной записи выкладки.
# Занимает порты 80, 443 и 8443 и пишет в /etc, /opt и /var: только для одноразовых runner'ов CI.
set -Eeuo pipefail

fail() { printf 'Installation test failed: %s\n' "$*" >&2; exit 1; }
archive=${1:?Pass the release.tar.gz path}
[[ ${GITHUB_ACTIONS:-} == true ]] || fail 'run it only on a disposable GitHub Actions runner.'
repository=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
archive=$(realpath -- "$archive")
domain=panel.gateway.localhost
env_file=/etc/private-gateway-docker/stack.env
work=$(mktemp -d)
collector=''

compose() {
  sudo docker compose --env-file "$env_file" -f /opt/private-gateway-docker/current/docker/compose.yml "$@"
}
# Неудачная установка удаляет свои контейнеры, а обновление заменяет их: журнал каждого
# контейнера сохраняется по ходу работы, по файлу на контейнер.
watch_xray_status() {
  local status last=''
  while :; do
    # Состояние Xray worker (хеш конфигурации, running, error) секретов не содержит.
    status=$(sudo cat /var/lib/private-gateway-docker/xray/xray.status.json 2>/dev/null) || status=''
    if [[ -n $status && $status != "$last" ]]; then
      printf '%s %s\n' "$(date -u +%T.%3N)" "$status" >> "$work/logs/xray-status.log"
      last=$status
    fi
    sleep 0.2
  done
}
collect_logs() {
  local id service
  while :; do
    # Только запущенные контейнеры: слежение за ещё не запущенным контейнером сразу заканчивается.
    for id in $(docker ps --quiet --filter label=com.docker.compose.project=private-gateway); do
      [[ ! -e $work/logs/$id ]] || continue
      service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$id") || continue
      : > "$work/logs/$id"
      docker logs --follow --timestamps "$id" > "$work/logs/$service-$id.log" 2>&1 &
      printf '%s\n' "$!" >> "$work/logs/pids"
    done
    sleep 1
  done
}
finish() {
  local status=$? log
  if [[ -n $collector ]]; then
    kill $collector 2>/dev/null || true
    xargs -r kill < "$work/logs/pids" 2>/dev/null || true
  fi
  ((status != 0)) || return 0
  printf '\nContainers and their logs follow.\n' >&2
  docker ps --all --filter label=com.docker.compose.project=private-gateway >&2 || true
  for log in "$work"/logs/*.log; do
    [[ -e $log ]] || continue
    printf -- '--- %s\n' "${log##*/}" >&2
    tail -n 60 "$log" >&2
  done
}
trap finish EXIT
mkdir "$work/logs"
touch "$work/logs/pids"
collect_logs &
collector=$!
watch_xray_status &
collector+=" $!"
# Шаг выкладки CI отправляет SHA и архив по SSH этой принудительной команде gateway-deploy.
deliver() {
  (cd / && { printf '%s\n' "$1"; cat "$2"; } \
    | sudo -u gateway-deploy sudo -n /usr/local/sbin/private-gateway-docker-deploy)
}
installed() { sudo cat /etc/private-gateway-docker/installed.version; }
container() { compose ps --status running --quiet "$1"; }
# Публичные порты читает тот же helper, что установщик и обновлятор.
public_port() {
  compose config --format json | python3 "$repository/deploy/docker/release-ports.py" --field "$1"
}
panel_ready() {
  local port=${1:-$(public_port panel)}
  curl --fail --silent --show-error --max-time 10 --output /dev/null \
    --resolve "$domain:$port:127.0.0.1" "https://$domain:$port/readyz"
}
browser_protected() {
  local port=${1:-$(public_port panel)} status
  status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 10 \
    --resolve "$domain:$port:127.0.0.1" "https://$domain:$port/browser/vnc.html")
  [[ $status == 403 ]] || fail "noVNC is not protected on $port."
}
vless_works() {
  local attempt port=${1:-$(public_port vpn)}
  for attempt in 1 2 3; do
    if sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py --port "$port"; then return 0; fi
    ((attempt == 3)) || sleep 5
  done
  return 1
}
# Хеши не раскрывают секреты и должны совпадать после перехода и отката.
identity() {
  sudo python3 - "$env_file" <<'PY'
import hashlib
from pathlib import Path
import sys
values = dict(line.split('=', 1) for line in Path(sys.argv[1]).read_text().splitlines() if '=' in line)
names = ('GATEWAY_MASTER_KEY', 'GATEWAY_REALITY_PRIVATE_KEY', 'GATEWAY_SHORT_ID',
         'GATEWAY_API_TOKEN', 'POSTGRES_PASSWORD', 'BROWSER_WORKER_TOKEN')
print(hashlib.sha256('\n'.join(values[name] for name in names).encode()).hexdigest())
PY
  compose exec -T postgres psql -At -U gateway -d gateway -c \
    'SELECT id, node_id, name, encode(credential_ciphertext, '\''hex'\''), subscription_generation, encode(subscription_hash, '\''hex'\''), revoked_at FROM devices ORDER BY id' \
    | sha256sum
}
http_redirect() {
  local redirect
  redirect=$(curl --silent --show-error --output /dev/null --write-out '%{http_code} %{redirect_url}' --max-time 10 \
    --resolve "$domain:80:127.0.0.1" "http://$domain/migration/path?query=kept")
  [[ $redirect == "301 https://$domain:8443/migration/path?query=kept" ]] \
    || fail "HTTP did not preserve the path and query on8443: $redirect"
}
check_identity() {
  [[ $(identity) == "$saved_identity" ]] || fail 'The device credentials, subscription or server keys changed.'
}
# Проверяем установленный worker, общий файл состояния и чтение его API. Токен остаётся
# внутри уже настроенного API-контейнера; ни ответ, ни переменные окружения не печатаются.
worker_telemetry() {
  compose exec -T api python3 - <<'PY'
from datetime import datetime, timezone
import json
import os
import time
import urllib.error
import urllib.request

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
request = urllib.request.Request(
    "http://127.0.0.1:8080/api/v1/checks/performance/server",
    headers={"Authorization": "Bearer " + os.environ["GATEWAY_API_TOKEN"]},
)


def counter(group, name):
    return isinstance(group, dict) and type(group.get(name)) is int and group[name] >= 0


def validate(document):
    if not isinstance(document, dict) or not isinstance(document.get("vpn"), dict):
        return "the API did not return VPN telemetry"
    vpn = document["vpn"]
    if vpn.get("status") != "ok" or vpn.get("running") is not True:
        return "the installed worker did not report a running Xray"
    try:
        sampled = datetime.fromisoformat(vpn["sampledAt"].replace("Z", "+00:00"))
        age = (datetime.now(timezone.utc) - sampled).total_seconds()
    except (KeyError, AttributeError, TypeError, ValueError):
        return "the worker sample time is invalid"
    if not -5 <= age <= 15:
        return "the worker sample is not fresh"
    # На одноразовом Ubuntu 26.04 runner Docker предоставляет собственный cgroup
    # контейнера. Неограниченные memory.max/cpu.max вправе возвращать null.
    for name in ("memoryBytes", "cpuUsageUsec"):
        if not counter(vpn.get("cgroup"), name):
            return "the worker cgroup counters are unavailable"
    for name in ("rxBytes", "txBytes"):
        if not counter(vpn.get("network"), name):
            return "the worker network counters are unavailable"
    for name in ("outSegments", "retransmittedSegments"):
        if not counter(vpn.get("tcp"), name):
            return "the worker TCP counters are unavailable"
    return None


deadline = time.monotonic() + 10
reason = "the diagnostic API is unavailable"
while (remaining := deadline - time.monotonic()) > 0:
    try:
        with opener.open(request, timeout=min(1, remaining)) as response:
            data = response.read(65537)
            if response.status != 200 or response.headers.get("Cache-Control") != "no-store":
                reason = "the diagnostic response status or cache policy is invalid"
            elif len(data) > 65536:
                reason = "the diagnostic response is too large"
            else:
                reason = validate(json.loads(data))
        if reason is None:
            print("Installed VPN telemetry passed: running Xray, fresh sample, cgroup, network and TCP counters.")
            break
    except (OSError, urllib.error.URLError, ValueError):
        reason = "the diagnostic API is unavailable or returned invalid JSON"
    time.sleep(min(0.5, max(0, deadline - time.monotonic())))
else:
    raise SystemExit("Installed VPN telemetry failed: " + reason + ".")
PY
}
check_stack() {
  panel_ready || fail 'The panel is unavailable.'
  browser_protected
  vless_works || fail 'VLESS traffic is unavailable.'
  local fallback
  fallback=$(public_port fallback)
  if [[ $fallback != 0 ]]; then
    panel_ready "$fallback" || fail 'The fallback panel is unavailable.'
    browser_protected "$fallback"
    vless_works "$fallback" || fail 'The fallback VLESS traffic is unavailable.'
  fi
  check_identity
  local status desired applied
  status=$(compose exec -T api /usr/local/bin/gatewayctl status)
  desired=$(awk '$1 == "DESIRED" && $2 == "REVISION" {print $3}' <<< "$status")
  applied=$(awk '$1 == "APPLIED" && $2 == "REVISION" {print $3}' <<< "$status")
  [[ $desired =~ ^[0-9]+$ && $desired == "$applied" ]] || fail 'The desired revision was not applied.'
  worker_telemetry
}

version_a=$(python3 "$repository/scripts/verify-release.py" --tar "$archive")
version_b=$(printf 'b%.0s' {1..40})
version_e=$(printf 'e%.0s' {1..40})
version_f=$(printf 'f%.0s' {1..40})
version_g=$(printf '1%.0s' {1..40})
version_h=$(printf '2%.0s' {1..40})
version_i=$(printf '3%.0s' {1..40})
# В тестовых архивах меняются только проверяемые файлы; manifest пересчитывается.
# E — текущая схема 443+8443 с основным443. F — будущий основной8443 с обоими входами.
# G/H ломают панель/VLESS именно во время смены основного порта. I убирает внешний443.
python3 - "$archive" "$work" "$version_b" "$version_e" "$version_f" "$version_g" "$version_h" "$version_i" <<'PY'
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile

source, work = map(Path, sys.argv[1:3])
with tarfile.open(source, "r:gz") as archive:
    manifest = json.load(archive.extractfile("manifest.json"))
    files = {member.name: archive.extractfile(member).read()
             for member in archive if member.isfile() and member.name != "manifest.json"}


def write(version, changes, filename=None):
    content = {**files, **changes}
    hashes = {name: hashlib.sha256(data).hexdigest() for name, data in content.items()}
    entries = {"manifest.json": json.dumps({**manifest, "version": version, "files": hashes},
                                           sort_keys=True, separators=(",", ":")).encode() + b"\n", **content}
    with (work / (filename or f"release-{version[0]}.tar.gz")).open("wb") as raw, \
            gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped, \
            tarfile.open(fileobj=zipped, mode="w", format=tarfile.USTAR_FORMAT) as output:
        for name, data in sorted(entries.items()):
            member = tarfile.TarInfo(name)
            member.size = len(data)
            member.mode = 0o755 if name.startswith("bin/") else 0o644
            output.addfile(member, io.BytesIO(data))


def compose_at(port, only=False):
    text = files["docker/compose.yml"].decode()
    text = re.sub(r'GATEWAY_PORT: "\d+"', f'GATEWAY_PORT: "{port}"', text)
    text = re.sub(r'^      GATEWAY_FALLBACK_PORT:.*\n', '', text, flags=re.M)
    text = text.replace(f'GATEWAY_PORT: "{port}"\n', f'GATEWAY_PORT: "{port}"\n      GATEWAY_FALLBACK_PORT: "{8443 if port == 443 else 0}"\n')
    text = re.sub(r'(GATEWAY_BROWSER_PUBLIC_URL: https://\$\{PANEL_DOMAIN[^}]*})(?::\d+)?',
                  lambda m: m[1] + (':8443' if port == 8443 else ''), text)
    text = re.sub(r'(  edge:\n.*?    ports:\n)(?:      - [^\n]*\n)+',
                  lambda m: m[1] + ('' if only else '      - "0.0.0.0:443:443/tcp"\n')
                  + '      - "0.0.0.0:8443:443/tcp"\n', text, flags=re.S)
    return text.encode()


def caddy_at(port):
    text = files['docker/Caddyfile.template'].decode()
    # Только .localhost-стенд: CA runner заранее установлен. Production ACME не меняется.
    text = re.sub(r'(?m)^(\s*)tls \{\n\s*issuer acme \{\n\s*disable_tlsalpn_challenge\n\s*}\n\s*}',
                  r'\1tls internal', text)
    text = re.sub(r'https://__PANEL_DOMAIN__(?::8443)?\{uri}',
                  f'https://__PANEL_DOMAIN__{":8443" if port == 8443 else ""}{{uri}}', text)
    if 'http://__PANEL_DOMAIN__' not in text:
        destination = f'https://__PANEL_DOMAIN__{":8443" if port == 8443 else ""}{{uri}}'
        text += '\nhttp://__PANEL_DOMAIN__ {\n    redir ' + destination + ' permanent\n}\n'
    return text.encode()

# Первичная установка использует тот же релиз, но локальный сертификат вместо public ACME.
initial_caddy = files['docker/Caddyfile.template'].decode()
initial_caddy = re.sub(r'(?m)^(\s*)tls \{\n\s*issuer acme \{\n\s*disable_tlsalpn_challenge\n\s*}\n\s*}',
                       r'\1tls internal', initial_caddy)
write(manifest['version'], {'docker/Caddyfile.template': initial_caddy.encode()}, 'release-initial.tar.gz')

route = 'use_backend panel_https if { req.ssl_sni -i __PANEL_DOMAIN__ }'
default = 'default_backend vpn_reality'
template = files['docker/haproxy.cfg.template'].decode()
if route not in template or default not in template:
    raise SystemExit('The expected HAProxy routes are missing.')
write(sys.argv[3], {'web/installation-test.txt': b'A file that only the API image contains.\n', 'docker/Caddyfile.template': initial_caddy.encode()})
write(sys.argv[4], {'docker/compose.yml': compose_at(443), 'docker/Caddyfile.template': caddy_at(443)})
main = {'docker/compose.yml': compose_at(8443), 'docker/Caddyfile.template': caddy_at(8443)}
write(sys.argv[5], main)
write(sys.argv[6], {**main, 'docker/haproxy.cfg.template': template.replace(route, route.replace('panel_https', 'vpn_reality')).encode()})
write(sys.argv[7], {**main, 'docker/haproxy.cfg.template': template.replace(default, 'default_backend panel_https').encode()})
write(sys.argv[8], {**main, 'docker/compose.yml': compose_at(8443, only=True)})
PY

# Имена .localhost получают сертификаты от доверенного тестового CA, без ослабления TLS.
cat > "$work/ca.cnf" <<'CA'
[req]
distinguished_name = name
x509_extensions = ca
prompt = no
[name]
CN = Private Gateway installation test CA
[ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
CA
openssl req -x509 -config "$work/ca.cnf" -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -noenc \
  -days 30 -keyout "$work/root.key" -out "$work/root.crt"
sudo install -m 0644 "$work/root.crt" /usr/local/share/ca-certificates/private-gateway-test.crt
sudo update-ca-certificates >/dev/null
caddy_image=$(sed -n 's/^ *image: \(caddy:[^ ]*\)$/\1/p' "$repository/deploy/docker/compose.yml")
seed_ca() {
  docker volume create private-gateway_caddy-data >/dev/null
  docker run --rm --network none --entrypoint sh -v private-gateway_caddy-data:/data \
    -v "$work/root.crt:/ca/root.crt:ro" -v "$work/root.key:/ca/root.key:ro" "$caddy_image" -c \
    'install -d -m 0700 /data/caddy/pki/authorities/local && install -m 0600 /ca/root.crt /ca/root.key /data/caddy/pki/authorities/local/'
}
seed_ca
printf '127.0.0.1 %s\n' "$domain" | sudo tee -a /etc/hosts >/dev/null

# Тот же комплект, который получает новый VPS через scripts/add-server.sh.
bundle=$work/bundle
mkdir "$bundle"
cp "$repository/deploy/docker/bootstrap.sh" "$repository/deploy/docker/bootstrap-receiver.sh" \
  "$repository/scripts/verify-release.py" "$repository/deploy/servers/deploy-key.pub" "$bundle/"
bootstrap() {
  local version=$1 release_archive=$2
  printf '%s\n' "$version" > "$bundle/commit.sha"
  (cd "$bundle" && sha256sum bootstrap.sh bootstrap-receiver.sh verify-release.py commit.sha > SHA256SUMS)
  sudo bash "$bundle/bootstrap.sh" --ref "$version" --host 127.0.0.1 --domain "$domain" \
    --deploy-key "$bundle/deploy-key.pub"
  deliver "$version" "$release_archive"
  [[ $(installed) == "$version" ]] || fail 'The first deployment installed another version.'
}
printf '\n== Bootstrap and first deployment ==\n'
bootstrap "$version_a" "$work/release-initial.tar.gz" 2>&1 | tee "$work/install.log"
panel_ready
browser_protected
node=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "NODE" {print $2}')
compose exec -T api /usr/local/bin/gatewayctl device add installation-test --node "$node" >/dev/null
compose exec -T api /usr/local/bin/gatewayctl config apply
# Экспорт при первом smoke заполняет хеш подписки; снимок должен включать его.
vless_works || fail 'Initial VLESS traffic is unavailable.'
saved_identity=$(identity)
check_stack

printf '\n== Panel-only update preserves unchanged containers ==\n'
declare -A before
for service in postgres api browser xray caddy edge; do before[$service]=$(container "$service"); done
deliver "$version_b" "$work/release-b.tar.gz" 2>&1 | tee "$work/update-b.log"
for service in postgres browser xray caddy edge; do
  [[ $(container "$service") == "${before[$service]}" ]] || fail "$service was recreated without a change."
done
[[ $(container api) != "${before[api]}" ]] || fail 'The changed API image was not installed.'
check_stack
deliver "$version_b" "$work/release-b.tar.gz" 2>&1 | tee "$work/repeat-b.log"
grep -Fx "Installed release verified: $version_b; panel=$(public_port panel); vpn=$(public_port vpn); updater=current" "$work/repeat-b.log" \
  || fail 'The installed updater did not verify itself.'

printf '\n== Preparation release keeps the old primary443 and both public ports ==\n'
deliver "$version_e" "$work/release-e.tar.gz" 2>&1 | tee "$work/preparation.log"
[[ $(public_port panel) == 443 && $(public_port vpn) == 443 ]] || fail 'The preparation changed the primary port.'
check_stack

printf '\n== Failed panel transition restores the previous ports and credentials ==\n'
if deliver "$version_g" "$work/release-1.tar.gz" 2>&1 | tee "$work/panel-rollback.log"; then
  fail 'A broken panel was accepted.'
fi
[[ $(installed) == "$version_e" ]] || fail 'The panel rollback did not restore the release.'
[[ $(public_port panel) == 443 && $(public_port vpn) == 443 ]] || fail 'The panel rollback did not restore the old ports.'
check_stack

printf '\n== Failed VLESS transition rolls back while the panel itself works ==\n'
if deliver "$version_h" "$work/release-2.tar.gz" 2>&1 | tee "$work/vless-rollback.log"; then
  fail 'Broken VLESS traffic was accepted.'
fi
grep -q '^Docker deployment stopped: VLESS traffic does not pass through the new release.' "$work/vless-rollback.log" \
  || fail 'The failure was not detected by the real VLESS test.'
grep -q '^VLESS traffic passes again with the previous release.' "$work/vless-rollback.log" \
  || fail 'VLESS was not verified after the rollback.'
[[ $(installed) == "$version_e" ]] || fail 'The VLESS rollback did not restore the release.'
check_stack

previous_revision=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "DESIRED" && $2 == "REVISION" {print $3}')
printf '\n== Move the canonical panel and profiles to8443, retaining both public ports ==\n'
deliver "$version_f" "$work/release-f.tar.gz" 2>&1 | tee "$work/primary8443.log"
[[ $(public_port panel) == 8443 && $(public_port vpn) == 8443 ]] || fail 'The public ports did not change.'
check_stack
current_revision=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "DESIRED" && $2 == "REVISION" {print $3}')
((current_revision > previous_revision)) || fail 'The public port change did not advance the configuration revision.'
panel_ready 443 || fail 'The compatibility panel port443 disappeared.'
http_redirect

printf '\n== A release may also remove external443 without changing internal Xray443 ==\n'
deliver "$version_i" "$work/release-3.tar.gz" 2>&1 | tee "$work/single8443.log"
check_stack
compose config --format json | python3 -c 'import json,sys; c=json.load(sys.stdin); assert not any(str(p.get("published")) == "443" for s in c["services"].values() for p in s.get("ports", []))'
if curl --silent --max-time 3 --resolve "$domain:443:127.0.0.1" "https://$domain/readyz" >/dev/null; then
  fail 'The single-port release still depends on an externally published443.'
fi

# Разрушительная очистка относится только к этому одноразовому runner. Следом проверяется
# установка с нуля на8443, без внешнего443; рабочий VPS этот скрипт запускать отказывается.
printf '\n== Fresh single8443 installation on the disposable runner ==\n'
compose down --volumes --remove-orphans
sudo rm -rf /etc/private-gateway-docker /opt/private-gateway-docker /var/lib/private-gateway-docker/xray
seed_ca
bootstrap "$version_i" "$work/release-3.tar.gz" 2>&1 | tee "$work/fresh8443.log"
[[ $(public_port panel) == 8443 && $(public_port vpn) == 8443 ]] || fail 'Fresh installation did not select8443.'
panel_ready
browser_protected
node=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "NODE" {print $2}')
compose exec -T api /usr/local/bin/gatewayctl device add fresh8443-test --node "$node" >/dev/null
compose exec -T api /usr/local/bin/gatewayctl config apply
vless_works || fail 'Fresh installation does not pass real VLESS through8443.'
worker_telemetry
http_redirect
printf '\nInstallation, port transitions, two rollbacks and fresh8443 installation passed.\n'
