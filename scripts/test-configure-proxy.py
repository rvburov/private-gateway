#!/usr/bin/env python3
"""Проверить закрытую подготовку параметров SOCKS5 без запуска служб."""

import base64
import importlib.util
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("configure_proxy", ROOT / "deploy/docker/configure-proxy.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ConfigureProxyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = self.root / "stack.env"
        self.env.write_text("GATEWAY_API_TOKEN=existing-value\n")
        os.chmod(self.env, 0o600)
        self.pending = self.root / "proxy.pending.env"
        self.backups = self.root / "backups"

    def configure(self, username="user's $name", password="пароль\\'$HOME", expected_ip=""):
        with mock.patch.object(MODULE, "require_private_file", side_effect=lambda path: path.stat()):
            return MODULE.configure("198.51.100.10", 1080, username, password,
                                    expected_ip=expected_ip,
                                    env=self.env, pending=self.pending, backups=self.backups)

    def test_hidden_credentials_round_trip_through_base64(self):
        old = self.env.read_bytes()
        message = self.configure(expected_ip="203.0.113.20")
        self.assertNotIn("user's", message)
        self.assertNotIn("пароль", message)
        self.assertNotIn("$HOME", message)
        self.assertEqual(self.env.read_bytes(), old)
        values = dict(line.split("=", 1) for line in self.pending.read_text().splitlines())
        self.assertEqual(values["GATEWAY_PROXY_AUTH"], "password")
        self.assertEqual(values["GATEWAY_PROXY_HOST"], "198.51.100.10")
        self.assertEqual(values["GATEWAY_PROXY_PORT"], "1080")
        self.assertEqual(values["GATEWAY_PROXY_EXPECTED_IP"], "203.0.113.20")
        self.assertEqual(base64.b64decode(values["GATEWAY_PROXY_USERNAME_B64"]).decode(), "user's $name")
        self.assertEqual(base64.b64decode(values["GATEWAY_PROXY_PASSWORD_B64"]).decode(), "пароль\\'$HOME")
        self.assertEqual(stat.S_IMODE(self.pending.stat().st_mode), 0o600)
        backup = next(self.backups.glob("proxy-*/stack.env"))
        self.assertEqual(backup.read_bytes(), old)
        self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o600)

    def test_invalid_input_does_not_change_file(self):
        original = self.env.read_bytes()
        for host, port, username, password in (("https://proxy.example", 1080, "user", "pass"),
                                               ("198.51.100.10", 0, "user", "pass"),
                                               ("198.51.100.10", 1080, "", "pass"),
                                               ("198.51.100.10", 1080, "user", "bad\npass")):
            with self.subTest(host=host, port=port):
                with self.assertRaises(MODULE.ConfigurationError):
                    MODULE.configure(host, port, username, password, env=self.env, backups=self.backups)
                self.assertEqual(self.env.read_bytes(), original)
                self.assertFalse(self.pending.exists())
        self.assertFalse(self.backups.exists())

    def test_explicit_noauth_clears_old_credentials(self):
        self.configure("old", "secret", expected_ip="203.0.113.20")
        with mock.patch.object(MODULE, "require_private_file", side_effect=lambda path: path.stat()):
            MODULE.configure("proxy.example", 1080, "", "", no_auth=True,
                             env=self.env, pending=self.pending, backups=self.backups)
        values = dict(line.split("=", 1) for line in self.pending.read_text().splitlines())
        self.assertEqual(values["GATEWAY_PROXY_AUTH"], "none")
        self.assertEqual(values["GATEWAY_PROXY_USERNAME_B64"], "")
        self.assertEqual(values["GATEWAY_PROXY_PASSWORD_B64"], "")
        self.assertEqual(values["GATEWAY_PROXY_EXPECTED_IP"], "")
        self.assertEqual(self.env.read_text(), "GATEWAY_API_TOKEN=existing-value\n")

    def test_invalid_expected_ip_does_not_change_file(self):
        old = self.env.read_bytes()
        with self.assertRaises(MODULE.ConfigurationError):
            self.configure(expected_ip="not-an-address")
        self.assertEqual(self.env.read_bytes(), old)
        self.assertFalse(self.pending.exists())
        self.assertFalse(self.backups.exists())

    def test_restage_replaces_only_pending_file_and_backs_up_previous_stage(self):
        self.configure("first", "secret", expected_ip="203.0.113.10")
        previous = self.pending.read_bytes()
        self.configure("second", "other", expected_ip="203.0.113.20")
        self.assertNotEqual(self.pending.read_bytes(), previous)
        self.assertEqual(self.env.read_text(), "GATEWAY_API_TOKEN=existing-value\n")
        backups = list(self.backups.glob("proxy-*/proxy.pending.env"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_bytes(), previous)
        self.assertEqual(stat.S_IMODE(backups[0].stat().st_mode), 0o600)


if __name__ == "__main__":
    unittest.main()
