#!/usr/bin/env python3
"""Test terminal setup, secret handling and rollback without changing the VPS."""

import importlib.util
import contextlib
import io
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
        self.env.write_text("PANEL_DOMAIN=example.test\nGATEWAY_API_TOKEN=old-token\n")
        self.original = self.env.read_bytes()
        self.compose = self.root / "compose.yml"
        self.compose.write_text("services: {}\n")
        self.backups = self.root / "backups"

    def invoke(self, check, execute):
        original_lstat = Path.lstat
        def root_owned(path):
            if path == self.env:
                return types.SimpleNamespace(st_mode=stat.S_IFREG | 0o600, st_uid=0)
            return original_lstat(path)
        secret = b"12345678901234567890"
        with patch.object(Path, "lstat", root_owned), \
             patch.object(admin, "input", side_effect=["admin", admin.code_at_step(secret, 1)], create=True), \
             patch.object(admin.getpass, "getpass", side_effect=["very-long-password", "very-long-password"]), \
             patch.object(admin.secrets, "token_bytes", side_effect=[b"0123456789abcdef", secret]), \
             patch.object(admin.time, "time", return_value=59):
            with contextlib.redirect_stdout(io.StringIO()):
                return admin.setup(self.env, self.compose, self.backups, check, execute)

    def test_setup_preserves_other_secrets_and_verifies_login(self):
        calls = []
        checked = []
        result = self.invoke(lambda *args: checked.append(args), lambda command: calls.append(command))
        self.assertIn("enabled", result)
        self.assertNotIn("very-long-password", result)
        self.assertIn(b"GATEWAY_API_TOKEN=old-token", self.env.read_bytes())
        self.assertIn(b"GATEWAY_ADMIN_USERNAME=admin", self.env.read_bytes())
        self.assertIn(b"GATEWAY_ADMIN_PASSWORD_HASH=pbkdf2_sha256:600000:", self.env.read_bytes())
        self.assertEqual(checked[0][0:2], ("example.test", "admin"))
        self.assertEqual(checked[0][3], admin.code_at_step(b"12345678901234567890", 1))
        self.assertEqual((next(self.backups.iterdir()) / "stack.env").read_bytes(), self.original)
        self.assertEqual(sum("--force-recreate" in command for command in calls), 1)

    def test_failed_verification_restores_original_env(self):
        calls = []
        def failed(*args):
            raise admin.SetupError("simulated HTTPS failure")
        with self.assertRaisesRegex(admin.SetupError, "previous access restored"):
            self.invoke(failed, lambda command: calls.append(command))
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertEqual(sum("--force-recreate" in command for command in calls), 2)

    def test_invalid_authenticator_code_does_not_change_env(self):
        self.assertFalse(admin.valid_code(b"12345678901234567890", "000000"))
        self.assertEqual(self.env.read_bytes(), self.original)


if __name__ == "__main__":
    unittest.main()
