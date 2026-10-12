"""Exercise the release trust boundary and failure handling without a VPS."""

import contextlib
import gzip
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
SOURCE = Path(__file__).resolve().parents[2] / "deploy/production/private-gateway-deploy.py"
SPEC = importlib.util.spec_from_file_location("private_gateway_deploy", SOURCE)
deploy = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = deploy
SPEC.loader.exec_module(deploy)

VERSION = "a" * 40
ELF = b"\x7fELF\x02\x01" + bytes(12) + b"\x3e\x00" + bytes(16)


def contents():
    return {name: ELF if name.startswith("bin/") else b"fixture" for name in deploy.REQUIRED}


def archive(files=None, *, extra=None, corrupt=None):
    files = files or contents()
    manifest = {"version": VERSION, "os": "linux", "arch": "amd64",
                "files": {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}}
    payload = {"manifest.json": json.dumps(manifest).encode(), **files}
    with io.BytesIO() as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb") as zipped:
            with tarfile.open(fileobj=zipped, mode="w", format=tarfile.USTAR_FORMAT) as tar:
                for name, data in payload.items():
                    entry = tarfile.TarInfo(name)
                    entry.size = len(data)
                    tar.addfile(entry, io.BytesIO(data))
                if extra:
                    name, kind, data = extra
                    entry = tarfile.TarInfo(name)
                    entry.type = kind
                    entry.size = len(data) if kind == tarfile.REGTYPE else 0
                    tar.addfile(entry, io.BytesIO(data) if kind == tarfile.REGTYPE else None)
        result = raw.getvalue()
    return corrupt(result) if corrupt else result


class ReleaseTrustBoundary(unittest.TestCase):
    def check(self, raw):
        with tempfile.TemporaryDirectory() as temporary:
            return deploy.unpack_verified(io.BytesIO(raw), Path(temporary))

    def test_valid_release_is_accepted(self):
        self.assertEqual(self.check(archive())["version"], VERSION)

    def test_corrupted_payload_is_rejected(self):
        raw = archive()
        with self.assertRaises(deploy.DeploymentError):
            self.check(raw[: len(raw) // 2])

    def test_unlisted_payload_is_rejected(self):
        with self.assertRaises(deploy.DeploymentError):
            self.check(archive(extra=("web/unlisted.js", tarfile.REGTYPE, b"console.log(1)")))

    def test_duplicate_payload_is_rejected(self):
        with self.assertRaises(deploy.DeploymentError):
            self.check(archive(extra=("web/index.html", tarfile.REGTYPE, b"changed")))

    def test_path_traversal_is_rejected(self):
        with self.assertRaises(deploy.DeploymentError):
            self.check(archive(extra=("web/../../etc/shadow", tarfile.REGTYPE, b"x")))

    def test_symlink_is_rejected(self):
        with self.assertRaises(deploy.DeploymentError):
            self.check(archive(extra=("web/link", tarfile.SYMTYPE, b"")))

    def test_invalid_binary_is_rejected(self):
        files = contents()
        files["bin/gateway-api"] = b"#!/bin/sh\n"
        with self.assertRaises(deploy.DeploymentError):
            self.check(archive(files))


class FailureState(unittest.TestCase):
    def test_browser_build_failure_does_not_stop_api(self):
        class Runtime:
            def __init__(self):
                self.actions = []

            def build_browser(self, release):
                self.actions.append("build")
                raise deploy.DeploymentError("image unavailable")

            def service(self, action, unit):
                self.actions.append((action, unit))

        runtime = Runtime()
        manager = deploy.Deployer(runtime=runtime)
        with mock.patch.object(deploy, "trusted_file", return_value=contextlib.nullcontext()):
            with self.assertRaisesRegex(deploy.DeploymentError, "image unavailable"):
                manager.deploy(Path("/opt/private-gateway/releases") / VERSION)
        self.assertEqual(runtime.actions, ["build"])


if __name__ == "__main__":
    unittest.main()
