#!/usr/bin/env bash
# First installation on an otherwise empty Ubuntu VPS. Existing data is never reset.
set -Eeuo pipefail
umask 077

usage() {
  cat <<'EOF'
Usage: sudo bash install/install.sh --host IP_OR_DNS [options]

  --host HOST          Public IPv4 address or DNS name of this server (required)
  --port PORT          Xray TCP port (default: 443)
  --server-name NAME   REALITY TLS server name (default: www.microsoft.com)
  --target HOST:PORT   REALITY TLS target (default: www.microsoft.com:443)
  --archive FILE       Release archive (default: ../release.tar.gz beside install/)
  --deploy-key FILE    Optional dedicated GitHub Actions public Ed25519 key
  --resume             Continue an installation stopped after browser worker setup
  --help               Show this help without changing the server

Requires Ubuntu 26.04 amd64, systemd, Python 3, and an unused server.
Installs PostgreSQL 18, the API, Xray and a separate rootless browser worker.
Does not change SSH, firewall rules, or existing applications.
EOF
}

fail() { printf 'Installation stopped: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
archive="$script_dir/../release.tar.gz"
gateway_host=''
gateway_port=443
server_name=www.microsoft.com
target=www.microsoft.com:443
deploy_key=''
resume=false
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --resume) resume=true; shift ;;
    --host|--port|--server-name|--target|--archive|--deploy-key)
      (($# >= 2)) || fail "Missing value after $1"
      case "$1" in
        --host) gateway_host=$2 ;;
        --port) gateway_port=$2 ;;
        --server-name) server_name=$2 ;;
        --target) target=$2 ;;
        --archive) archive=$2 ;;
        --deploy-key) deploy_key=$2 ;;
      esac
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done

[[ -n "$gateway_host" ]] || fail '--host is required; see --help'
[[ $EUID == 0 ]] || fail 'Run this installer using sudo.'
command -v python3 >/dev/null || fail 'Python 3 is required before installation.'
[[ -f "$archive" ]] || fail "Release archive not found: $archive"
for name in private-gateway-deploy.py gateway-api.service private-gateway-xray.service 49-private-gateway-xray.rules; do
  [[ -f "$script_dir/$name" && ! -L "$script_dir/$name" ]] || fail "Missing installation file: $name"
done
exec 9>/run/lock/private-gateway-install.lock
flock -n 9 || fail 'Another installation is already running.'

trap 'printf "Installation did not finish (line %s). Existing database, keys and files have been preserved. Do not delete them or rerun blindly; inspect the failed command first.\n" "$LINENO" >&2' ERR

if [[ $resume == true ]]; then
  # This path is deliberately narrow: it cannot create or overwrite credentials.
  for account in gateway gateway-browser; do
    getent passwd "$account" >/dev/null || fail "Resume requires the existing $account account."
  done
  if getent passwd gateway-deploy >/dev/null; then
    fail 'Deployment account already exists; inspect the later installation failure before resuming.'
  fi
  for path in /etc/private-gateway/gateway.env /etc/systemd/system/gateway-api.service \
    /etc/systemd/system/private-gateway-xray.service \
    /etc/tmpfiles.d/private-gateway-browser.conf \
    /home/gateway-browser/.config/systemd/user/private-gateway-browser-worker.socket \
    /home/gateway-browser/.config/systemd/user/private-gateway-browser-worker.service; do
    [[ -f "$path" && ! -L "$path" ]] || fail "Resume requires $path."
  done
  [[ ! -e /etc/sudoers.d/private-gateway-deploy && ! -e /var/lib/private-gateway-deploy/.ssh/authorized_keys ]] || fail 'Deployment setup already started; inspect it before resuming.'
  [[ -L /opt/private-gateway/current ]] || fail 'Resume requires the existing release symlink.'
  release=$(readlink -f /opt/private-gateway/current)
  [[ "$release" =~ ^/opt/private-gateway/releases/[0-9a-f]{40}$ && -x "$release/bin/gateway-api" && -f "$release/browser/Containerfile" ]] || fail 'Existing release is invalid; inspect it before resuming.'
  python3 - "$gateway_host" "$gateway_port" "$deploy_key" <<'PY'
