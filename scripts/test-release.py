#!/usr/bin/env python3
"""Прогнать границы доверия релиза на правильных и намеренно испорченных артефактах."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import sys
import tarfile
import tempfile
import unittest
import zipfile

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("verify_release", Path(__file__).with_name("verify-release.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)
VERSION = "a" * 40


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)

    def artifact(self, *, payload_change=None, manifest_change=None, extra_tar=None,
                 extra_zip=None, corrupt_checksum=False, manifest_bytes=None):
        payload = {name: name.encode() for name in verifier.REQUIRED}
        manifest = {"version": VERSION, "os": "linux", "arch": "amd64",
                    "files": {name: hashlib.sha256(content).hexdigest() for name, content in payload.items()}}
        if payload_change:
            payload_change(payload)
        if manifest_change:
            manifest_change(manifest)
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w:gz", format=tarfile.USTAR_FORMAT) as archive:
            entries = {"manifest.json": manifest_bytes or json.dumps(manifest).encode(), **payload}
            for name, content in entries.items():
                member = tarfile.TarInfo(name)
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
            if extra_tar:
                extra_tar(archive)
        release = raw.getvalue()
        checksum = "0" * 64 if corrupt_checksum else hashlib.sha256(release).hexdigest()
        artifact = self.directory / "artifact.zip"
        with zipfile.ZipFile(artifact, "w") as archive:
            archive.writestr("release.tar.gz", release)
            archive.writestr("release.tar.gz.sha256", f"{checksum}  release.tar.gz\n")
            archive.writestr("install/install.sh", "installer is not extracted or executed")
            if extra_zip:
                extra_zip(archive)
        return artifact

    def verify(self, **kwargs):
        return verifier.unpack_artifact(self.artifact(**kwargs), self.directory / "output", VERSION)

    def test_valid_artifact_only_extracts_release(self):
        target = self.verify()
        self.assertEqual(target.name, "release.tar.gz")
        self.assertEqual([p.name for p in target.parent.iterdir()], ["release.tar.gz"])

    def test_archive_checksum_mismatch(self):
        with self.assertRaisesRegex(ValueError, "Archive checksum mismatch"):
            self.verify(corrupt_checksum=True)

    def test_payload_checksum_mismatch(self):
        with self.assertRaisesRegex(ValueError, "file checksum mismatch"):
            self.verify(payload_change=lambda files: files.update({"bin/gateway-api": b"tampered"}))

    def test_stale_release(self):
        with self.assertRaisesRegex(ValueError, "identity"):
            self.verify(manifest_change=lambda manifest: manifest.update(version="b" * 40))

    def test_wrong_architecture(self):
        with self.assertRaisesRegex(ValueError, "identity"):
            self.verify(manifest_change=lambda manifest: manifest.update(arch="arm64"))

    def test_unlisted_payload(self):
        with self.assertRaisesRegex(ValueError, "file list"):
            self.verify(payload_change=lambda files: files.update({"web/extra.js": b"unlisted"}))

    def test_extra_executable(self):
        def add_to_manifest(manifest):
            manifest["files"]["bin/extra"] = hashlib.sha256(b"extra").hexdigest()

        with self.assertRaisesRegex(ValueError, "Unexpected release executable"):
            self.verify(payload_change=lambda files: files.update({"bin/extra": b"extra"}),
                        manifest_change=add_to_manifest)

    def test_tar_symlink(self):
        def add_symlink(archive):
            member = tarfile.TarInfo("web/link")
            member.type = tarfile.SYMTYPE
            member.linkname = "/etc/shadow"
            archive.addfile(member)

        with self.assertRaisesRegex(ValueError, "non-regular"):
            self.verify(extra_tar=add_symlink)

    def test_tar_traversal(self):
        with self.assertRaisesRegex(ValueError, "Invalid"):
            self.verify(extra_tar=lambda archive: archive.addfile(tarfile.TarInfo("web/../../outside")))
        self.assertFalse((self.directory / "outside").exists())

    def test_duplicate_tar_entry(self):
        with self.assertRaisesRegex(ValueError, "duplicate"):
            self.verify(extra_tar=lambda archive: archive.addfile(tarfile.TarInfo("web/index.html")))

    def test_zip_traversal(self):
        with self.assertRaisesRegex(ValueError, "Invalid artifact ZIP"):
            self.verify(extra_zip=lambda archive: archive.writestr("install/../../outside", b"blocked"))
        self.assertFalse((self.directory / "outside").exists())

    def test_zip_symlink_even_in_ignored_installer(self):
        def add_symlink(archive):
            member = zipfile.ZipInfo("install/link")
            member.create_system = 3
            member.external_attr = (stat.S_IFLNK | 0o777) << 16
            archive.writestr(member, "/etc/shadow")

        with self.assertRaisesRegex(ValueError, "Invalid artifact ZIP"):
            self.verify(extra_zip=add_symlink)

    def test_duplicate_manifest_key(self):
        with self.assertRaisesRegex(ValueError, "Duplicate manifest key"):
            self.verify(manifest_bytes=b'{"version":"first","version":"second"}')

    def test_refuses_existing_output_file(self):
        output = self.directory / "output"
        output.mkdir()
        target = output / "release.tar.gz"
        target.write_bytes(b"existing")
        with self.assertRaises(FileExistsError):
            self.verify()
        self.assertEqual(target.read_bytes(), b"existing")


if __name__ == "__main__":
    unittest.main()
