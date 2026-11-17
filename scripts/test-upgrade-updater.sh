#!/usr/bin/env bash
# Проверка одноразового обновления helper без доступа к рабочему VPS и Docker.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d /tmp/private-gateway-upgrade-test.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT
old=8d73025eae365e426e0d2080d9f81f15ec267b10
target=$(printf 'b%.0s' {1..40})
[[ $(sha256sum scripts/fixtures/verify-release-8d73025.py | cut -d' ' -f1) == \
   92fbcead0ca0ec167f1989b144eee1916df97ea4328c205f260497598266a67c ]] \
  || { echo 'The pinned old verifier fixture changed.' >&2; exit 1; }
[[ $(sha256sum scripts/fixtures/update-8d73025.sh | cut -d' ' -f1) == \
   08322aa8bf46e88941145d85f247ba4df2a3f7adf4f15ffe27915dc9bb7536b3 ]] \
  || { echo 'The pinned old updater fixture changed.' >&2; exit 1; }

bundle=$work/bundle
install -d -m 0700 "$bundle"
install -m 0700 deploy/docker/upgrade-updater.sh "$bundle/upgrade-updater.sh"
install -m 0600 scripts/verify-release.py "$bundle/verify-release.py"
python3 - "$bundle/release.tar.gz" "$target" <<'PY'
import gzip
import hashlib
import io
import json
from pathlib import Path
import runpy
import sys
import tarfile

archive_path = Path(sys.argv[1])
version = sys.argv[2]
required = runpy.run_path('scripts/verify-release.py')['REQUIRED']
files = {name: name.encode() for name in required}
files['docker/update.sh'] = Path('deploy/docker/update.sh').read_bytes()
files['docker/verify-release.py'] = Path('scripts/verify-release.py').read_bytes()
files['docker/upgrade-updater.sh'] = Path('deploy/docker/upgrade-updater.sh').read_bytes()
manifest = json.dumps({
    'version': version, 'os': 'linux', 'arch': 'amd64',
    'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()},
}, sort_keys=True, separators=(',', ':')).encode() + b'\n'
with archive_path.open('wb') as raw, gzip.GzipFile(filename='', fileobj=raw, mode='wb', mtime=0) as zipped:
    with tarfile.open(fileobj=zipped, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for name, data in sorted({'manifest.json': manifest, **files}.items()):
            member = tarfile.TarInfo(name)
            member.size = len(data)
            member.mode = 0o755 if name.startswith('bin/') else 0o644
            archive.addfile(member, io.BytesIO(data))
PY
chmod 0600 "$bundle/release.tar.gz"
digest=$(sha256sum "$bundle/release.tar.gz" | cut -d' ' -f1)
python3 "$bundle/verify-release.py" --tar "$bundle/release.tar.gz" > "$work/verified"
[[ $(cat "$work/verified") == "$target" ]] || { echo 'The new verifier rejected its fixture.' >&2; exit 1; }
if python3 scripts/fixtures/verify-release-8d73025.py --tar "$bundle/release.tar.gz" > "$work/old.out" 2>&1; then
  echo 'The old verifier unexpectedly accepted a release with gateway-node.' >&2
  exit 1
fi
grep -q 'Unexpected release executable' "$work/old.out" \
  || { echo 'The old verifier failed for another reason.' >&2; exit 1; }

cat > "$work/mock-updater.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
IFS= read -r version
digest=$(sha256sum | cut -d' ' -f1)
[[ $version == "$MOCK_TARGET" && $digest == "$MOCK_DIGEST" ]] || exit 90
case "$MOCK_OUTCOME" in
  success)
    mkdir -p "$PG_UPGRADE_TEST_ROOT/opt/private-gateway-docker/releases/$version"
    printf '%s\n' "$version" > "$PG_UPGRADE_TEST_ROOT/etc/private-gateway-docker/installed.version"
    ln -s "$PG_UPGRADE_TEST_ROOT/opt/private-gateway-docker/releases/$version" \
      "$PG_UPGRADE_TEST_ROOT/opt/private-gateway-docker/current.next"
    mv -Tf "$PG_UPGRADE_TEST_ROOT/opt/private-gateway-docker/current.next" \
      "$PG_UPGRADE_TEST_ROOT/opt/private-gateway-docker/current"
    ;;
  fail) exit 42 ;;
  unexpected) printf '%s\n' "$(printf 'c%.0s' {1..40})" > \
      "$PG_UPGRADE_TEST_ROOT/etc/private-gateway-docker/installed.version"; exit 43 ;;
  *) exit 91 ;;
esac
SH
chmod 0700 "$work/mock-updater.sh"

