#!/usr/bin/env bash
# Только одноразовый runner CI: настоящий релиз 8d73025 с общим Xray/XHTTP -> новый split worker.
set -Eeuo pipefail
umask 077

fail() { printf 'Legacy upgrade check failed: %s\n' "$*" >&2; exit 1; }
[[ ${GITHUB_ACTIONS:-} == true && $EUID != 0 && $# == 3 ]] || \
  fail 'Use an unprivileged account on a disposable GitHub Actions runner with OLD_ARCHIVE NEW_ARCHIVE OLD_SOURCE.'
old_archive=$(realpath -- "$1")
new_archive=$(realpath -- "$2")
old_source=$(realpath -- "$3")
repository=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
[[ -f $old_archive && -f $new_archive && -d $old_source ]] || fail 'Release input is missing.'
[[ ! -e /etc/private-gateway-docker/stack.env ]] || fail 'A Docker installation already exists.'
[[ -z $(sudo docker ps --all --quiet --filter label=com.docker.compose.project=private-gateway) ]] \
  || fail 'The Docker project already exists.'
if sudo docker volume inspect private-gateway_postgres-data >/dev/null 2>&1; then
  fail 'A Docker database volume already exists.'
fi

domain=panel.gateway.localhost
env_file=/etc/private-gateway-docker/stack.env
work=$(mktemp -d)
upgrade_bundle=''
cleanup() {
  if [[ -n $upgrade_bundle ]]; then sudo rm -rf -- "$upgrade_bundle"; fi
  rm -rf -- "$work"
}
trap cleanup EXIT

# Проверить исходные CI-артефакты до изменения единственного Caddyfile в локальной копии.
for source in "$old_archive" "$new_archive"; do
  checksum=${source%/*}/release.tar.gz.sha256
  [[ ${source##*/} == release.tar.gz && -f $checksum ]] || fail 'A release checksum is missing.'
  (cd -- "${source%/*}" && sha256sum --check --status release.tar.gz.sha256) \
    || fail 'A release checksum does not match.'
done
old_version=$(python3 "$old_source/scripts/verify-release.py" --tar "$old_archive")
new_version=$(python3 "$repository/scripts/verify-release.py" --tar "$new_archive")
[[ $old_version == "$(git -C "$old_source" rev-parse HEAD)" && \
   $new_version == "$(git -C "$repository" rev-parse HEAD)" && \
   $old_version == 8d73025eae365e426e0d2080d9f81f15ec267b10 && \
   $old_version != "$new_version" ]] \
  || fail 'The source checkouts and release versions disagree.'

# Ни один production-файл не меняется: тестовый сертификат живёт только на runner.
python3 - "$old_archive" "$work/old" "$new_archive" "$work/new" "$work/broken" <<'PY'
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile

