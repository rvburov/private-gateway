#!/usr/bin/env python3
"""Exercise the production target repair without touching Docker or the VPS."""

import importlib.util
from pathlib import Path
import tempfile
import unittest


MODULE = Path(__file__).resolve().parents[1] / "deploy/docker/change-reality-target.py"
SPEC = importlib.util.spec_from_file_location("change_reality_target", MODULE)
repair = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(repair)


class TargetRepairTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = self.root / "stack.env"
        self.env.write_text("GATEWAY_API_TOKEN=private-test-value\n"
                            "GATEWAY_SERVER_NAME=www.microsoft.com\n"
                            "GATEWAY_REALITY_TARGET=www.microsoft.com:443\n")
        self.original = self.env.read_bytes()
        self.compose = self.root / "compose.yml"
        self.compose.write_text("services: {}\n")
        self.smoke = self.root / "vpn-smoke.py"
        self.smoke.write_text("# dummy\n")
        self.xray = self.root / "xray.json"
        self.xray.write_text('{"privateKey":"private-test-value"}')
        self.backups = self.root / "backups"

    def migrate(self, execute):
        return repair.migrate(self.env, self.compose, self.smoke, self.backups, self.xray, execute)

    def test_success_preserves_secrets_and_creates_private_backup(self):
        seen = []

        def execute(command, timeout):
            seen.append(command)
            return "VLESS traffic OK: TLSv1.3\n" if command[0].endswith("python3") else ""

        result = self.migrate(execute)
        self.assertIn("VLESS traffic OK: TLSv1.3", result)
        self.assertIn("GATEWAY_API_TOKEN=private-test-value", self.env.read_text())
        self.assertIn("GATEWAY_SERVER_NAME=www.cloudflare.com", self.env.read_text())
        self.assertIn("GATEWAY_REALITY_TARGET=www.cloudflare.com:443", self.env.read_text())
        self.assertEqual([command[-3:] for command in seen if command[-3:] ==
                          ["--isolated", "--isolated-target", "www.cloudflare.com"]],
                         [["--isolated", "--isolated-target", "www.cloudflare.com"]])
        self.assertEqual(sum("--force-recreate" in command for command in seen), 1)
        backup = next(self.backups.iterdir())
        self.assertEqual((backup / "stack.env").read_bytes(), self.original)
        self.assertEqual((backup / "xray.json").read_text(), self.xray.read_text())
        self.assertEqual((backup / "stack.env").stat().st_mode & 0o777, 0o600)

        seen.clear()
        self.assertIn("already configured", self.migrate(execute))
        self.assertFalse(any("--force-recreate" in command for command in seen))

    def test_failed_apply_restores_env_and_reapplies_old_configuration(self):
        seen = []

        def execute(command, timeout):
            seen.append(command)
            if len(command) >= 3 and command[-3].endswith("/gatewayctl") and command[-2:] == ["config", "apply"] and len(seen) < 6:
                raise repair.MigrationError("simulated apply failure")
            return "VLESS traffic OK: TLSv1.3\n" if command[0].endswith("python3") else ""

        with self.assertRaisesRegex(repair.MigrationError, "previous settings were restored"):
            self.migrate(execute)
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertEqual(sum("--force-recreate" in command for command in seen), 2)
        self.assertEqual(sum(len(command) >= 3 and command[-3].endswith("/gatewayctl")
                             and command[-2:] == ["config", "apply"]
                             for command in seen), 2)

    def test_failed_isolated_check_does_not_edit_or_backup(self):
        def execute(command, timeout):
            raise repair.MigrationError("temporary check failed")

        with self.assertRaisesRegex(repair.MigrationError, "temporary check failed"):
            self.migrate(execute)
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertFalse(self.backups.exists())


if __name__ == "__main__":
    unittest.main()