import base64
import pathlib
import struct
import sys

host, port, keyfile = sys.argv[1:]
values = dict(line.split('=', 1) for line in pathlib.Path('/etc/private-gateway/gateway.env').read_text().splitlines() if '=' in line)
if values.get('GATEWAY_HOST', '').strip("'\"") != host or values.get('GATEWAY_PORT', '').strip("'\"") != port:
    raise SystemExit('Resume host/port differ from the preserved server configuration.')
if keyfile:
    fields = pathlib.Path(keyfile).read_text().strip().split()
    try:
        blob = base64.b64decode(fields[1], validate=True)
        valid = fields[0] == 'ssh-ed25519' and blob[:19] == struct.pack('>I', 11) + b'ssh-ed25519' + struct.pack('>I', 32) and len(blob) == 51
    except (ValueError, IndexError):
        valid = False
    if not valid:
        raise SystemExit('Resume deploy key must be an OpenSSH public Ed25519 key.')
PY
  worker_uid=$(id -u gateway-browser)
  systemctl is-active --quiet postgresql.service "user@$worker_uid.service" || fail 'PostgreSQL and the browser user manager must be active before resuming.'
  if systemctl is-active --quiet gateway-api.service || systemctl is-active --quiet private-gateway-xray.service; then
    fail 'Application services already started; inspect the later installation failure before resuming.'
  fi
  printf 'Validated interrupted installation; continuing with browser worker setup.\n'
else
# Validate arguments, the host and the target before package/service changes.
python3 - "$gateway_host" "$gateway_port" "$server_name" "$target" "$deploy_key" <<'PY'
import base64
import ipaddress
import os
import pathlib
import platform
import re
import socket
import ssl
import struct
import sys

def fail(message):
    raise SystemExit('Preflight: ' + message)

