#!/usr/bin/env bash
# Install, update and roll back the production stack on this machine the way a VPS gets it:
# bootstrap.sh, then CI deliveries through the forced command of the deploy account.
# It takes ports 80, 443 and 8443 and writes to /etc, /opt and /var: disposable CI runners only.
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
# A failed installation removes its containers and an update replaces them: keep every
# container's log as it runs, one file per container.
collect_logs() {
  local id service status last=''
  while :; do
    # The Xray worker's status (config hash, running, error) holds no secrets.
    status=$(sudo cat /var/lib/private-gateway-docker/xray/xray.status.json 2>/dev/null) || status=''
    if [[ -n $status && $status != "$last" ]]; then
      printf '%s %s\n' "$(date -u +%T)" "$status" >> "$work/logs/xray-status.log"
      last=$status
    fi
    for id in $(docker ps --all --quiet --filter label=com.docker.compose.project=private-gateway); do
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
    kill "$collector" 2>/dev/null || true
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
# CI's deploy step sends the SHA and the archive over SSH to this forced command of gateway-deploy.
deliver() {
  (cd / && { printf '%s\n' "$1"; cat "$2"; } \
    | sudo -u gateway-deploy sudo -n /usr/local/sbin/private-gateway-docker-deploy)
}
installed() { sudo cat /etc/private-gateway-docker/installed.version; }
container() { compose ps --status running --quiet "$1"; }
panel_ready() {
  curl --fail --silent --show-error --max-time 10 --output /dev/null \
    --resolve "$domain:443:127.0.0.1" "https://$domain/readyz"
}
# A recreated Xray gets a new address; HAProxy picks it up from Docker DNS within seconds.
vless_works() {
  local attempt
  for attempt in 1 2 3; do
    if sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py; then return 0; fi
    ((attempt == 3)) || sleep 5
  done
  return 1
}

version_a=$(python3 "$repository/scripts/verify-release.py" --tar "$archive")
version_b=$(printf 'b%.0s' {1..40})
version_c=$(printf 'c%.0s' {1..40})
# B is the same build under another version. C sends the panel name to Xray instead of Caddy:
# the panel check fails only if the updater really restarts HAProxy, and then it must roll back.
python3 - "$archive" "$work" "$version_b" "$version_c" <<'PY'
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
template = files["docker/haproxy.cfg.template"].decode()
if route not in template:
    raise SystemExit("The panel route is missing from the HAProxy template.")
write(sys.argv[3], {})
write(sys.argv[4], {"docker/haproxy.cfg.template":
                    template.replace(route, route.replace("panel_https", "vpn_reality")).encode()})
PY

# Caddy signs .localhost names with its local CA. The test gives Caddy a CA this runner trusts,
# so the installer and the updater check the panel with full TLS verification, as on a VPS.
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
# The same bundle scripts/add-server.sh uploads.
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
redirect=$(curl --silent --output /dev/null --write-out '%{http_code} %{redirect_url}' --max-time 10 \
  --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz")
[[ $redirect == "301 https://$domain/readyz" ]] || fail "port 8443 answered '$redirect' instead of a redirect."
node=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "NODE" {print $2}')
compose exec -T api /usr/local/bin/gatewayctl device add installation-test --node "$node" >/dev/null
compose exec -T api /usr/local/bin/gatewayctl config apply
vless_works || fail 'VLESS traffic does not pass after the first deployment.'

printf '\n== Update to %s ==\n' "$version_b"
declare -A before
for service in postgres caddy edge; do before[$service]=$(container "$service"); done
deliver "$version_b" "$work/release-b.tar.gz" 2>&1 | tee "$work/update-b.log"
grep -q "^Docker deployment verified: $version_b\. " "$work/update-b.log" \
  || fail 'the update did not report success.'
[[ $(installed) == "$version_b" ]] || fail 'the update did not switch the installed version.'
for service in postgres caddy edge; do
  [[ $(container "$service") == "${before[$service]}" ]] \
    || fail "$service was recreated although its image and configuration did not change."
done
panel_ready || fail 'the panel does not answer after the update.'
vless_works || fail 'VLESS traffic does not pass after the update.'
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
compose exec -T api /usr/local/bin/gatewayctl status | grep -Eq '^ACTIVE DEVICES +1$' \
  || fail 'the device did not survive the rollback.'
vless_works || fail 'VLESS traffic does not pass after the rollback.'
printf '\nInstallation, update and rollback passed on %s.\n' "$(. /etc/os-release && printf '%s' "$PRETTY_NAME")"
