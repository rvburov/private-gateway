#!/usr/bin/env python3
"""Проверить смену внутреннего токена CLI и откат без учётных данных браузера."""

import importlib.util
import json
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
        self.panel_port = 8443

    def execute(self, command, *, env=None, timeout=120):
        self.calls.append(command)
        if command[-3:] == ["config", "--format", "json"]:
            return json.dumps({"services": {
                "api": {"environment": {
                    "GATEWAY_BROWSER_PUBLIC_URL": f"https://vpn.example.test:{self.panel_port}/browser/vnc.html",
                    "GATEWAY_PORT": "8443", "GATEWAY_XRAY_PORT": "443"}},
                "edge": {"ports": [{"published": "8443", "target": 443},
                                   {"published": "443", "target": 443}]},
            }})
        return ""

    def rotate(self, verify):
        return rotation.rotate(self.env, self.compose, self.backups,
                               self.execute, verify)

    def test_rotates_only_internal_cli_token_and_keeps_secrets_private(self):
        observed = []
        result = self.rotate(lambda new, old, domain, port: observed.append((new, old, domain, port)))
        new_token, old_token, domain, port = observed[0]
        self.assertEqual(port, 8443)
        self.assertIn("https://vpn.example.test:8443/", result)
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

    def test_legacy_panel_port_is_taken_from_installed_release(self):
        self.panel_port = 443
        checked = []
        result = self.rotate(lambda *args: checked.append(args))
        self.assertEqual(checked[0][-1], 443)
        self.assertIn("https://vpn.example.test/", result)
        self.assertNotIn("https://vpn.example.test:8443/", result)

    def test_failed_https_verification_restores_file_and_api(self):
        def verify(new, old, domain, port):
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
            rotation.verify_access("2" * 64, self.old_token, "vpn.example.test", 8443)
        self.assertEqual(len(checked), 3)
        self.assertTrue(all(url.startswith("https://vpn.example.test:8443/") for url, _ in checked))
        self.assertEqual(checked[-1][1], "")
        with patch.object(rotation, "http_status", return_value=200):
            with self.assertRaisesRegex(rotation.RotationError, "HTTPS checks"):
                rotation.verify_access("2" * 64, self.old_token, "vpn.example.test", 8443)


if __name__ == "__main__":
    unittest.main()