def hostname(value, allow_ip=True):
    try:
        address = ipaddress.ip_address(value)
        return allow_ip and address.version == 4 and not address.is_unspecified
    except ValueError:
        return len(value) <= 253 and all(re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?', label) for label in value.split('.'))

host, port, server_name, target, keyfile = sys.argv[1:]
if not hostname(host) or not hostname(server_name, False):
    fail('Host must be an IPv4 address or DNS name; server-name must be a DNS name.')
if not re.fullmatch(r'[0-9]{1,5}', port) or not 1 <= int(port) <= 65535 or int(port) in (22, 53, 5432, 6080, 8080):
    fail('Choose an unused Xray port from 1 to 65535, distinct from SSH, DNS, PostgreSQL, API and browser ports.')
parts = target.rsplit(':', 1)
if len(parts) != 2 or not hostname(parts[0]) or not parts[1].isdigit() or not 1 <= int(parts[1]) <= 65535:
    fail('Target must be an IPv4 address or DNS name followed by :PORT.')
os_release = platform.freedesktop_os_release()
if os_release.get('ID') != 'ubuntu' or os_release.get('VERSION_ID') != '26.04' or platform.machine() != 'x86_64':
    fail('This installer supports Ubuntu 26.04 amd64 only.')
if pathlib.Path('/proc/1/comm').read_text().strip() != 'systemd':
    fail('A running systemd host is required; do not run this installer inside a container.')
if not pathlib.Path('/sys/fs/cgroup/cgroup.controllers').is_file():
    fail('The rootless browser requires cgroup v2.')
for path in (
    '/etc/private-gateway/gateway.env', '/opt/private-gateway/current',
    '/usr/local/sbin/private-gateway-deploy',
    '/etc/systemd/system/gateway-api.service',
    '/etc/systemd/system/private-gateway-xray.service',
    '/etc/polkit-1/rules.d/49-private-gateway-xray.rules',
    '/etc/tmpfiles.d/private-gateway-browser.conf',
    '/etc/sudoers.d/private-gateway-deploy',
):
    if os.path.lexists(path):
        fail(f'{path} already exists. Use the deployment updater for an installed server; preserve existing keys and data.')
for bind_host, bind_port in [('0.0.0.0', int(port)), ('127.0.0.1', 5432), ('127.0.0.1', 8080), ('127.0.0.1', 6080)]:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        try:
            listener.bind((bind_host, bind_port))
        except OSError:
            fail(f'TCP {bind_host}:{bind_port} is already in use.')
free = os.statvfs('/var')
if free.f_bavail * free.f_frsize < 8 * 1024**3:
    fail('At least 8 GiB of free space is required for packages, releases and the browser image.')
if keyfile:
    content = pathlib.Path(keyfile).read_text()
    if len(content) > 4096 or len(content.strip().splitlines()) != 1:
        fail('Deploy key must contain exactly one public Ed25519 key.')
    fields = content.strip().split()
    try:
        blob = base64.b64decode(fields[1], validate=True)
        valid = fields[0] == 'ssh-ed25519' and blob[:19] == struct.pack('>I', 11) + b'ssh-ed25519' + struct.pack('>I', 32) and len(blob) == 51
    except (ValueError, IndexError):
        valid = False
    if not valid:
        fail('Deploy key must be an OpenSSH public Ed25519 key, never a private key.')
context = ssl.create_default_context()
context.minimum_version = ssl.TLSVersion.TLSv1_3
context.maximum_version = ssl.TLSVersion.TLSv1_3
context.set_alpn_protocols(['h2'])
try:
    with socket.create_connection((parts[0], int(parts[1])), timeout=15) as transport:
        with context.wrap_socket(transport, server_hostname=server_name) as tls:
            if tls.selected_alpn_protocol() != 'h2':
                fail('REALITY target did not negotiate HTTP/2; select another target/server-name.')
except (OSError, ssl.SSLError):
    fail('Cannot verify REALITY target certificate with TLS 1.3. Check --target and --server-name or server internet access.')
print('Preflight passed: Ubuntu 26.04 amd64, free ports, TLS 1.3 + HTTP/2 target.')
PY

# Account names are reserved for this installation. Do not adopt unrelated users.
for account in gateway gateway-browser gateway-deploy; do
  if getent passwd "$account" >/dev/null; then
    fail "User $account already exists. Preserve existing data and review the previous installation before proceeding."
  fi
done
for group in gateway gateway-browser gateway-deploy gateway-browser-control; do
  if getent group "$group" >/dev/null; then
    fail "Group $group already exists; review the previous installation first."
  fi
done
python3 "$script_dir/private-gateway-deploy.py" --check < "$archive"

apt-get update -o APT::Update::Error-Mode=any
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  postgresql-18 postgresql-client-18 polkitd curl python3 podman uidmap \
  slirp4netns fuse-overlayfs dbus-user-session sudo ca-certificates
systemctl enable --now postgresql.service
systemctl start polkit.service
runuser -u postgres -- psql -X --port=5432 --dbname=postgres -v ON_ERROR_STOP=1 --tuples-only --no-align \
  --command="SELECT current_setting('server_version_num')::int / 10000, current_setting('listen_addresses'), current_setting('port')" \
  | python3 -c 'import sys; row=sys.stdin.read().strip().split("|"); sys.exit(0 if len(row) == 3 and row[0] == "18" and row[1] in ("localhost", "127.0.0.1", "127.0.0.1,::1", "::1,127.0.0.1") and row[2] == "5432" else "PostgreSQL must be version 18, listen only on loopback, and use port 5432. Review its configuration locally.")'
runuser -u postgres -- psql -X --port=5432 --dbname=postgres -v ON_ERROR_STOP=1 --tuples-only --no-align \
  --command="SELECT CASE WHEN EXISTS(SELECT FROM pg_roles WHERE rolname='gateway') OR EXISTS(SELECT FROM pg_database WHERE datname='gateway') THEN 'occupied' ELSE 'available' END" \
  | python3 -c 'import sys; sys.exit(0 if sys.stdin.read().strip() == "available" else "PostgreSQL gateway role/database already exists. Existing data has been preserved.")'

useradd --system --user-group --home-dir /var/lib/private-gateway --no-create-home --shell /usr/sbin/nologin gateway
groupadd --system gateway-browser-control
useradd --create-home --user-group --shell /usr/sbin/nologin gateway-browser
usermod --append --groups gateway-browser-control gateway
usermod --append --groups gateway-browser-control gateway-browser
install -d -o root -g gateway -m 0750 /etc/private-gateway
install -d -o gateway -g gateway -m 0700 /var/lib/private-gateway
install -d -o root -g root -m 0755 /opt/private-gateway /opt/private-gateway/releases
install -o root -g root -m 0755 "$script_dir/private-gateway-deploy.py" /usr/local/sbin/private-gateway-deploy
release=$(/usr/local/sbin/private-gateway-deploy --stage < "$archive")
[[ "$release" =~ ^/opt/private-gateway/releases/[0-9a-f]{40}$ && -d "$release" ]] || fail 'Release staging returned an invalid path.'
ln -s "$release" /opt/private-gateway/current

# Secrets travel only through process memory and a root-controlled file.
# The generated database password is hexadecimal, so no SQL/URL quoting is needed.
python3 - "$gateway_host" "$gateway_port" "$server_name" "$target" <<'PY'
import grp
import json
import os
import secrets
import subprocess
import sys

host, port, server_name, target = sys.argv[1:]
generated = subprocess.run(['runuser', '-u', 'gateway', '--', '/opt/private-gateway/current/bin/gateway-api', 'keygen'], check=True, capture_output=True)
keys = json.loads(generated.stdout)
password = secrets.token_hex(32)
values = {
    'GATEWAY_DATABASE_URL': f'postgres://gateway:{password}@127.0.0.1:5432/gateway?sslmode=disable',
    'GATEWAY_API_TOKEN': keys['api_token'],
    'GATEWAY_MASTER_KEY': keys['master_key'],
    'GATEWAY_REALITY_PRIVATE_KEY': keys['reality_private_key'],
    'GATEWAY_SHORT_ID': keys['short_id'],
    'GATEWAY_LISTEN': '127.0.0.1:8080',
    'GATEWAY_HOST': host,
    'GATEWAY_PORT': port,
    'GATEWAY_SERVER_NAME': server_name,
    'GATEWAY_REALITY_TARGET': target,
    'GATEWAY_XRAY_LISTEN': '0.0.0.0',
    'GATEWAY_XRAY_CONFIG': '/var/lib/private-gateway/xray.json',
    'GATEWAY_XRAY_BINARY': '/opt/private-gateway/current/bin/xray',
    'GATEWAY_XRAY_RESTART': json.dumps(['/usr/bin/systemctl', '--no-ask-password', 'restart', 'private-gateway-xray.service'], separators=(',', ':')),
    'GATEWAY_WEB_DIR': '/opt/private-gateway/current/web',
    'GATEWAY_BROWSER_BINARY': '/usr/bin/podman',
    'GATEWAY_BROWSER_CONNECTION': 'unix:///run/private-gateway-browser/podman.sock',
    'GATEWAY_BROWSER_IMAGE': 'localhost/private-gateway-browser:0.1',
    'GATEWAY_BROWSER_PORT': '6080',
}
path = '/etc/private-gateway/gateway.env'
descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o640)
with os.fdopen(descriptor, 'w') as output:
    os.fchown(output.fileno(), 0, grp.getgrnam('gateway').gr_gid)
    os.fchmod(output.fileno(), 0o640)
    for key, value in values.items():
        # systemd EnvironmentFile uses shell-style quotes, but is never sourced.
        output.write(f"{key}='{value}'\n")
    output.flush()
    os.fsync(output.fileno())