for source_name, output_name, broken in ((sys.argv[1], sys.argv[2], False),
                                         (sys.argv[3], sys.argv[4], False),
                                         (sys.argv[3], sys.argv[5], True)):
    source, output = Path(source_name), Path(output_name)
    output.mkdir(mode=0o700)
    with tarfile.open(source, 'r:gz') as original:
        files = {member.name: original.extractfile(member).read()
                 for member in original if member.isfile() and member.name != 'manifest.json'}
        manifest = json.load(original.extractfile('manifest.json'))
    template = files['docker/Caddyfile.template'].decode()
    changed, count = re.subn(
        r'(?m)^(\s*)tls \{\n\s*issuer acme \{\n\s*disable_tlsalpn_challenge\n\s*\}\n\s*\}',
        r'\1tls internal', template)
    if count != 1:
        raise SystemExit('Expected exactly one Caddy ACME stanza')
    files['docker/Caddyfile.template'] = changed.encode()
    if broken:
        # Docker вызывает python3 явно; падает только второй worker после переноса порта8444.
        script = files['docker/xray-worker.py'].decode()
        header, rest = script.split('\n', 1)
        files['docker/xray-worker.py'] = (header + '\nimport os\n'
            'if os.environ.get("XRAY_TRANSPORT") == "xhttp":\n'
            '    raise SystemExit(42)\n' + rest).encode()
        manifest['version'] = 'c' * 40
    hashes = {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}
    document = {**manifest, 'files': hashes}
    entries = {'manifest.json': json.dumps(document, sort_keys=True, separators=(',', ':')).encode() + b'\n',
               **files}
    archive_path = output / 'release.tar.gz'
    with archive_path.open('wb') as raw, gzip.GzipFile(filename='', fileobj=raw, mode='wb', mtime=0) as zipped, \
            tarfile.open(fileobj=zipped, mode='w', format=tarfile.USTAR_FORMAT) as destination:
        for name, data in sorted(entries.items()):
            item = tarfile.TarInfo(name)
            item.size = len(data)
            item.mode = 0o755 if name.startswith('bin/') else 0o644
            destination.addfile(item, io.BytesIO(data))
    digest = hashlib.sha256(archive_path.read_bytes()).hexdigest()
    (output / 'release.tar.gz.sha256').write_text(f'{digest}  release.tar.gz\n')
PY
python3 "$old_source/scripts/verify-release.py" --tar "$work/old/release.tar.gz" >/dev/null
python3 "$repository/scripts/verify-release.py" --tar "$work/new/release.tar.gz" >/dev/null
python3 "$repository/scripts/verify-release.py" --tar "$work/broken/release.tar.gz" >/dev/null