new_state() {
  test_root=$(mktemp -d /tmp/private-gateway-upgrade-root.XXXXXXXX)
  mkdir -p "$test_root/etc/private-gateway-docker" "$test_root/opt/private-gateway-docker/releases/$old" \
    "$test_root/usr/local/sbin" "$test_root/usr/local/lib/private-gateway-docker" \
    "$test_root/run/lock" "$test_root/var/backups/private-gateway"
  chmod 0700 "$test_root/var/backups/private-gateway"
  : > "$test_root/run/lock/private-gateway-docker-install.lock"
  chmod 0600 "$test_root/run/lock/private-gateway-docker-install.lock"
  printf '%s\n' "$old" > "$test_root/etc/private-gateway-docker/installed.version"
  ln -s "$test_root/opt/private-gateway-docker/releases/$old" "$test_root/opt/private-gateway-docker/current"
  install -m 0755 scripts/fixtures/update-8d73025.sh "$test_root/usr/local/sbin/private-gateway-docker-deploy"
  install -m 0644 scripts/fixtures/verify-release-8d73025.py "$test_root/usr/local/lib/private-gateway-docker/verify-release.py"
}
run_upgrade() {
  local outcome=$1 expected=$2
  if PG_UPGRADE_TEST_ROOT="$test_root" PG_UPGRADE_TEST_COMMAND="$work/mock-updater.sh" \
      MOCK_TARGET="$target" MOCK_DIGEST="$digest" MOCK_OUTCOME="$outcome" \
      bash "$bundle/upgrade-updater.sh" "$bundle" "$target" "$digest" > "$work/run.out" 2>&1; then
    [[ $expected == success ]] || { echo "Unexpected success: $outcome" >&2; exit 1; }
  else
    [[ $expected == failure ]] || { cat "$work/run.out" >&2; exit 1; }
  fi
}
assert_old() {
  [[ $(cat "$test_root/etc/private-gateway-docker/installed.version") == "$old" ]]
  cmp -s scripts/fixtures/update-8d73025.sh "$test_root/usr/local/sbin/private-gateway-docker-deploy"
  cmp -s scripts/fixtures/verify-release-8d73025.py "$test_root/usr/local/lib/private-gateway-docker/verify-release.py"
}
assert_new_helpers() {
  cmp -s deploy/docker/update.sh "$test_root/usr/local/sbin/private-gateway-docker-deploy"
  cmp -s scripts/verify-release.py "$test_root/usr/local/lib/private-gateway-docker/verify-release.py"
}

new_state
run_upgrade success success
[[ $(cat "$test_root/etc/private-gateway-docker/installed.version") == "$target" ]]
assert_new_helpers
[[ $(find "$test_root/var/backups/private-gateway" -mindepth 1 -maxdepth 1 -type d | wc -l) == 1 ]]
rm -rf -- "$test_root"
echo 'Pinned old-to-new helper upgrade: success PASS.'

new_state
run_upgrade fail failure
assert_old
grep -q 'Old helpers restored after failed deployment' "$work/run.out"
rm -rf -- "$test_root"
echo 'Failed deployment restores old helpers: PASS.'

new_state
run_upgrade unexpected failure
assert_new_helpers
grep -q 'Unexpected release state' "$work/run.out"
rm -rf -- "$test_root"
echo 'Unexpected release state retains backup for inspection: PASS.'

new_state
bad_digest=$(printf '0%.0s' {1..64})
if PG_UPGRADE_TEST_ROOT="$test_root" PG_UPGRADE_TEST_COMMAND="$work/mock-updater.sh" \
    bash "$bundle/upgrade-updater.sh" "$bundle" "$target" "$bad_digest" > "$work/run.out" 2>&1; then
  echo 'An incorrect archive digest was accepted.' >&2; exit 1
fi
assert_old
rm -rf -- "$test_root"
echo 'Incorrect archive digest changes nothing: PASS.'

new_state
printf '\n# Bundle drift fixture.\n' >> "$bundle/upgrade-updater.sh"
run_upgrade fail failure
grep -q 'bootstrap script differs from the verified release' "$work/run.out"
assert_old
install -m 0700 deploy/docker/upgrade-updater.sh "$bundle/upgrade-updater.sh"
rm -rf -- "$test_root"
echo 'Bootstrap script drift changes nothing: PASS.'

new_state
rm "$test_root/usr/local/sbin/private-gateway-docker-deploy"
ln -s "$work/mock-updater.sh" "$test_root/usr/local/sbin/private-gateway-docker-deploy"
run_upgrade fail failure
grep -q 'not a root-owned regular file' "$work/run.out"
rm -rf -- "$test_root"
echo 'Linked helper destination is rejected: PASS.'

new_state
ready=$work/lock-ready
flock "$test_root/run/lock/private-gateway-docker-install.lock" \
  bash -c 'touch "$1"; sleep 3' -- "$ready" &
locker=$!
for _ in {1..30}; do [[ -e $ready ]] && break; sleep 0.1; done
[[ -e $ready ]] || { echo 'The lock fixture did not start.' >&2; exit 1; }
run_upgrade fail failure
grep -q 'Another deployment is running' "$work/run.out"
wait "$locker"
assert_old
rm -rf -- "$test_root"
echo 'Existing deployment lock is respected: PASS.'