sql = "SET log_statement = 'none'; SET log_min_duration_statement = -1;\n"
sql += f"CREATE ROLE gateway LOGIN PASSWORD '{password}';\nCREATE DATABASE gateway OWNER gateway;\n"
created = subprocess.run(['runuser', '-u', 'postgres', '--', 'psql', '-X', '--port=5432', '--dbname=postgres', '-v', 'ON_ERROR_STOP=1'], input=sql, text=True, capture_output=True)
if created.returncode:
    raise SystemExit('Database creation failed. Credentials were preserved in /etc/private-gateway/gateway.env; inspect PostgreSQL locally without publishing its logs.')
print('Database and server-only credentials created.')
PY

install -o root -g root -m 0644 "$script_dir/gateway-api.service" /etc/systemd/system/gateway-api.service
install -o root -g root -m 0644 "$script_dir/private-gateway-xray.service" /etc/systemd/system/private-gateway-xray.service
install -o root -g root -m 0644 "$script_dir/49-private-gateway-xray.rules" /etc/polkit-1/rules.d/49-private-gateway-xray.rules
systemctl daemon-reload
systemctl restart polkit.service

# Only the worker can own browser containers. The API receives socket access,
# never access to the worker's home or to a rootful Podman service.
python3 - <<'PY'
import pathlib
import pwd
worker = pwd.getpwnam('gateway-browser')
for source in ('/etc/subuid', '/etc/subgid'):
    ranges = [line.split(':') for line in pathlib.Path(source).read_text().splitlines() if line and not line.startswith('#')]
    if not any(name in ('gateway-browser', str(worker.pw_uid)) and int(count) >= 65536 for name, start, count in ranges):
        raise SystemExit(f'{source} lacks a contiguous range of at least 65536 IDs for gateway-browser. Allocate a nonoverlapping range before continuing.')
