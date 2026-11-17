#!/usr/bin/env bash
# Отдельно установить VPS-мост из проверенного артефакта CI, не меняя SSH и firewall.
set -Eeuo pipefail
umask 077

fail() { printf 'Bridge installation stopped: %s\n' "$*" >&2; exit 1; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
archive=''
config=''
mode=()
while (($#)); do
  case "$1" in
    --help|-h)
      printf 'sudo bash install-bridge.sh --archive release.tar.gz --config bridge.json [--check]\n'
      printf 'Запускать только на отдельном VPS-мосте. --check не запускает мост.\n'
      exit 0 ;;
    --check) mode=(--check); shift ;;
    --archive|--config)
      (($# >= 2)) || fail "Missing value after $1"
      if [[ $1 == --archive ]]; then archive=$2; else config=$2; fi
      shift 2 ;;
    *) fail "Unknown argument: $1" ;;
  esac
done
[[ $EUID == 0 && $(uname -s) == Linux ]] || fail 'Run with sudo on the Linux bridge VPS.'
[[ -f $archive && ! -L $archive && -f $config && ! -L $config ]] || fail 'Regular archive and configuration files are required.'
[[ -f $script_dir/verify-release.py ]] || fail 'Release verifier is missing.'
checksum=$(dirname -- "$archive")/release.tar.gz.sha256
[[ -f $checksum && ! -L $checksum ]] || fail 'Release checksum is missing.'
python3 - "$archive" "$checksum" <<'PY'
import hashlib, pathlib, re, sys
archive, checksum = map(pathlib.Path, sys.argv[1:])
expected = checksum.read_text().strip()
with archive.open('rb') as source:
    if (archive.name != 'release.tar.gz' or
            not re.fullmatch(r'[0-9a-f]{64}  release\.tar\.gz', expected) or
            hashlib.file_digest(source, 'sha256').hexdigest() != expected[:64]):
        raise SystemExit('Release checksum mismatch.')
PY
version=$(python3 "$script_dir/verify-release.py" --tar "$archive")
[[ $version =~ ^[0-9a-f]{40}$ ]] || fail 'Release identity is invalid.'
tar -xOf "$archive" docker/install-bridge.sh | cmp -s - "$script_dir/install-bridge.sh" \
  || fail 'Installer does not match the verified release.'
tar -xOf "$archive" docker/verify-release.py | cmp -s - "$script_dir/verify-release.py" \
  || fail 'Verifier does not match the verified release.'
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
tar -xOf "$archive" docker/bridge.py > "$temporary/bridge.py"
python3 "$temporary/bridge.py" "$config" "${mode[@]}"
