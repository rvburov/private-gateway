#!/usr/bin/env bash
# Собрать для CI релиз под Linux и доверенные файлы первой установки.
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
# scripts/dev.sh release передаёт SHA коммита: в образе проверок, где идёт сборка, нет .git.
version=${GATEWAY_RELEASE_VERSION:-$(git rev-parse HEAD)}
[[ "$version" =~ ^[0-9a-f]{40}$ ]] || { printf 'A full Git SHA is required.\n' >&2; exit 1; }
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/payload/bin" "$output"

CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags='-s -w' \
  -o "$temporary/payload/bin/gateway-api" ./cmd/gateway-api
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags='-s -w' \
  -o "$temporary/payload/bin/gatewayctl" ./cmd/gatewayctl
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags='-s -w' \
  -o "$temporary/payload/bin/gateway-node" ./cmd/gateway-node
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
for name in ("start.sh", "healthcheck.sh", "smoke.py"):
    source = Path("deploy/browser") / name
    if not stat.S_ISREG(source.lstat().st_mode):
        raise SystemExit(f"Browser release file must be regular: {source}")
    (payload / "browser").mkdir(exist_ok=True)
    shutil.copyfile(source, payload / "browser" / name)
for name in ("compose.yml", "compose.xhttp.yml", "compose.xhttp-legacy.yml", "compose.xhttp-h3.yml",
             "compose.node.yml", "compose.node-h3.yml", "Caddyfile.template", "Caddyfile.node.template",
             "render-caddy.py", "render-node-caddy.py", "haproxy.cfg.template", "Dockerfile.api", "Dockerfile.node", "Dockerfile.xray",
             "Dockerfile.browser", "browser-worker.py", "xray-worker.py", "wait-xray.py",
             "vpn-smoke.py", "change-reality-target.py", "rotate-admin-token.py", "admin-credentials.py", "configure-xhttp.py", "configure-proxy.py",
             "image-tags.py", "release-ports.py", "install.sh", "install-node.sh", "install-bridge.sh", "prepare-vps.sh", "setup-node.py", "bridge.py", "upgrade-updater.sh", "update.sh"):
    source = Path("deploy/docker") / name
    if not stat.S_ISREG(source.lstat().st_mode):
        raise SystemExit(f"Docker release file must be regular: {source}")
    (payload / "docker").mkdir(exist_ok=True)
    shutil.copyfile(source, payload / "docker" / name)
shutil.copyfile(Path("scripts/verify-release.py"), payload / "docker/verify-release.py")
shutil.copyfile(Path("scripts/setup-vps.sh"), payload / "docker/setup-vps.sh")
required = {"bin/gateway-api", "bin/gatewayctl", "bin/gateway-node", "bin/xray", "web/index.html",
            "browser/start.sh", "browser/healthcheck.sh", "browser/smoke.py",
            "docker/compose.yml", "docker/compose.xhttp.yml", "docker/compose.xhttp-legacy.yml", "docker/compose.xhttp-h3.yml",
            "docker/compose.node.yml", "docker/compose.node-h3.yml", "docker/Caddyfile.template", "docker/Caddyfile.node.template",
            "docker/render-caddy.py", "docker/render-node-caddy.py", "docker/haproxy.cfg.template", "docker/Dockerfile.api", "docker/Dockerfile.node",
            "docker/Dockerfile.xray", "docker/Dockerfile.browser", "docker/browser-worker.py",
            "docker/xray-worker.py", "docker/wait-xray.py", "docker/vpn-smoke.py",
            "docker/change-reality-target.py", "docker/rotate-admin-token.py", "docker/admin-credentials.py", "docker/configure-xhttp.py", "docker/configure-proxy.py",
            "docker/image-tags.py", "docker/release-ports.py", "docker/install.sh", "docker/install-node.sh", "docker/install-bridge.sh", "docker/bridge.py", "docker/update.sh",
            "docker/verify-release.py", "docker/prepare-vps.sh", "docker/setup-node.py", "docker/setup-vps.sh"}
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

# Файлы первой установки не содержат созданных ключей и локальных файлов окружения.
install = temporary / "install"
for source in (Path("deploy/docker/install.sh"), Path("deploy/docker/install-node.sh"), Path("deploy/docker/install-bridge.sh"),
               Path("deploy/docker/prepare-vps.sh"), Path("deploy/docker/setup-node.py"), Path("scripts/setup-vps.sh"),
               Path("deploy/docker/upgrade-updater.sh"), Path("scripts/verify-release.py")):
    if not stat.S_ISREG(source.lstat().st_mode):
        raise SystemExit(f"Installer source must be a regular file: {source}")
    target = install / source.name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
if not all((install / name).is_file() for name in
           ("install.sh", "install-node.sh", "install-bridge.sh", "prepare-vps.sh", "setup-node.py", "setup-vps.sh", "upgrade-updater.sh", "verify-release.py")):
    raise SystemExit("The Docker installer and release verifier are required")
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