PY
install -o root -g root -m 0644 "$release/browser/tmpfiles.conf" /etc/tmpfiles.d/private-gateway-browser.conf
systemd-tmpfiles --create /etc/tmpfiles.d/private-gateway-browser.conf
install -d -o gateway-browser -g gateway-browser -m 0700 /home/gateway-browser/.config /home/gateway-browser/.config/systemd /home/gateway-browser/.config/systemd/user
for unit in private-gateway-browser-worker.socket private-gateway-browser-worker.service; do
  install -o root -g root -m 0644 "$release/browser/$unit" "/home/gateway-browser/.config/systemd/user/$unit"
done
loginctl enable-linger gateway-browser
worker_uid=$(id -u gateway-browser)
systemctl start "user@$worker_uid.service"
fi

# A service account cannot traverse the administrator's private home directory.
# Use a public working directory for every remaining runuser and Podman command.
cd /
worker() {
  runuser -u gateway-browser -- env HOME=/home/gateway-browser \
    XDG_RUNTIME_DIR="/run/user/$worker_uid" \
    DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$worker_uid/bus" "$@"
}
worker systemctl --user daemon-reload
worker systemctl --user enable --now private-gateway-browser-worker.socket
worker podman info --format json | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if d["host"]["security"]["rootless"] and d["host"]["cgroupVersion"] == "v2" else "Rootless Podman with cgroup v2 is required.")'
worker podman build --tag localhost/private-gateway-browser:0.1 --file "$release/browser/Containerfile" "$release/browser"

# EnvironmentFile keeps tokens and the database password out of process arguments.
gateway_command() {
  systemd-run --quiet --wait --pipe --collect \
    --property=User=gateway --property=Group=gateway \
    --property=EnvironmentFile=/etc/private-gateway/gateway.env \
    --property=WorkingDirectory=/opt/private-gateway/current "$@"
}
gateway_command /opt/private-gateway/current/bin/gateway-api migrate
systemctl enable --now gateway-api.service
ready=false
for attempt in {1..30}; do
  if curl --noproxy '*' --fail --silent --max-time 3 http://127.0.0.1:8080/readyz >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || fail 'API readiness failed; inspect sudo journalctl -u gateway-api.service locally.'
