#!/usr/bin/env bash
# Build a native Linux release and the trusted first-install files for CI.
set -euo pipefail

repository=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repository"
output="${1:-$repository/.local/release}"
[[ "$output" = /* ]] || output="$repository/$output"
[[ "$(uname -s)" == Linux ]] || { printf 'Linux is required.\n' >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  *) printf 'Production installation currently supports Linux amd64 only.\n' >&2; exit 1 ;;
esac
if [[ "${GOARCH:-$architecture}" != "$architecture" || "${GOOS:-linux}" != linux ]]; then
  printf 'Cross-compilation is not supported: Xray must match the native architecture.\n' >&2
  exit 1
fi
version=$(git rev-parse HEAD)
[[ "$version" =~ ^[0-9a-f]{40}$ ]] || { printf 'A full Git SHA is required.\n' >&2; exit 1; }
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/payload/bin" "$output"

CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags='-s -w' \
  -o "$temporary/payload/bin/gateway-api" ./cmd/gateway-api
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags='-s -w' \
  -o "$temporary/payload/bin/gatewayctl" ./cmd/gatewayctl
bash scripts/install-xray.sh "$temporary/payload/bin"
npm --prefix web ci
npm --prefix web run build

python3 - "$temporary" "$output" "$version" "$architecture" <<'PY'
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile

temporary, output = map(Path, sys.argv[1:3])
version, architecture = sys.argv[3:5]
payload = temporary / "payload"

def copy_regular_tree(source, destination):
    if source.is_symlink() or not source.is_dir():
        raise SystemExit(f"Not a regular directory: {source}")
    for path in sorted(source.rglob("*")):
        mode = path.lstat().st_mode
        if stat.S_ISLNK(mode) or not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
            raise SystemExit(f"Unsupported release entry: {path}")
        if stat.S_ISREG(mode):
            target = destination / path.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)

copy_regular_tree(Path("web/dist"), payload / "web")
copy_regular_tree(Path("deploy/browser"), payload / "browser")
required = {"bin/gateway-api", "bin/gatewayctl", "bin/xray", "web/index.html",
            "browser/Containerfile", "browser/start.sh", "browser/healthcheck.sh", "browser/smoke.py",
            "browser/private-gateway-browser-worker.socket", "browser/private-gateway-browser-worker.service",
            "browser/tmpfiles.conf"}
files = {path.relative_to(payload).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
         for path in sorted(payload.rglob("*")) if path.is_file()}
if not required <= files.keys():
    raise SystemExit("Required release files are missing")
manifest = json.dumps({"version": version, "os": "linux", "arch": architecture, "files": files},
                      sort_keys=True, separators=(",", ":")).encode() + b"\n"
archive_path = temporary / "release.tar.gz"
with archive_path.open("wb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped:
    with tarfile.open(fileobj=zipped, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        entries = {"manifest.json": manifest}
        entries.update({name: (payload / name).read_bytes() for name in files})
        for name, content in sorted(entries.items()):
            member = tarfile.TarInfo(name)
            member.size = len(content)
            member.mode = 0o755 if name.startswith("bin/") else 0o644
            archive.addfile(member, io.BytesIO(content))
checksum = hashlib.sha256(archive_path.read_bytes()).hexdigest()

# Only Git-tracked bootstrap files are distributed; never include a local key or env file.
install = temporary / "install"
tracked = subprocess.check_output(["git", "ls-files", "-z", "--", "deploy/production"]).split(b"\0")
for encoded in filter(None, tracked):
    source = Path(os.fsdecode(encoded))
    if not stat.S_ISREG(source.lstat().st_mode):
        raise SystemExit(f"Installer source must be a regular file: {source}")
    target = install / source.relative_to("deploy/production")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
if not (install / "install.sh").is_file() or not (install / "private-gateway-deploy.py").is_file():
    raise SystemExit("Commit the production installer and deployment helper before building a release")
destination_install = output / "install"
if destination_install.is_symlink() or (destination_install.exists() and not destination_install.is_dir()):
    raise SystemExit("Output install path is not a regular directory")
if destination_install.exists():
    shutil.rmtree(destination_install)
shutil.copytree(install, destination_install)
with tempfile.NamedTemporaryFile(dir=output, prefix=".release-", delete=False) as staged:
    staged_path = Path(staged.name)
    with archive_path.open("rb") as source:
        shutil.copyfileobj(source, staged)
os.replace(staged_path, output / "release.tar.gz")
(output / "release.tar.gz.sha256").write_text(f"{checksum}  release.tar.gz\n")
print(f"Built Linux {architecture} release {version}: {output}")
PY
