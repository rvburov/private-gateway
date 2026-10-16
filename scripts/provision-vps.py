#!/usr/bin/env python3
"""Install a verified CI release on one new VPS over an existing trusted SSH login."""

import argparse
import hashlib
import ipaddress
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]


def run(*args, capture=False):
    return subprocess.run(args, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", required=True, help="public IPv4 address of the new VPS")
    parser.add_argument("--domain", required=True, help="DNS name pointing to that VPS")
    parser.add_argument("--ssh-user", required=True, help="existing sudo-capable SSH user")
    parser.add_argument("--artifact", required=True, type=Path, help="downloaded private-gateway-linux-amd64 CI artifact directory")
    parser.add_argument("--sha", required=True, help="full SHA of the successful CI commit")
    parser.add_argument("--dry-run", action="store_true", help="verify the artifact without opening SSH")
    args = parser.parse_args()
    ipaddress.IPv4Address(args.server)
    if not re.fullmatch(r"[a-zA-Z0-9.-]{4,253}", args.domain) or ".." in args.domain:
        parser.error("invalid panel domain")
    if not re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", args.ssh_user):
        parser.error("invalid SSH username")
    if not re.fullmatch(r"[0-9a-f]{40}", args.sha):
        parser.error("--sha must be a full commit SHA")
    artifact = args.artifact.resolve()
    files = [artifact / "release.tar.gz", artifact / "release.tar.gz.sha256",
             artifact / "install/install.sh", artifact / "install/verify-release.py"]
    if not all(path.is_file() and not path.is_symlink() for path in files):
        parser.error("CI artifact is incomplete")
    checksum = files[1].read_text().strip()
    if not re.fullmatch(r"[0-9a-f]{64}  release\.tar\.gz", checksum):
        parser.error("invalid release checksum")
    with files[0].open("rb") as source:
        actual = hashlib.file_digest(source, "sha256").hexdigest()
    if actual != checksum[:64]:
        parser.error("release checksum does not match")
    version = run(sys.executable, str(ROOT / "scripts/verify-release.py"),
                  "--tar", str(files[0]), capture=True).stdout.strip()
    if version != args.sha:
        parser.error("artifact commit does not match --sha")
    if args.dry_run:
        print(f"Release {version} verified for {args.domain} on {args.server}; SSH was not opened.")
        return

    key_dir = ROOT / ".local/provision-keys"
    key_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    key_dir.chmod(0o700)
    key = key_dir / args.server
    if not key.exists():
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key))
    public_key = Path(str(key) + ".pub")
    if not public_key.is_file():
        parser.error("deploy public key is missing")
    destination = f"{args.ssh_user}@{args.server}"
    ssh_options = ["-o", "StrictHostKeyChecking=yes"]
    remote = run("ssh", *ssh_options, destination,
                 "mktemp -d /tmp/private-gateway-install.XXXXXXXX", capture=True).stdout.strip()
    if not re.fullmatch(r"/tmp/private-gateway-install\.[A-Za-z0-9]{8}", remote):
        parser.error("VPS returned an invalid temporary directory")
    print("Release verified. Uploading to the new VPS; sudo may ask for your password.")
    for local, target in [(files[0], "release.tar.gz"), (files[1], "release.tar.gz.sha256"),
                          (files[2], "install.sh"), (files[3], "verify-release.py"),
                          (public_key, "deploy-key.pub")]:
        run("scp", *ssh_options, str(local), f"{destination}:{remote}/{target}")
    run("ssh", "-tt", *ssh_options, destination,
        f"sudo bash {remote}/install.sh --archive {remote}/release.tar.gz "
        f"--host {args.server} --domain {args.domain} --deploy-key {remote}/deploy-key.pub")
    run("ssh", *ssh_options, destination, f"rm -rf -- {remote}")
    print("New VPS installed. Configure its administrator in SSH, then check a real VLESS client.")
    print(f"Deploy key for this VPS is stored locally at {key}; keep it private.")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"Provisioning stopped at command exit {error.returncode}; inspect VPS and CI artifact before retrying") from None