compose() {
  local base=/opt/private-gateway-docker/current/docker files=()
  files=(-f "$base/compose.yml")
  if sudo test -f "$base/compose.xhttp.yml" && sudo grep -qx 'GATEWAY_XHTTP_ENABLED=1' "$env_file"; then
    files+=(-f "$base/compose.xhttp.yml")
  fi
  if sudo test -f "$base/compose.xhttp-legacy.yml" && sudo grep -qx 'GATEWAY_XHTTP_ENABLED=1' "$env_file"; then
    files+=(-f "$base/compose.xhttp-legacy.yml")
  fi
  sudo docker compose --env-file "$env_file" "${files[@]}" "$@"
}
deliver() {
  (cd / && { printf '%s\n' "$1"; cat "$2"; } \
    | sudo -u gateway-deploy sudo -n /usr/local/sbin/private-gateway-docker-deploy)
}
smoke() {
  local port=$1 transport=${2:-tcp} attempt
  for attempt in 1 2 3; do
    if sudo python3 /opt/private-gateway-docker/current/docker/vpn-smoke.py \
        --transport "$transport" --port "$port"; then return 0; fi
    ((attempt == 3)) || sleep 5
  done
  return 1
}
panel_ready() {
  [[ $(curl --noproxy '*' --silent --show-error --output /dev/null --max-time 10 \
      --write-out '%{http_code} %{ssl_verify_result}' \
      --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/readyz") == '200 0' ]]
}
identity() {
  sudo python3 - "$env_file" <<'PY'
import hashlib
from pathlib import Path
import sys
values = dict(line.split('=', 1) for line in Path(sys.argv[1]).read_text().splitlines() if '=' in line)
names = ('GATEWAY_MASTER_KEY', 'GATEWAY_REALITY_PRIVATE_KEY', 'GATEWAY_SHORT_ID',
         'GATEWAY_API_TOKEN', 'POSTGRES_PASSWORD', 'BROWSER_WORKER_TOKEN',
         'GATEWAY_XHTTP_PRIVATE_KEY', 'GATEWAY_XHTTP_SHORT_ID', 'GATEWAY_XHTTP_PATH')
print(hashlib.sha256('\n'.join(values[name] for name in names).encode()).hexdigest())
PY
  compose exec -T postgres psql -At -U gateway -d gateway -c \
    'SELECT id, node_id, name, encode(credential_ciphertext, '\''hex'\''), subscription_generation, encode(subscription_hash, '\''hex'\''), revoked_at FROM devices ORDER BY id' \
    | sha256sum | cut -d' ' -f1
}
proxy_settings_hash() {
  sudo sed -n '/^GATEWAY_PROXY_ENABLED=/!{/^GATEWAY_PROXY_/p;}' "$env_file" \
    | sha256sum | cut -d' ' -f1
}
profile_digest() {
  compose exec -T api python3 - "$direct_device" <<'PY'
import hashlib
import json
import os
import sys
import urllib.request
request = urllib.request.Request('http://127.0.0.1:8080/api/v1/devices/' + sys.argv[1] + '/profile',
    headers={'Authorization':'Bearer ' + os.environ['GATEWAY_API_TOKEN']})
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(request, timeout=5) as response:
    profile = json.load(response)
data = (profile['uri'] + '\n' + profile['xhttp_uri']).encode()
print(hashlib.sha256(data).hexdigest())
PY
}
config_shape() {
  sudo python3 - "$1" <<'PY'
import json
from pathlib import Path
import sys
mode = sys.argv[1]
state = Path('/var/lib/private-gateway-docker/xray')
tcp = json.loads((state / 'xray.json').read_text())
def ports(config):
    return {(item.get('tag'), item.get('port'), item.get('streamSettings', {}).get('network'))
            for item in config['inbounds']}
def count(config, tag):
    return len(next(item for item in config['inbounds'] if item.get('tag') == tag)['settings']['clients'])
if mode == 'old':
    assert not (state / 'xhttp.json').exists(), 'Old release unexpectedly has split XHTTP config'
    assert ('vless-reality', 443, 'raw') in ports(tcp)
    assert ('vless-xhttp-reality', 8444, 'xhttp') in ports(tcp)
    assert count(tcp, 'vless-reality') == count(tcp, 'vless-xhttp-reality') == 4
    assert {item.get('protocol') for item in tcp['outbounds']} >= {'socks', 'freedom'}
else:
    extra = json.loads((state / 'xhttp.json').read_text())
    assert ('vless-reality', 443, 'raw') in ports(tcp)
    assert ('vless-xhttp-reality', 8444, 'xhttp') not in ports(tcp)
    assert ('vless-xhttp-reality', 8444, 'xhttp') in ports(extra)
    assert count(tcp, 'vless-reality') == count(extra, 'vless-xhttp-reality') == 2
    assert not any(item.get('protocol') == 'socks' for item in tcp['outbounds'] + extra['outbounds'])
print('Xray configuration structure OK:', mode)
PY
}

# Доверенный локальный CA нужен для настоящей TLS-проверки Caddy.
cat > "$work/ca.cnf" <<'CA'
[req]
distinguished_name = name
x509_extensions = ca
prompt = no
[name]
CN = Private Gateway legacy upgrade test CA
[ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
CA
openssl req -x509 -config "$work/ca.cnf" -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -noenc \
  -days 30 -keyout "$work/root.key" -out "$work/root.crt" >/dev/null 2>&1
sudo install -m 0644 "$work/root.crt" /usr/local/share/ca-certificates/private-gateway-legacy-test.crt
sudo update-ca-certificates >/dev/null
caddy_image=$(sed -n 's/^ *image: \(caddy:[^ ]*\)$/\1/p' "$old_source/deploy/docker/compose.yml")
sudo docker volume create private-gateway_caddy-data >/dev/null
sudo docker run --rm --network none --entrypoint sh -v private-gateway_caddy-data:/data \
  -v "$work/root.crt:/ca/root.crt:ro" -v "$work/root.key:/ca/root.key:ro" "$caddy_image" -c \
  'install -d -m 0700 /data/caddy/pki/authorities/local && install -m 0600 /ca/root.crt /ca/root.key /data/caddy/pki/authorities/local/'
printf '127.0.0.1 %s\n' "$domain" | sudo tee -a /etc/hosts >/dev/null

# Настоящий старый bootstrap устанавливает старый receiver, verifier, updater и API.
bundle=$work/old-bundle
mkdir -m 0700 "$bundle"
cp "$old_source/deploy/docker/bootstrap.sh" "$old_source/deploy/docker/bootstrap-receiver.sh" \
  "$old_source/scripts/verify-release.py" "$old_source/deploy/servers/deploy-key.pub" "$bundle/"
printf '%s\n' "$old_version" > "$bundle/commit.sha"
(cd "$bundle" && sha256sum bootstrap.sh bootstrap-receiver.sh verify-release.py commit.sha > SHA256SUMS)
sudo bash "$bundle/bootstrap.sh" --ref "$old_version" --host 127.0.0.1 --domain "$domain" \
  --deploy-key "$bundle/deploy-key.pub"
deliver "$old_version" "$work/old/release.tar.gz"
[[ $(sudo cat /etc/private-gateway-docker/installed.version) == "$old_version" ]] \
  || fail 'The old release was not installed.'
panel_ready || fail 'The old HTTPS panel is unavailable.'
node=$(compose exec -T api /usr/local/bin/gatewayctl status | awk '$1 == "NODE" {print $2}')
[[ $node =~ ^[0-9a-f-]{36}$ ]] || fail 'Old CLI did not report a node ID.'
direct_device=$(compose exec -T api /usr/local/bin/gatewayctl device add legacy-direct --node "$node")
proxy_device=$(compose exec -T api /usr/local/bin/gatewayctl device add legacy-proxy --node "$node")
[[ $direct_device =~ ^[0-9a-f-]{36}$ && $proxy_device =~ ^[0-9a-f-]{36}$ ]] \
  || fail 'Old CLI did not create two devices.'
compose exec -T api /usr/local/bin/gatewayctl config apply
smoke 443 || fail 'Old TCP 443 smoke failed.'
smoke 8443 || fail 'Old TCP 8443 smoke failed.'
sudo env GITHUB_ACTIONS=true python3 /opt/private-gateway-docker/current/docker/configure-xhttp.py \
  --enable --port 8444 --apply
smoke 8444 xhttp || fail 'Old XHTTP 8444 smoke failed.'

# Фиктивный SOCKS нужен только для старой конфигурации; его соединение не проверяется.
sudo python3 - "$env_file" <<'PY'
import os
from pathlib import Path
import sys
import tempfile
path = Path(sys.argv[1])
values = {'GATEWAY_PROXY_ENABLED':'1', 'GATEWAY_PROXY_HOST':'127.0.0.1',
          'GATEWAY_PROXY_PORT':'9', 'GATEWAY_PROXY_AUTH':'password',
          'GATEWAY_PROXY_USERNAME_B64':'bGVnYWN5LXRlc3Q=',
          'GATEWAY_PROXY_PASSWORD_B64':'bGVnYWN5LXRlc3Q='}
lines = path.read_text().splitlines()
for name, value in values.items():
    lines = [line for line in lines if not line.startswith(name + '=')]
    lines.append(name + '=' + value)
with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, prefix='.legacy-', delete=False) as output:
    output.write('\n'.join(lines) + '\n')
    staged = output.name
os.chmod(staged, 0o600)
os.replace(staged, path)
PY
compose up -d --wait --no-deps api
compose exec -T api /usr/local/bin/gatewayctl config apply
compose exec -T api python3 - "$proxy_device" <<'PY'
import json
import os
import sys
import urllib.request
request = urllib.request.Request('http://127.0.0.1:8080/api/v1/devices/' + sys.argv[1],
    data=b'{"preferred_exit":"proxy"}', method='PATCH',
    headers={'Authorization':'Bearer ' + os.environ['GATEWAY_API_TOKEN'],
             'Content-Type':'application/json'})
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(request, timeout=5) as response:
    assert json.load(response)['preferred_exit'] == 'proxy'
PY
smoke 443 || fail 'Old direct TCP failed with SOCKS exit enabled.'
smoke 8444 xhttp || fail 'Old direct XHTTP failed with SOCKS exit enabled.'
config_shape old
before_identity=$(identity)
before_proxy_hash=$(proxy_settings_hash)
before_profile=$(profile_digest)
[[ $(compose exec -T postgres psql -At -U gateway -d gateway -c \
    "SELECT count(*) FROM devices WHERE preferred_exit='proxy'") == 1 ]] \
  || fail 'Old proxy preference was not persisted.'

# Старый verifier отвергает новый архив из-за изменённой структуры; переход делает
# проверенный root bootstrap, который временно ставит новые helper/verifier под общим lock.
if sudo python3 /usr/local/lib/private-gateway-docker/verify-release.py \
    --tar "$work/new/release.tar.gz" >/dev/null 2>&1; then
  fail 'Old verifier unexpectedly accepted the new release.'
fi
upgrade_candidate() {
  local archive=$1 version=$2 outcome=$3 digest
  upgrade_bundle=$(sudo mktemp -d /root/private-gateway-upgrade-test.XXXXXXXX)
  sudo chmod 0700 "$upgrade_bundle"
  sudo install -m 0600 "$archive" "$upgrade_bundle/release.tar.gz"
  tar -xOf "$archive" docker/verify-release.py \
    | sudo tee "$upgrade_bundle/verify-release.py" >/dev/null
  tar -xOf "$archive" docker/upgrade-updater.sh \
    | sudo tee "$upgrade_bundle/upgrade-updater.sh" >/dev/null
  sudo chmod 0600 "$upgrade_bundle/verify-release.py"
  sudo chmod 0700 "$upgrade_bundle/upgrade-updater.sh"
  digest=$(sha256sum "$archive" | cut -d' ' -f1)
  if [[ $outcome == fail ]]; then
    if sudo bash "$upgrade_bundle/upgrade-updater.sh" "$upgrade_bundle" "$version" "$digest" \
        2>&1 | tee "$work/broken-upgrade.log"; then
      fail 'A release with a broken split XHTTP worker was accepted.'
    fi
    grep -Fq 'Deployment failed. Restoring the previous database and release.' \
      "$work/broken-upgrade.log" || fail 'The broken release did not reach updater rollback.'
    grep -Fq 'Old helpers restored after failed deployment.' \
      "$work/broken-upgrade.log" || fail 'The bootstrap did not restore old helpers.'
    grep -Fq 'xray-xhttp' "$work/broken-upgrade.log" \
      || fail 'The broken release did not attempt the split XHTTP worker.'
  else
    sudo bash "$upgrade_bundle/upgrade-updater.sh" "$upgrade_bundle" "$version" "$digest"
  fi
  sudo rm -rf -- "$upgrade_bundle"
  upgrade_bundle=''
}

printf '== Broken split worker must roll back to the actual installed release ==\n'
upgrade_candidate "$work/broken/release.tar.gz" "$(printf 'c%.0s' {1..40})" fail
[[ $(sudo cat /etc/private-gateway-docker/installed.version) == "$old_version" ]] \
  || fail 'The broken release changed the installed version.'
sudo cmp -s /usr/local/sbin/private-gateway-docker-deploy \
  /opt/private-gateway-docker/current/docker/update.sh || fail 'Old updater was not restored.'
sudo cmp -s /usr/local/lib/private-gateway-docker/verify-release.py \
  /opt/private-gateway-docker/current/docker/verify-release.py || fail 'Old verifier was not restored.'
sudo grep -qx 'GATEWAY_PROXY_ENABLED=1' "$env_file" || fail 'Old proxy setting was not restored.'
[[ $(proxy_settings_hash) == "$before_proxy_hash" && $(identity) == "$before_identity" ]] \
  || fail 'Rollback changed credentials or device records.'
[[ $(profile_digest) == "$before_profile" ]] || fail 'Rollback changed the direct profile.'
[[ $(compose exec -T postgres psql -At -U gateway -d gateway -c \
    "SELECT count(*) FROM devices WHERE preferred_exit='proxy'") == 1 ]] \
  || fail 'Rollback did not restore the proxy preference.'
[[ -z $(sudo docker ps --quiet --filter label=com.docker.compose.project=private-gateway \
    --filter label=com.docker.compose.service=xray-xhttp) ]] \
  || fail 'Split XHTTP worker remained running after rollback.'
config_shape old
panel_ready || fail 'Old HTTPS panel was not restored.'
smoke 443 || fail 'Old TCP 443 failed after rollback.'
smoke 8443 || fail 'Old TCP 8443 failed after rollback.'
smoke 8444 xhttp || fail 'Old XHTTP 8444 failed after rollback.'

printf '== Upgrade the same installed release using the verified candidate ==\n'
upgrade_candidate "$work/new/release.tar.gz" "$new_version" pass
[[ $(sudo cat /etc/private-gateway-docker/installed.version) == "$new_version" ]] \
  || fail 'The new release was not installed.'
sudo cmp -s /usr/local/sbin/private-gateway-docker-deploy \
  /opt/private-gateway-docker/current/docker/update.sh || fail 'Installed updater differs from current release.'
sudo cmp -s /usr/local/lib/private-gateway-docker/verify-release.py \
  /opt/private-gateway-docker/current/docker/verify-release.py || fail 'Installed verifier differs from current release.'

panel_ready || fail 'New HTTPS panel is unavailable.'
smoke 443 || fail 'New TCP 443 smoke failed.'
smoke 8443 || fail 'New TCP 8443 smoke failed.'
smoke 8444 xhttp || fail 'New XHTTP 8444 smoke failed.'
config_shape new
[[ $(identity) == "$before_identity" ]] || fail 'Device credentials or private keys changed.'
[[ $(proxy_settings_hash) == "$before_proxy_hash" ]] || fail 'Stored provider credentials changed.'
[[ $(profile_digest) == "$before_profile" ]] || fail 'The direct TCP/XHTTP profile identity changed.'
sudo grep -qx 'GATEWAY_PROXY_ENABLED=0' "$env_file" || fail 'Server SOCKS exit was not disabled.'
[[ $(compose exec -T postgres psql -At -U gateway -d gateway -c \
    "SELECT count(*) FROM devices WHERE preferred_exit='proxy'") == 0 ]] \
  || fail 'Legacy preferred exit was not migrated.'
compose exec -T api python3 - "$proxy_device" <<'PY'
import os
import sys
import urllib.error
import urllib.request
request = urllib.request.Request(
    'http://127.0.0.1:8080/api/v1/devices/' + sys.argv[1] + '/profile?exit=proxy',
    headers={'Authorization':'Bearer ' + os.environ['GATEWAY_API_TOKEN']})
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
    opener.open(request, timeout=5).close()
except urllib.error.HTTPError as error:
    assert error.code in (400, 410), 'Old server exit returned unexpected status'
else:
    raise SystemExit('Old server exit profile is still available')
PY
status=$(compose exec -T api /usr/local/bin/gatewayctl status)
desired=$(awk '$1 == "DESIRED" && $2 == "REVISION" {print $3}' <<< "$status")
applied=$(awk '$1 == "APPLIED" && $2 == "REVISION" {print $3}' <<< "$status")
[[ $desired =~ ^[0-9]+$ && $desired == "$applied" ]] \
  || fail 'New Xray revision was not applied.'
compose ps --status running xray-xhttp | grep -q xray-xhttp \
  || fail 'The split XHTTP worker is not running.'
printf 'Actual 8d73025 to split XHTTP release upgrade passed: HTTPS, TCP, XHTTP, DB and credentials.\n'
