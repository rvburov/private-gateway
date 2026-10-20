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
panel_ready() {
  curl --fail --silent --show-error --max-time 10 --output /dev/null \
    --resolve "$domain:${1:-443}:127.0.0.1" "https://$domain:${1:-443}/readyz"
}
fallback_ready() {
  panel_ready 8443 || fail 'the fallback panel does not answer through HAProxy.'
  local status
  status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 10 \
    --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/browser/vnc.html")
  [[ $status == 403 ]] || fail 'fallback noVNC is not protected.'
}
legacy_redirect() {
  local redirect
  redirect=$(curl --silent --show-error --output /dev/null --write-out '%{http_code} %{redirect_url}' --max-time 10 \
    --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz")
  [[ $redirect == "301 https://$domain/readyz" ]] || fail 'legacy redirect was not restored.'
}
# Пересозданный Xray получает новый адрес; HAProxy узнаёт его из DNS Docker за секунды.
vless_works() {
  local attempt
  for attempt in 1 2 3; do
    if sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py --port "${1:-443}"; then return 0; fi
    ((attempt == 3)) || sleep 5
  done
  return 1
}

version_a=$(python3 "$repository/scripts/verify-release.py" --tar "$archive")
version_b=$(printf 'b%.0s' {1..40})
version_c=$(printf 'c%.0s' {1..40})
version_d=$(printf 'd%.0s' {1..40})
version_e=$(printf 'e%.0s' {1..40})
version_f=$(printf 'f%.0s' {1..40})
version_g=$(printf '1%.0s' {1..40})
# B — та же сборка с ещё одним файлом панели, поэтому новый тег по содержимому получает только
# образ API. C отправляет имя панели в Xray вместо Caddy: проверка панели проваливается, только
# если обновление действительно перезапускает HAProxy, и тогда оно должно откатиться. D отправляет
# в Caddy все остальные имена, включая VPN-клиентов: панель работает, и поймать это может только
# проверка VLESS.
python3 - "$archive" "$work" "$version_b" "$version_c" "$version_d" "$version_e" "$version_f" "$version_g" <<'PY'
import gzip
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile

source, work = map(Path, sys.argv[1:3])
with tarfile.open(source, "r:gz") as archive:
    manifest = json.load(archive.extractfile("manifest.json"))
    files = {member.name: archive.extractfile(member).read()
             for member in archive if member.isfile() and member.name != "manifest.json"}


def write(version, changes):
    content = {**files, **changes}
    hashes = {name: hashlib.sha256(data).hexdigest() for name, data in content.items()}
    entries = {"manifest.json": json.dumps({**manifest, "version": version, "files": hashes},
                                           sort_keys=True, separators=(",", ":")).encode() + b"\n",
               **content}
    with (work / f"release-{version[0]}.tar.gz").open("wb") as raw, \
            gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped, \
            tarfile.open(fileobj=zipped, mode="w", format=tarfile.USTAR_FORMAT) as output:
        for name, data in sorted(entries.items()):
            member = tarfile.TarInfo(name)
            member.size = len(data)
            member.mode = 0o755 if name.startswith("bin/") else 0o644
            output.addfile(member, io.BytesIO(data))


route = "use_backend panel_https if { req.ssl_sni -i __PANEL_DOMAIN__ }"
default = "default_backend vpn_reality"
template = files["docker/haproxy.cfg.template"].decode()
if route not in template or default not in template:
    raise SystemExit("The panel route or the default route is missing from the HAProxy template.")
write(sys.argv[3], {"web/installation-test.txt": b"A panel file that only the API image contains.\n"})
write(sys.argv[4], {"docker/haproxy.cfg.template":
                    template.replace(route, route.replace("panel_https", "vpn_reality")).encode()})
write(sys.argv[5], {"docker/haproxy.cfg.template":
                    template.replace(default, "default_backend panel_https").encode()})
# E имитирует подготовительный релиз: обновлённый установщик, но старый владелец 8443.
compose = files["docker/compose.yml"].decode()
if '      - "0.0.0.0:8443:443/tcp"\n' not in compose:
    raise SystemExit("The fallback edge mapping is missing.")
legacy = compose.replace('      - "0.0.0.0:8443:443/tcp"\n', '')
legacy = legacy.replace('      - "0.0.0.0:80:80/tcp"\n',
                        '      - "0.0.0.0:80:80/tcp"\n      - "0.0.0.0:8443:8443/tcp"\n')
legacy = legacy.replace('GATEWAY_FALLBACK_PORT: "8443"', 'GATEWAY_FALLBACK_PORT: "0"')
redirect = b"\nhttps://__PANEL_DOMAIN__:8443 {\n    redir https://__PANEL_DOMAIN__{uri} permanent\n}\n"
write(sys.argv[6], {"docker/compose.yml": legacy.encode(),
                    "docker/Caddyfile.template": files["docker/Caddyfile.template"] + redirect})
write(sys.argv[7], {})
# В G основной вход исправен, но публикация резервного порта потеряна.
write(sys.argv[8], {"docker/compose.yml":
                    compose.replace('      - "0.0.0.0:8443:443/tcp"\n', '').encode()})
PY

# Имена .localhost Caddy подписывает своим локальным центром сертификации. Тест выдаёт Caddy центр,
# которому доверяет этот runner, поэтому установщик и обновление проверяют панель с полной
# проверкой TLS, как на VPS.
cat > "$work/ca.cnf" <<'EOF'
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
EOF
openssl req -x509 -config "$work/ca.cnf" -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -noenc \
  -days 30 -keyout "$work/root.key" -out "$work/root.crt"
sudo install -m 0644 "$work/root.crt" /usr/local/share/ca-certificates/private-gateway-test.crt
sudo update-ca-certificates >/dev/null
caddy_image=$(sed -n 's/^ *image: \(caddy:[^ ]*\)$/\1/p' "$repository/deploy/docker/compose.yml")
docker volume create private-gateway_caddy-data >/dev/null
docker run --rm --network none --entrypoint sh -v private-gateway_caddy-data:/data \
  -v "$work/root.crt:/ca/root.crt:ro" -v "$work/root.key:/ca/root.key:ro" "$caddy_image" -c \
  'install -d -m 0700 /data/caddy/pki/authorities/local && install -m 0600 /ca/root.crt /ca/root.key /data/caddy/pki/authorities/local/'
printf '127.0.0.1 %s\n' "$domain" | sudo tee -a /etc/hosts >/dev/null

printf '\n== Bootstrap and first deployment of %s ==\n' "$version_a"
# Тот же комплект, который загружает scripts/add-server.sh.
bundle=$work/bundle
mkdir "$bundle"
cp "$repository/deploy/docker/bootstrap.sh" "$repository/deploy/docker/bootstrap-receiver.sh" \
  "$repository/scripts/verify-release.py" "$repository/deploy/servers/deploy-key.pub" "$bundle/"
printf '%s\n' "$version_a" > "$bundle/commit.sha"
(cd "$bundle" && sha256sum bootstrap.sh bootstrap-receiver.sh verify-release.py commit.sha > SHA256SUMS)
sudo bash "$bundle/bootstrap.sh" --ref "$version_a" --host 127.0.0.1 --domain "$domain" \
  --deploy-key "$bundle/deploy-key.pub"
deliver "$version_a" "$archive" 2>&1 | tee "$work/install.log"
grep -qx "First Docker deployment verified: $version_a" "$work/install.log" \
  || fail 'the first deployment did not report success.'
[[ $(installed) == "$version_a" ]] || fail 'the installed version is not the delivered release.'
panel_ready || fail 'the panel does not answer through HAProxy.'
fallback_ready
node=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "NODE" {print $2}')
compose exec -T api /usr/local/bin/gatewayctl device add installation-test --node "$node" >/dev/null
compose exec -T api /usr/local/bin/gatewayctl config apply
vless_works || fail 'VLESS traffic does not pass after the first deployment.'
vless_works 8443 || fail 'fallback VLESS traffic does not pass after the first deployment.'
xray_container=$(container xray)

printf '\n== Update to %s, which changes only the panel ==\n' "$version_b"
declare -A before
for service in postgres api browser xray caddy edge; do before[$service]=$(container "$service"); done
deliver "$version_b" "$work/release-b.tar.gz" 2>&1 | tee "$work/update-b.log"
grep -q "^Docker deployment verified: $version_b\. " "$work/update-b.log" \
  || fail 'the update did not report success.'
grep -q '^VLESS traffic OK' "$work/update-b.log" || fail 'the update did not check VLESS traffic.'
[[ $(installed) == "$version_b" ]] || fail 'the update did not switch the installed version.'
for service in postgres browser xray caddy edge; do
  [[ $(container "$service") == "${before[$service]}" ]] \
    || fail "$service was recreated although its image and configuration did not change."
done
[[ $(container api) != "${before[api]}" ]] || fail 'the API was not recreated with its new image.'
panel_ready || fail 'the panel does not answer after the update.'
vless_works || fail 'VLESS traffic does not pass after the update.'
fallback_ready
vless_works 8443 || fail 'fallback VLESS traffic does not pass after the update.'
deliver "$version_b" "$work/release-b.tar.gz" 2>&1 | tee "$work/repeat-b.log"
grep -qx "Release $version_b is already installed." "$work/repeat-b.log" \
  || fail 'a repeated delivery was not recognised as installed.'
sudo cp /etc/private-gateway-docker/haproxy.cfg "$work/haproxy.cfg"

printf '\n== Update to %s, which sends the panel to Xray ==\n' "$version_c"
if deliver "$version_c" "$work/release-c.tar.gz" 2>&1 | tee "$work/update-c.log"; then
  fail 'the updater accepted a release whose HAProxy routing breaks the panel.'
fi
grep -q '^Deployment failed. Restoring the previous database and release.' "$work/update-c.log" \
  || fail 'the failed update did not roll back.'
[[ $(installed) == "$version_b" ]] || fail 'the rollback did not restore the installed version.'
[[ $(sudo readlink -f /opt/private-gateway-docker/current) == "/opt/private-gateway-docker/releases/$version_b" ]] \
  || fail 'the rollback did not restore the release pointer.'
sudo test ! -e "/opt/private-gateway-docker/releases/$version_c" || fail 'the failed release was left installed.'
sudo cmp -s /etc/private-gateway-docker/haproxy.cfg "$work/haproxy.cfg" \
  || fail 'the rollback did not restore the HAProxy configuration.'
[[ -n $(container edge) ]] || fail 'HAProxy is not running after the rollback.'
panel_ready || fail 'the panel does not answer after the rollback.'
# Читается целиком: grep -q выходит на первом совпадении, и pipefail счёл бы SIGPIPE пишущего сбоем.
gateway_status=$(compose exec -T api /usr/local/bin/gatewayctl status)
grep -Eq '^ACTIVE DEVICES +1$' <<< "$gateway_status" \
  || fail "the device did not survive the rollback: $gateway_status"
vless_works || fail 'VLESS traffic does not pass after the rollback.'
fallback_ready
vless_works 8443 || fail 'fallback VLESS traffic does not pass after the rollback.'

printf '\n== Update to %s, which sends VPN clients to Caddy ==\n' "$version_d"
if deliver "$version_d" "$work/release-d.tar.gz" 2>&1 | tee "$work/update-d.log"; then
  fail 'the updater accepted a release whose HAProxy routing breaks VLESS.'
fi
grep -q '^Docker deployment stopped: VLESS traffic does not pass through the new release.' "$work/update-d.log" \
  || fail 'the VLESS check did not stop the update.'
grep -q '^VLESS traffic passes again with the previous release.' "$work/update-d.log" \
  || fail 'the rollback did not bring VLESS traffic back.'
[[ $(installed) == "$version_b" ]] || fail 'the rollback did not restore the installed version.'
sudo cmp -s /etc/private-gateway-docker/haproxy.cfg "$work/haproxy.cfg" \
  || fail 'the rollback did not restore the HAProxy configuration.'
panel_ready || fail 'the panel does not answer after the VLESS rollback.'
vless_works || fail 'VLESS traffic does not pass after the VLESS rollback.'
[[ $(container xray) == "$xray_container" ]] || fail 'Xray was recreated although its image never changed.'

# Проверить перенос порта и откат с установщиком подготовительного релиза.
printf '\n== Restore legacy ownership of port 8443 ==\n'
deliver "$version_e" "$work/release-e.tar.gz" 2>&1 | tee "$work/update-e.log"
[[ $(installed) == "$version_e" ]] || fail 'legacy topology was not installed.'
legacy_redirect
vless_works || fail 'primary VLESS failed with legacy port ownership.'
if deliver "$version_c" "$work/release-c.tar.gz" 2>&1 | tee "$work/transfer-failed.log"; then
  fail 'the updater accepted broken routing during port transfer.'
fi
[[ $(installed) == "$version_e" ]] || fail 'port transfer rollback lost the previous release.'
panel_ready || fail 'primary panel failed after port transfer rollback.'
legacy_redirect
vless_works || fail 'primary VLESS failed after port transfer rollback.'
printf '\n== Transfer port 8443 from Caddy to HAProxy ==\n'
deliver "$version_f" "$work/release-f.tar.gz" 2>&1 | tee "$work/update-f.log"
[[ $(installed) == "$version_f" ]] || fail 'fallback topology was not installed.'
panel_ready || fail 'primary panel failed after port transfer.'
fallback_ready
vless_works || fail 'primary VLESS failed after port transfer.'
vless_works 8443 || fail 'fallback VLESS failed after port transfer.'
gateway_status=$(compose exec -T api /usr/local/bin/gatewayctl status)
grep -Eq '^ACTIVE DEVICES +1$' <<< "$gateway_status" || fail 'port transfer lost the device.'
printf '\n== Reject a release that loses only the fallback port ==\n'
if deliver "$version_g" "$work/release-1.tar.gz" 2>&1 | tee "$work/fallback-failed.log"; then
  fail 'the updater accepted a release with an unavailable fallback port.'
fi
grep -q '^Docker deployment stopped: Fallback HTTPS readiness check failed.' "$work/fallback-failed.log" \
  || fail 'the fallback readiness check did not stop the update.'
[[ $(installed) == "$version_f" ]] || fail 'fallback failure did not restore the installed release.'
panel_ready || fail 'primary panel failed after fallback rollback.'
fallback_ready
vless_works || fail 'primary VLESS failed after fallback rollback.'
vless_works 8443 || fail 'fallback VLESS failed after fallback rollback.'
printf '\nInstallation, update and rollback passed on %s.\n' "$(. /etc/os-release && printf '%s' "$PRETTY_NAME")"
