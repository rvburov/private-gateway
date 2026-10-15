#!/usr/bin/env python3
"""Test terminal password setup and rollback without changing a VPS."""

import importlib.util
import json
from pathlib import Path
import stat
import tempfile
import types
import unittest
from unittest.mock import patch

MODULE = Path(__file__).resolve().parents[1] / "deploy/docker/admin-credentials.py"
SPEC = importlib.util.spec_from_file_location("admin_credentials", MODULE)
admin = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(admin)


class CredentialsTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = self.root / "stack.env"
        self.env.write_text("PANEL_DOMAIN=example.test\nGATEWAY_API_TOKEN=old-token\n"
                            "GATEWAY_ADMIN_TOTP_SECRET=old-exposed-seed\n")
        self.original = self.env.read_bytes()
        self.compose = self.root / "compose.yml"
        self.compose.write_text("services: {}\n")
        self.backups = self.root / "backups"
        self.calls = []
        self.saved = False

    def execute(self, command, stdin=None):
        self.calls.append((command, stdin))
        if command[-1] == "admin-name":
            return b"old-admin\n"
        if command[-1] == "admin-backup":
            if self.saved:
                return b'{"exists":true,"username":"romanburov89@gmail.com","password_hash":"new-hash","generation":4}\n'
            return b'{"exists":true,"username":"old-admin","password_hash":"old-hash","generation":3}\n'
        if command[-1] == "admin-set":
            self.new_credentials = json.loads(stdin)
            self.saved = True
        if command[-1] == "admin-restore":
            self.saved = False
        return b""

    def invoke(self, check, execute=None):
        original_lstat = Path.lstat
        def root_owned(path):
            if path == self.env:
                return types.SimpleNamespace(st_mode=stat.S_IFREG | 0o600, st_uid=0)
            return original_lstat(path)
        def dump(_command, path):
            path.write_bytes(b"test database backup")
        with patch.object(Path, "lstat", root_owned), \
             patch.object(admin, "input", return_value="romanburov89@gmail.com", create=True), \
             patch.object(admin.getpass, "getpass", side_effect=["very-long-password", "very-long-password"]):
            return admin.setup(self.env, self.compose, self.backups, execute or self.execute, check, dump)

    def test_password_is_stored_in_db_and_authenticator_is_enrolled_in_browser(self):
        checked = []
        result = self.invoke(lambda *args: checked.append(args))
        self.assertIn("PostgreSQL", result)
        self.assertEqual(checked, [("example.test", "romanburov89@gmail.com", "very-long-password")])
        self.assertEqual(self.new_credentials["username"], "romanburov89@gmail.com")
        self.assertTrue(self.new_credentials["password_hash"].startswith("pbkdf2_sha256:600000:"))
        self.assertNotIn("very-long-password", json.dumps(self.new_credentials))
        self.assertNotIn(b"GATEWAY_ADMIN_TOTP_SECRET=", self.env.read_bytes())
        self.assertIn(b"GATEWAY_API_TOKEN=old-token", self.env.read_bytes())
        backup = next(self.backups.iterdir())
        self.assertEqual((backup / "stack.env").read_bytes(), self.original)
        self.assertTrue((backup / "gateway.dump").read_bytes())
        self.assertEqual((backup / "administrator.json").stat().st_mode & 0o777, 0o600)
        self.assertFalse(any(command[-1] == "admin-restore" for command, _ in self.calls))

    def test_failed_verification_restores_database_and_environment(self):
        def failed(*_args):
            raise admin.SetupError("simulated HTTPS failure")
        with self.assertRaisesRegex(admin.SetupError, "previous access restored"):
            self.invoke(failed)
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertTrue(any(command[-1] == "admin-restore" for command, _ in self.calls))
        self.assertEqual(sum("--force-recreate" in command for command, _ in self.calls), 2)

    def test_failed_write_before_database_change_keeps_old_credentials(self):
        def fail_set(command, stdin=None):
            if command[-1] == "admin-set":
                raise admin.SetupError("simulated Docker failure")
            return self.execute(command, stdin)
        with self.assertRaisesRegex(admin.SetupError, "previous access restored"):
            self.invoke(lambda *_args: None, fail_set)
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertFalse(any(command[-1] == "admin-restore" for command, _ in self.calls))


if __name__ == "__main__":
    unittest.main()
