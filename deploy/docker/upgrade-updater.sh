#!/usr/bin/env bash
# Одноразовый переход с релиза 8d73025: сначала обновить проверяющие помощники,
# затем передать уже проверенный архив обычному updater с его собственным откатом.
set -Eeuo pipefail
umask 077

fail() { printf 'Helper upgrade stopped: %s\n' "$*" >&2; exit 1; }
[[ $# == 3 ]] || fail 'Usage: bash upgrade-updater.sh ABSOLUTE_BUNDLE_DIR TARGET_COMMIT_SHA RELEASE_SHA256'
bundle=$1
target=$2
expected_digest=$3
[[ $bundle == /* && $bundle != */ && $target =~ ^[0-9a-f]{40}$ && $expected_digest =~ ^[0-9a-f]{64}$ ]] \
  || fail 'The bundle path, commit SHA or archive SHA256 is invalid.'
[[ -d $bundle && ! -L $bundle && $(realpath -e -- "$bundle") == "$bundle" ]] \
  || fail 'The bundle directory must be an existing absolute path without symlinks.'

# Тестовый корень доступен только непривилегированному процессу. На рабочем VPS
# все пути ниже фиксированы, а переменная теста при запуске от root запрещена.
root=${PG_UPGRADE_TEST_ROOT:-}
if [[ -n $root ]]; then
  [[ $EUID != 0 && $root == /tmp/* && -d $root && ! -L $root && $(realpath -e -- "$root") == "$root" ]] \
    || fail 'The isolated test root is invalid.'
  [[ -n ${PG_UPGRADE_TEST_COMMAND:-} ]] || fail 'The isolated test updater is missing.'
  owner=$EUID
else
  [[ $EUID == 0 && -z ${PG_UPGRADE_TEST_COMMAND:-} ]] || fail 'Run as root without test overrides.'
  [[ $bundle == /root/* ]] || fail 'The production bundle must be under /root.'
  owner=0
fi

regular_owned() {
  local path=$1 mode=$2
  [[ -f $path && ! -L $path && $(stat -c %u -- "$path") == "$owner" &&
     $(stat -c %a -- "$path") == "$mode" ]]
}
safe_directory() {
  local path=$1
  [[ -d $path && ! -L $path && $(realpath -e -- "$path") == "$path" &&
     $(stat -c %u -- "$path") == "$owner" ]]
}
[[ $(stat -c %u -- "$bundle") == "$owner" && $(stat -c %a -- "$bundle") == 700 ]] \
  || fail 'The bundle directory must belong to the caller and have mode 0700.'
[[ $(realpath -e -- "$0") == "$bundle/upgrade-updater.sh" ]] \
  || fail 'Run the upgrade script from the reviewed bundle.'
regular_owned "$bundle/upgrade-updater.sh" 700 || fail 'The bundle script is not a private regular file.'
regular_owned "$bundle/verify-release.py" 600 || fail 'The bundle verifier is not a private regular file.'
regular_owned "$bundle/release.tar.gz" 600 || fail 'The release archive is not a private regular file.'

archive=$bundle/release.tar.gz
actual_digest=$(sha256sum -- "$archive" | cut -d' ' -f1)
[[ $actual_digest == "$expected_digest" ]] || fail 'The pinned release SHA256 does not match.'
verified=$(python3 "$bundle/verify-release.py" --tar "$archive") \
  || fail 'The reviewed verifier rejected the release archive.'
[[ $verified == "$target" ]] || fail 'The verified release has another commit SHA.'

staging=$(mktemp -d "${TMPDIR:-/tmp}/private-gateway-helper-stage.XXXXXXXX")
trap 'rm -rf -- "$staging"' EXIT
tar -xOf "$archive" docker/update.sh > "$staging/update.sh"
tar -xOf "$archive" docker/verify-release.py > "$staging/verify-release.py"
tar -xOf "$archive" docker/upgrade-updater.sh > "$staging/upgrade-updater.sh"
cmp -s "$bundle/verify-release.py" "$staging/verify-release.py" \
  || fail 'The reviewed verifier differs from the verified release.'
cmp -s "$bundle/upgrade-updater.sh" "$staging/upgrade-updater.sh" \
  || fail 'The bootstrap script differs from the verified release.'

old_version=8d73025eae365e426e0d2080d9f81f15ec267b10
old_updater_sha=08322aa8bf46e88941145d85f247ba4df2a3f7adf4f15ffe27915dc9bb7536b3
old_verifier_sha=92fbcead0ca0ec167f1989b144eee1916df97ea4328c205f260497598266a67c
installed=$root/etc/private-gateway-docker/installed.version
current=$root/opt/private-gateway-docker/current
releases=$root/opt/private-gateway-docker/releases
updater=$root/usr/local/sbin/private-gateway-docker-deploy
verifier=$root/usr/local/lib/private-gateway-docker/verify-release.py
lock=$root/run/lock/private-gateway-docker-install.lock
backup_parent=$root/var/backups/private-gateway
for directory in "$root/etc/private-gateway-docker" "$root/opt/private-gateway-docker" \
    "$releases" "$root/usr/local/sbin" "$root/usr/local/lib/private-gateway-docker" \
    "$root/run/lock" "$root/var/backups"; do
  safe_directory "$directory" || fail 'A deployment path is linked or has an unexpected owner.'
done
regular_owned "$lock" 600 || fail 'The deployment lock is missing or unsafe.'
exec 9>"$lock"
flock -n 9 || fail 'Another deployment is running.'
locked=true

state_is() {
  local version=$1
  [[ -f $installed && ! -L $installed && $(cat -- "$installed") == "$version" &&
     $(stat -c %u -- "$installed") == "$owner" &&
     -L $current && $(stat -c %u -- "$current") == "$owner" &&
     $(readlink -- "$current") == "$releases/$version" &&
     $(readlink -f -- "$current") == "$releases/$version" &&
     -d $releases/$version && ! -L $releases/$version &&
     $(stat -c %u -- "$releases/$version") == "$owner" ]]
}
state_is "$old_version" || fail 'The installed release is not the pinned 8d73025 state.'
regular_owned "$updater" 755 || fail 'The installed updater is not a root-owned regular file.'
regular_owned "$verifier" 644 || fail 'The installed verifier is not a root-owned regular file.'
[[ $(sha256sum -- "$updater" | cut -d' ' -f1) == "$old_updater_sha" &&
   $(sha256sum -- "$verifier" | cut -d' ' -f1) == "$old_verifier_sha" ]] \
  || fail 'The installed helpers differ from the pinned 8d73025 release.'

install -d -m 0700 -- "$backup_parent"
[[ -d $backup_parent && ! -L $backup_parent && $(stat -c %u -- "$backup_parent") == "$owner" &&
   $(stat -c %a -- "$backup_parent") == 700 ]] \
  || fail 'The backup directory is not private.'
backup=$(mktemp -d "$backup_parent/helper-upgrade-${target}-XXXXXXXX")
cp -p -- "$updater" "$backup/update.sh"
cp -p -- "$verifier" "$backup/verify-release.py"
printf '%s\n' "$old_version" > "$backup/installed.version"

atomic_install() {
  local source=$1 destination=$2 mode=$3 temporary
  temporary=$(mktemp "${destination}.upgrade.XXXXXXXX")
  if ! install -m "$mode" -- "$source" "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  mv -Tf -- "$temporary" "$destination"
}

changed=false
complete=false
recover() {
  local status=$?
  trap - EXIT
  if [[ $changed == true && $complete != true ]]; then
    if [[ $locked != true ]]; then
      if flock -w 180 9; then locked=true; fi
    fi
    if [[ $locked == true ]] && state_is "$old_version"; then
      atomic_install "$backup/update.sh" "$updater" 755 || true
      atomic_install "$backup/verify-release.py" "$verifier" 644 || true
      if cmp -s "$backup/update.sh" "$updater" && cmp -s "$backup/verify-release.py" "$verifier"; then
        printf 'Old helpers restored after failed deployment. Backup: %s\n' "$backup" >&2
      else
        printf 'Helper restoration needs manual inspection. Backup: %s\n' "$backup" >&2
      fi
    else
      printf 'Unexpected release state or deployment lock; helpers left for inspection. Backup: %s\n' "$backup" >&2
    fi
  fi
  rm -rf -- "$staging"
  exit "$status"
}
trap recover EXIT

changed=true
atomic_install "$staging/verify-release.py" "$verifier" 644
atomic_install "$staging/update.sh" "$updater" 755
cmp -s "$verifier" "$staging/verify-release.py" && cmp -s "$updater" "$staging/update.sh" \
  || fail 'The installed helpers differ from the verified release.'
flock -u 9
locked=false

deploy_command=$updater
if [[ -n $root ]]; then deploy_command=$PG_UPGRADE_TEST_COMMAND; fi
set +e
{ printf '%s\n' "$target"; cat -- "$archive"; } | "$deploy_command"
deploy_status=$?
set -e
flock -w 180 9 || fail "Cannot reacquire the deployment lock; backup: $backup"
locked=true
((deploy_status == 0)) || fail "The verified updater failed with status $deploy_status; backup: $backup"
state_is "$target" || fail "The updater returned success without installing the target release; backup: $backup"
cmp -s "$verifier" "$staging/verify-release.py" && cmp -s "$updater" "$staging/update.sh" \
  || fail "The installed target helpers differ from the reviewed release; backup: $backup"
complete=true
printf 'Verified release %s installed; helper backup: %s\n' "$target" "$backup"
