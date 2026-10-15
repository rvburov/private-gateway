#!/usr/bin/env python3
"""Read and validate an Actions release artifact without extracting arbitrary paths."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile
import zipfile

MAX_ARCHIVE = 256 * 1024 * 1024
MAX_EXPANDED = 1024 * 1024 * 1024
REQUIRED = {"bin/gateway-api", "bin/gatewayctl", "bin/xray", "web/index.html",
            "browser/start.sh", "browser/healthcheck.sh", "browser/smoke.py",
            "docker/compose.yml", "docker/Caddyfile.template", "docker/Dockerfile.api",
            "docker/Dockerfile.xray", "docker/Dockerfile.browser", "docker/browser-worker.py",
            "docker/xray-worker.py", "docker/wait-xray.py", "docker/vpn-smoke.py",
            "docker/change-reality-target.py", "docker/rotate-admin-token.py", "docker/admin-credentials.py", "docker/install.sh",
            "docker/update.sh", "docker/verify-release.py"}


def canonical_path(name):
    path = PurePosixPath(name)
    return (bool(name) and not path.is_absolute() and "\\" not in name and "\0" not in name
            and all(part not in ("", ".", "..") for part in name.split("/")))


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate manifest key")
        result[key] = value
    return result


def verify_tar(path, version, architecture="amd64"):
    if path.stat().st_size > MAX_ARCHIVE:
        raise ValueError("Release exceeds the size limit")
    with tarfile.open(path, "r:gz") as archive:
        members = {}
        size = 0
        for member in archive:
            name = member.name.rstrip("/") if member.isdir() else member.name
            if not canonical_path(name) or name in members or not (member.isfile() or member.isdir()):
                raise ValueError("Invalid, duplicate, or non-regular release entry")
            if name != "manifest.json" and name not in ("bin", "web", "browser", "docker") and not name.startswith(("bin/", "web/", "browser/", "docker/")):
                raise ValueError("Unexpected release path")
            members[name] = member
            size += member.size
            if len(members) > 10000 or size > MAX_EXPANDED:
                raise ValueError("Expanded release exceeds the size limit")
        manifest_member = members.get("manifest.json")
        if not manifest_member or not manifest_member.isfile() or manifest_member.size > 2 * 1024 * 1024:
            raise ValueError("Missing or oversized manifest")
        with archive.extractfile(manifest_member) as source:
            manifest = json.load(source, object_pairs_hook=unique_object)
        if (not isinstance(manifest, dict) or set(manifest) != {"version", "os", "arch", "files"}
                or manifest["version"] != version or manifest["os"] != "linux"
                or manifest["arch"] != architecture or not isinstance(manifest["files"], dict)):
            raise ValueError("Release identity does not match the successful CI run")
        files = {name: member for name, member in members.items() if member.isfile() and name != "manifest.json"}
        if set(files) != set(manifest["files"]) or not REQUIRED <= files.keys():
            raise ValueError("Release file list does not match its manifest")
        if {name for name in files if name.startswith("bin/")} != {"bin/gateway-api", "bin/gatewayctl", "bin/xray"}:
            raise ValueError("Unexpected release executable")
        for name, member in files.items():
            digest = manifest["files"][name]
            if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise ValueError("Invalid file checksum")
            with archive.extractfile(member) as source:
                actual = hashlib.file_digest(source, "sha256").hexdigest()
            if actual != digest:
                raise ValueError(f"Release file checksum mismatch: {name}")


def unpack_artifact(artifact, output, version, architecture="amd64"):
    if not re.fullmatch(r"[0-9a-f]{40}", version):
        raise ValueError("Expected a full Git commit SHA")
    with zipfile.ZipFile(artifact) as archive:
        entries = {}
        total_size = 0
        for member in archive.infolist():
            name = member.filename.rstrip("/") if member.is_dir() else member.filename
            mode = member.external_attr >> 16
            kind = stat.S_IFMT(mode)
            if (not canonical_path(name) or name in entries or member.flag_bits & 1
                    or kind not in (0, stat.S_IFREG, stat.S_IFDIR)
                    or (kind == stat.S_IFDIR and not member.is_dir())):
                raise ValueError("Invalid artifact ZIP entry")
            if name not in ("release.tar.gz", "release.tar.gz.sha256", "install") and not name.startswith("install/"):
                raise ValueError("Unexpected artifact ZIP path")
            entries[name] = member
            total_size += member.file_size
            if len(entries) > 10000 or total_size > MAX_ARCHIVE + 16 * 1024 * 1024:
                raise ValueError("Artifact ZIP exceeds the size limit")
        release = entries.get("release.tar.gz")
        checksum = entries.get("release.tar.gz.sha256")
        if not release or not checksum or release.is_dir() or checksum.is_dir() or checksum.file_size > 1024:
            raise ValueError("Release archive or checksum is missing")
        checksum_text = archive.read(checksum).decode("ascii")
        if not re.fullmatch(r"[0-9a-f]{64}  release\.tar\.gz\n?", checksum_text):
            raise ValueError("Invalid archive checksum file")
        content = archive.read(release)
        if len(content) > MAX_ARCHIVE or hashlib.sha256(content).hexdigest() != checksum_text[:64]:
            raise ValueError("Archive checksum mismatch")
        output.mkdir(parents=True, exist_ok=True)
        target = output / "release.tar.gz"
        # A private fresh Actions directory is expected; never overwrite a symlink.
        with target.open("xb") as destination:
            destination.write(content)
    verify_tar(target, version, architecture)
    return target


def verify_standalone_tar(path, architecture="amd64"):
    with tarfile.open(path, "r:gz") as archive:
        member = archive.getmember("manifest.json")
        if not member.isfile() or member.size > 2 * 1024 * 1024:
            raise ValueError("Invalid release manifest")
        with archive.extractfile(member) as source:
            version = json.load(source, object_pairs_hook=unique_object).get("version")
    if not isinstance(version, str) or not re.fullmatch(r"[0-9a-f]{40}", version):
        raise ValueError("Invalid release version")
    verify_tar(path, version, architecture)
    return version


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path)
    parser.add_argument("--version")
    parser.add_argument("--tar", action="store_true", help="Verify a standalone release archive")
    parser.add_argument("--arch", choices=("amd64",), default="amd64")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.tar:
        version = verify_standalone_tar(args.artifact, args.arch)
        print(version)
    else:
        if not args.version or not args.output:
            parser.error("--version and --output are required for an Actions artifact")
        path = unpack_artifact(args.artifact, args.output, args.version, args.arch)
        print(f"Verified release {args.version}: {path}")


if __name__ == "__main__":
    main()
