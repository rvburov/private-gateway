#!/usr/bin/env bash
# Настоящие пользователи, SSH и sudo проверяются только в одноразовом контейнере Ubuntu.
set -Eeuo pipefail
repository=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
command -v docker >/dev/null || { printf 'Docker is required.\n' >&2; exit 1; }
image='ubuntu:26.04@sha256:d66b366dac61b575bee2aef07e9c26e8070096fbe84a3d4d3eb0da069b5d76db'
test_log=$(mktemp)
trap 'rm -f -- "$test_log"' EXIT
MSYS_NO_PATHCONV=1 docker run --rm --interactive --network bridge \
  --mount "type=bind,src=$repository/deploy/docker/prepare-vps.sh,dst=/work/prepare-vps.sh,readonly" \
  "$image" bash -s <<'CONTAINER' | tee "$test_log"
set -Eeuo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -o APT::Update::Error-Mode=any >/dev/null
apt-get install -y --no-install-recommends openssh-server python3 sudo adduser \
  curl iproute2 util-linux ca-certificates >/dev/null
umask 077
ssh-keygen -q -t ed25519 -N '' -f /tmp/automation
ssh-keygen -q -t ed25519 -N '' -f /tmp/existing

# Некорректный ключ не должен создавать пользователя.
printf 'ssh-ed25519 invalid\n' >/tmp/invalid.pub
if bash /work/prepare-vps.sh --role bridge --user pg_invalid \
    --public-key /tmp/invalid.pub --access-only >/tmp/invalid.log 2>&1; then
  printf 'Invalid public key was accepted.\n' >&2
  exit 1
fi
if getent passwd pg_invalid >/dev/null; then
  printf 'Invalid input changed the user database.\n' >&2
  exit 1
fi

# Первый запуск под обычным пользователем с sudo, затем повторный запуск под root.
adduser --disabled-password --gecos '' pg_initial >/dev/null
printf 'pg_initial ALL=(ALL:ALL) NOPASSWD: ALL\n' >/etc/sudoers.d/pg-initial
chmod 0440 /etc/sudoers.d/pg-initial
chmod 0644 /tmp/automation.pub
runuser -u pg_initial -- bash /work/prepare-vps.sh --role bridge --user pg_created \
  --public-key /tmp/automation.pub --access-only
runuser -u pg_created -- sudo -n id -u | grep -qx 0

# Чужой ключ и посторонний sudoers сохраняются при повторной настройке.
cat /tmp/existing.pub >>/home/pg_created/.ssh/authorized_keys
printf 'root ALL=(ALL:ALL) ALL\n' >/etc/sudoers.d/pg-sentinel
chmod 0440 /etc/sudoers.d/pg-sentinel
sentinel=$(sha256sum /etc/sudoers.d/pg-sentinel)
for attempt in 1 2; do
  bash /work/prepare-vps.sh --role bridge --user pg_created \
    --public-key /tmp/automation.pub --access-only
done
test "$(wc -l </home/pg_created/.ssh/authorized_keys)" -eq 2
test "$(sha256sum /etc/sudoers.d/pg-sentinel)" = "$sentinel"
test "$(stat -c '%U %a' /home/pg_created/.ssh/authorized_keys)" = 'pg_created 600'
test "$(stat -c '%U %a' /etc/sudoers.d/private-gateway-admin-pg_created)" = 'root 440'
visudo -c

# Отдельный sshd слушает только петлю контейнера; наружу ни один порт не публикуется.
mkdir -p /run/sshd
ssh-keygen -A >/dev/null
cat >/tmp/sshd_config <<'CONFIG'
Port 2222
ListenAddress 127.0.0.1
HostKey /etc/ssh/ssh_host_ed25519_key
PidFile /run/pg-test-sshd.pid
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
UsePAM yes
StrictModes yes
AuthorizedKeysFile .ssh/authorized_keys
CONFIG
/usr/sbin/sshd -f /tmp/sshd_config
ssh-keyscan -p 2222 -t ed25519 127.0.0.1 >/tmp/known_hosts 2>/dev/null
ssh -n -F /dev/null -p 2222 -i /tmp/automation -o IdentitiesOnly=yes \
  -o IdentityAgent=none -o BatchMode=yes -o StrictHostKeyChecking=yes \
  -o UserKnownHostsFile=/tmp/known_hosts pg_created@127.0.0.1 \
  'test "$(id -un)" = pg_created && test "$(sudo -n id -u)" = 0 && printf "REAL_SSH_SUDO_OK\n"'
printf 'VPS_ACCESS_INTEGRATION_OK\n'
CONTAINER
grep -qx 'VPS_ACCESS_INTEGRATION_OK' "$test_log"