gateway_command /opt/private-gateway/current/bin/gatewayctl config validate
gateway_command /opt/private-gateway/current/bin/gatewayctl config apply
systemctl enable private-gateway-xray.service
systemctl is-active --quiet private-gateway-xray.service
python3 - "$gateway_port" <<'PY'
import socket
import sys
import time
for attempt in range(20):
    try:
        with socket.create_connection(('127.0.0.1', int(sys.argv[1])), timeout=2):
            break
    except OSError:
        if attempt == 19:
            raise SystemExit('Xray has not opened its configured TCP port. Inspect its service locally.')
        time.sleep(0.5)
print('Xray TCP listener is available locally.')
PY
gateway_command /opt/private-gateway/current/bin/gatewayctl status

# Start and probe the real rootless browser, then remove the temporary session.
# No cookies or browsing history are retained by this check.
gateway_command /opt/private-gateway/current/bin/gatewayctl browser start
if ! runuser -u gateway -- python3 "$release/browser/smoke.py" --port 6080; then
  gateway_command /opt/private-gateway/current/bin/gatewayctl browser stop || true
  fail 'Rootless browser verification failed. Existing installation was preserved for diagnosis.'
fi
gateway_command /opt/private-gateway/current/bin/gatewayctl browser stop
systemctl is-active --quiet gateway-api.service private-gateway-xray.service

# The deployment identity can only stream a release into the fixed root helper.
# Its root-owned home and authorized_keys cannot be rewritten by that identity.
useradd --system --user-group --home-dir /var/lib/private-gateway-deploy --no-create-home --shell /bin/sh --password '*' gateway-deploy
install -d -o root -g root -m 0755 /var/lib/private-gateway-deploy /var/lib/private-gateway-deploy/.ssh
sudoers_tmp=$(mktemp /etc/sudoers.d/.private-gateway-deploy.XXXXXX)
printf '%s\n' 'gateway-deploy ALL=(root) NOPASSWD: /usr/local/sbin/private-gateway-deploy ""' > "$sudoers_tmp"
chmod 0440 "$sudoers_tmp"
visudo -cf "$sudoers_tmp"
mv -T "$sudoers_tmp" /etc/sudoers.d/private-gateway-deploy
if [[ -n "$deploy_key" ]]; then
  python3 - "$deploy_key" <<'PY'
import os
import pathlib
import sys
fields = pathlib.Path(sys.argv[1]).read_text().strip().split()
path = '/var/lib/private-gateway-deploy/.ssh/authorized_keys'
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
with os.fdopen(fd, 'w') as output:
    os.fchmod(output.fileno(), 0o644)
    output.write(f'command="sudo -n /usr/local/sbin/private-gateway-deploy",restrict ssh-ed25519 {fields[1]} private-gateway-deploy\n')
PY
fi

printf '\nInstallation completed. Public connection parameters:\n'
printf 'Gateway: %s:%s\nDeployment user: gateway-deploy\nAPI: http://127.0.0.1:8080 (use an SSH tunnel)\n' "$gateway_host" "$gateway_port"
printf 'GitHub Actions known_hosts entry (verify the fingerprint against your trusted SSH connection):\n'
if [[ -r /etc/ssh/ssh_host_ed25519_key.pub ]]; then
  awk -v host="$gateway_host" '$1 == "ssh-ed25519" { print host, $1, $2 }' /etc/ssh/ssh_host_ed25519_key.pub
fi
if [[ -z "$deploy_key" ]]; then
  printf 'No deployment public key was supplied. Configure its restricted authorized_keys entry before enabling GitHub Actions.\n'
fi
printf 'Credentials remain in /etc/private-gateway/gateway.env; keep a separate encrypted backup.\n'
