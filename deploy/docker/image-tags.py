#!/usr/bin/env python3
"""Print a content tag for each image a release builds, as NAME=TAG lines for stack.env.

A tag hashes the Dockerfile and every release file its COPY instructions take. An image keeps
its tag while none of that changes, so a deployment leaves its container running.
"""

import hashlib
from pathlib import Path, PurePosixPath
import sys

IMAGES = (("PG_API_IMAGE", "Dockerfile.api"), ("PG_XRAY_IMAGE", "Dockerfile.xray"),
          ("PG_BROWSER_IMAGE", "Dockerfile.browser"))


def copy_sources(dockerfile):
    sources = []
    for line in dockerfile.read_text().splitlines():
        words = line.split()
        if not words or words[0].upper() not in ("COPY", "ADD"):
            continue
        arguments = [word for word in words[1:] if not word.startswith("--")]
        if (words[0].upper() == "ADD" or "[" in line or len(arguments) < 2
                or any(word.startswith("--from") for word in words)):
            raise SystemExit(f"{dockerfile.name}: unsupported instruction: {line.strip()}")
        sources += arguments[:-1]
    return sources


def files_of(root, source):
    path = PurePosixPath(source.rstrip("/"))
    if path.is_absolute() or any(part in ("", ".", "..") for part in path.parts):
        raise SystemExit(f"COPY source outside the release: {source}")
    target = root / path
    if target.is_symlink():
        raise SystemExit(f"COPY source is a symlink: {source}")
    if target.is_file():
        return [target]
    found = sorted(item for item in target.rglob("*") if item.is_file()) if target.is_dir() else []
    if not found:
        raise SystemExit(f"COPY source is missing: {source}")
    return found


def content_tag(root, name):
    dockerfile = root / "docker" / name
    digest = hashlib.sha256(b"private-gateway image tag v1\0")
    digest.update(hashlib.sha256(dockerfile.read_bytes()).digest())
    files = {item for source in copy_sources(dockerfile) for item in files_of(root, source)}
    for item in sorted(files):
        digest.update(item.relative_to(root).as_posix().encode() + b"\0")
        digest.update(hashlib.sha256(item.read_bytes()).digest())
    return digest.hexdigest()[:32]


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: image-tags.py RELEASE_DIRECTORY")
    root = Path(sys.argv[1])
    for variable, name in IMAGES:
        print(f"{variable}={content_tag(root, name)}")


if __name__ == "__main__":
    main()
