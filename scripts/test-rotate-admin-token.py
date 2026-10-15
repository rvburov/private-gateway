#!/usr/bin/env python3
"""Test that a failed credential rotation restores both access paths."""

import base64
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
        self.caddyfile = self.root / "Caddyfile"
        self.caddyfile.write_text("basic_auth {\n    browser $2a$12$" + "a" * 53 + "\n}\n")
        self.old_env = self.env.read_bytes()
        self.old_caddyfile = self.caddyfile.read_bytes()
        self.compose = self.root / "compose.yml"
        self.compose.write_text("services: {}\n")
        self.backups = self.root / "backups"
        self.calls = []

    def execute(self, command, *, env=None, timeout=120):
        self.calls.append(command)
        if command[:2] == ["docker", "run"]:
            self.assertRegex(env["CADDY_PASSWORD"], r"^[0-9a-f]{64}$")
            self.assertNotEqual(env["CADDY_PASSWORD"], self.old_token)
            return "$2a$12$" + "b" * 53
        return ""

    def rotate(self, verify):
        return rotation.rotate(self.env, self.caddyfile, self.compose, self.backups,
                               self.execute, verify)

    def test_rotates_api_and_browser_together_and_keeps_secrets_private(self):
        observed = []

        def verify(new, old, domain):
            observed.append((new, old, domain))

        result = self.rotate(verify)
        new_token, old_token, domain = observed[0]
        self.assertNotIn(new_token, result)
        self.assertEqual(old_token, self.old_token)
        self.assertEqual(domain, "vpn.example.test")
        self.assertEqual(rotation.env_value(self.env.read_bytes(), "GATEWAY_API_TOKEN"), new_token)
        self.assertIn(b"GATEWAY_REALITY_PRIVATE_KEY=unchanged-key", self.env.read_bytes())
        self.assertIn(b"browser $2a$12$" + b"b" * 53, self.caddyfile.read_bytes())
        backup = next(self.backups.iterdir())
        self.assertEqual((backup / "stack.env").read_bytes(), self.old_env)
        self.assertEqual((backup / "Caddyfile").read_bytes(), self.old_caddyfile)
        self.assertEqual((backup / "stack.env").stat().st_mode & 0o777, 0o600)
        self.assertEqual(sum("--force-recreate" in command for command in self.calls), 1)

    def test_failed_https_verification_restores_both_files_and_containers(self):
        def verify(new, old, domain):
            raise rotation.RotationError("simulated HTTPS failure")

        with self.assertRaisesRegex(rotation.RotationError, "previous access was restored"):
            self.rotate(verify)
        self.assertEqual(self.env.read_bytes(), self.old_env)
        self.assertEqual(self.caddyfile.read_bytes(), self.old_caddyfile)
        self.assertEqual(sum("--force-recreate" in command for command in self.calls), 2)

    def test_https_verification_requires_new_access_and_rejects_old_access(self):
        checked = []
        old_basic = "Basic " + base64.b64encode(("browser:" + self.old_token).encode()).decode()

        def status(url, authorization):
            checked.append((url, authorization))
            if authorization.startswith("Bearer ") or authorization == old_basic:
                return 401
            return 502 if url.endswith("/browser/vnc.html") else 200

        with patch.object(rotation, "http_status", side_effect=status):
            rotation.verify_access("2" * 64, self.old_token, "vpn.example.test")
        self.assertEqual(len(checked), 4)
        with patch.object(rotation, "http_status", return_value=200):
            with self.assertRaisesRegex(rotation.RotationError, "HTTPS checks"):
                rotation.verify_access("2" * 64, self.old_token, "vpn.example.test")

    def test_rotation_uses_internal_cli_with_public_token_login_disabled(self):
        self.rotate(lambda new, old, domain: None)
        self.assertTrue(any(any(item.endswith("/gatewayctl") for item in command) for command in self.calls))
        with patch.object(rotation, "http_status", side_effect=[401, 401, 502, 401]):
            rotation.verify_access("2" * 64, self.old_token, "vpn.example.test")


if __name__ == "__main__":
    unittest.main()
