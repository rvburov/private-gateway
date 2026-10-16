#!/usr/bin/env python3
"""Test internal CLI token rotation and rollback without browser credentials."""

import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


MODULE = Path(__file__).resolve().parents[1] / "deploy/docker/rotate-admin-token.py"
SPEC = importlib.util.spec_from_file_location("rotate_admin_token", MODULE)
rotation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rotation)


class RotationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = self.root / "stack.env"
        self.old_token = "1" * 64
        self.env.write_text("GATEWAY_API_TOKEN=" + self.old_token + "\n"
                            "PANEL_DOMAIN=vpn.example.test\n"
                            "GATEWAY_REALITY_PRIVATE_KEY=unchanged-key\n")
        self.old_env = self.env.read_bytes()
        self.compose = self.root / "compose.yml"
        self.compose.write_text("services: {}\n")
        self.backups = self.root / "backups"
        self.calls = []

    def execute(self, command, *, env=None, timeout=120):
        self.calls.append(command)
        return ""

    def rotate(self, verify):
        return rotation.rotate(self.env, self.compose, self.backups,
                               self.execute, verify)

    def test_rotates_only_internal_cli_token_and_keeps_secrets_private(self):
        observed = []
        result = self.rotate(lambda new, old, domain: observed.append((new, old, domain)))
        new_token, old_token, domain = observed[0]
        self.assertNotIn(new_token, result)
        self.assertEqual(old_token, self.old_token)
        self.assertEqual(domain, "vpn.example.test")
        self.assertEqual(rotation.env_value(self.env.read_bytes(), "GATEWAY_API_TOKEN"), new_token)
        self.assertIn(b"GATEWAY_REALITY_PRIVATE_KEY=unchanged-key", self.env.read_bytes())
        backup = next(self.backups.iterdir())
        self.assertEqual((backup / "stack.env").read_bytes(), self.old_env)
        self.assertEqual((backup / "stack.env").stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(backup.iterdir()), [backup / "stack.env"])
        self.assertEqual(sum("--force-recreate" in command for command in self.calls), 1)
        self.assertTrue(any(any(item.endswith("/gatewayctl") for item in command)
                            for command in self.calls))
        self.assertFalse(any("caddy" in command for command in self.calls))

    def test_failed_https_verification_restores_file_and_api(self):
        def verify(new, old, domain):
            raise rotation.RotationError("simulated HTTPS failure")

        with self.assertRaisesRegex(rotation.RotationError, "previous access was restored"):
            self.rotate(verify)
        self.assertEqual(self.env.read_bytes(), self.old_env)
        self.assertEqual(sum("--force-recreate" in command for command in self.calls), 2)

    def test_https_verification_denies_cli_token_and_unauthorized_browser(self):
        checked = []

        def status(url, authorization):
            checked.append((url, authorization))
            return 403 if url.endswith("/browser/vnc.html") else 401

        with patch.object(rotation, "http_status", side_effect=status):
            rotation.verify_access("2" * 64, self.old_token, "vpn.example.test")
        self.assertEqual(len(checked), 3)
        self.assertEqual(checked[-1][1], "")
        with patch.object(rotation, "http_status", return_value=200):
            with self.assertRaisesRegex(rotation.RotationError, "HTTPS checks"):
                rotation.verify_access("2" * 64, self.old_token, "vpn.example.test")


if __name__ == "__main__":
    unittest.main()
