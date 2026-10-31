#!/usr/bin/env python3
"""Проверить подготовку XHTTP, сохранение секретов и откат без рабочего сервера."""

import base64
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("configure_xhttp", ROOT / "deploy/docker/configure-xhttp.py")
xhttp = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(xhttp)
KEY = base64.urlsafe_b64encode(bytes(range(32))).rstrip(b"=").decode()
OLD_KEY = base64.urlsafe_b64encode(bytes(reversed(range(32)))).rstrip(b"=").decode()


class ConfigureXHTTPTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = self.root / "stack.env"
        self.original = (f"GATEWAY_REALITY_PRIVATE_KEY={OLD_KEY}\nGATEWAY_SHORT_ID=0123456789abcdef\n"
                         "GATEWAY_HOST=192.0.2.10\nPANEL_DOMAIN=example.com\nGATEWAY_PORT=8443\n"
                         "GATEWAY_XRAY_PORT=443\nGATEWAY_XRAY_STATS_PORT=10085\n").encode()
        self.env.write_bytes(self.original)
        self.env.chmod(0o600)
        (self.root / "release/docker").mkdir(parents=True)
        (self.root / "release/bin").mkdir()
        self.compose = self.root / "release/docker/compose.yml"
        self.compose.write_text("GATEWAY_XHTTP_ENABLED: ${GATEWAY_XHTTP_ENABLED:-0}\n")
        self.overlay = self.compose.parent / "compose.xhttp.yml"
        self.overlay.write_text("services:\n  xray:\n    ports: [\"8444:8444/tcp\"]\n")
        (self.compose.parent / "vpn-smoke.py").write_text("# Тестовая заглушка\n")
        (self.root / "release/bin/gateway-api").write_text("# Тестовая заглушка\n")
        self.config = self.root / "xray.json"
        self.config.write_bytes(b'{"inbounds": [{"tag": "vless-reality"}]}\n')
        self.config.chmod(0o600)
        self.calls = []

    def execute(self, command, timeout):
        self.calls.append(command)
        if command[-1] == "keygen":
            return json.dumps({"reality_private_key": KEY})
        if command[-1] == "status":
            return "ACTIVE DEVICES 1\n"
        if "vpn-smoke.py" in command[1]:
            return "VLESS traffic OK: TLSv1.3\n"
        return ""

    def configure(self, **kwargs):
        return xhttp.configure(env=self.env, compose=self.compose, xray_config=self.config,
                               backups=self.root / "backups", execute=self.execute, **kwargs)

    def test_preparation_preserves_old_values_and_never_returns_secrets(self):
        message = self.configure()
        values = xhttp.parse_env(self.env.read_bytes())
        self.assertEqual(values["GATEWAY_REALITY_PRIVATE_KEY"], OLD_KEY)
        self.assertEqual(values["GATEWAY_XHTTP_PRIVATE_KEY"], KEY)
        self.assertEqual(values["GATEWAY_XHTTP_ENABLED"], "1")
        self.assertEqual(values["GATEWAY_XHTTP_PORT"], "8444")
        self.assertRegex(values["GATEWAY_XHTTP_PATH"], r"^/[A-Za-z0-9_-]{16,128}$")
        self.assertEqual(len(values["GATEWAY_XHTTP_SHORT_ID"]), 16)
        for name in ("GATEWAY_XHTTP_PRIVATE_KEY", "GATEWAY_XHTTP_SHORT_ID", "GATEWAY_XHTTP_PATH"):
            self.assertNotIn(values[name], message)
        self.assertEqual(self.env.stat().st_mode & 0o777, 0o600)
        self.assertFalse(any("up" in command for command in self.calls))

    def test_repeat_retains_keys_path_and_does_not_generate_again(self):
        self.configure()
        prepared = self.env.read_bytes()
        self.calls.clear()
        self.assertIn("already prepared", self.configure())
        self.assertEqual(self.env.read_bytes(), prepared)
        self.assertEqual(self.calls, [])

    def test_apply_checks_both_transports_and_does_not_echo_values(self):
        self.configure(apply=True, port=9444)
        probes = [command[-1] for command in self.calls if "vpn-smoke.py" in command[1]]
        self.assertEqual(probes, ["tcp", "xhttp"])
        self.assertTrue(any(command[-2:] == ["api", "xray"] for command in self.calls))
        for command in self.calls:
            self.assertNotIn(KEY, command)
        self.assertEqual(xhttp.parse_env(self.env.read_bytes())["GATEWAY_XHTTP_PORT"], "9444")
        self.assertNotIn(str(self.overlay), self.calls[0])
        enabled_commands = [command for command in self.calls if "compose" in command and command[-1] != "status"]
        self.assertTrue(all(str(self.overlay) in command for command in enabled_commands))

    def test_disable_preserves_identity_and_checks_only_tcp(self):
        self.configure()
        before = xhttp.parse_env(self.env.read_bytes())
        self.calls.clear()
        self.configure(enabled=False, apply=True)
        after = xhttp.parse_env(self.env.read_bytes())
        self.assertEqual(after["GATEWAY_XHTTP_ENABLED"], "0")
        for name in ("GATEWAY_XHTTP_PRIVATE_KEY", "GATEWAY_XHTTP_SHORT_ID", "GATEWAY_XHTTP_PATH"):
            self.assertEqual(after[name], before[name])
        probes = [command[-1] for command in self.calls if "vpn-smoke.py" in command[1]]
        self.assertEqual(probes, ["tcp"])
        disabled_commands = [command for command in self.calls if "compose" in command and command[-1] != "status"]
        self.assertTrue(all(str(self.overlay) not in command for command in disabled_commands))

    def test_failed_probe_restores_env_and_configuration_then_applies_old_revision(self):
        config_before = self.config.read_bytes()
        original_execute = self.execute
        failed = False

        def execute(command, timeout):
            nonlocal failed
            if "vpn-smoke.py" in command[1] and command[-1] == "xhttp" and not failed:
                failed = True
                self.config.write_bytes(b'{"inbounds": [{"tag": "new-xhttp"}]}')
                raise xhttp.ConfigurationError("command output suppressed")
            return original_execute(command, timeout)

        self.execute = execute
        with self.assertRaisesRegex(xhttp.ConfigurationError, "previous settings restored") as caught:
            self.configure(apply=True)
        self.assertNotIn(KEY, str(caught.exception))
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertEqual(self.config.read_bytes(), config_before)
        self.assertEqual(sum(command[-2:] == ["config", "apply"] for command in self.calls), 2)
        self.assertEqual(self.calls[-1][-1], "tcp")
        starts = [command for command in self.calls if "up" in command]
        self.assertIn(str(self.overlay), starts[0])
        self.assertNotIn(str(self.overlay), starts[-1])

    def test_port_validation_and_invalid_existing_secret_leave_file_untouched(self):
        for port in (0, 65536, 80, 443, 8443, 10085, True, "bad"):
            with self.subTest(port=port), self.assertRaises(xhttp.ConfigurationError):
                self.configure(port=port)
            self.assertEqual(self.env.read_bytes(), self.original)
        self.env.write_bytes(self.original + b"GATEWAY_XHTTP_PRIVATE_KEY=not-a-key\n")
        with self.assertRaisesRegex(xhttp.ConfigurationError, "separate private key"):
            self.configure()

    def test_failed_disable_restores_enabled_overlay(self):
        self.configure()
        before = self.env.read_bytes()
        self.calls.clear()
        original_execute = self.execute
        failed = False

        def execute(command, timeout):
            nonlocal failed
            if command[-2:] == ["config", "apply"] and not failed:
                failed = True
                self.calls.append(command)
                raise xhttp.ConfigurationError("simulated disabled configuration failure")
            return original_execute(command, timeout)

        self.execute = execute
        with self.assertRaisesRegex(xhttp.ConfigurationError, "previous settings restored"):
            self.configure(enabled=False, apply=True)
        self.assertEqual(self.env.read_bytes(), before)
        starts = [command for command in self.calls if "up" in command]
        self.assertNotIn(str(self.overlay), starts[0])
        self.assertIn(str(self.overlay), starts[-1])
        self.assertEqual(self.calls[-1][-1], "xhttp")

    def test_duplicate_variables_and_old_release_are_rejected(self):
        self.env.write_bytes(self.original + b"GATEWAY_PORT=8443\n")
        with self.assertRaisesRegex(xhttp.ConfigurationError, "duplicate"):
            self.configure()
        self.env.write_bytes(self.original)
        self.compose.write_text("services: {}\n")
        with self.assertRaisesRegex(xhttp.ConfigurationError, "verified release"):
            self.configure()
        self.assertEqual(self.env.read_bytes(), self.original)

    def test_apply_without_active_device_does_not_change_configuration(self):
        original_execute = self.execute
        self.execute = lambda command, timeout: ("ACTIVE DEVICES 0\n" if command[-1] == "status"
                                                  else original_execute(command, timeout))
        with self.assertRaisesRegex(xhttp.ConfigurationError, "active device"):
            self.configure(apply=True)
        self.assertEqual(self.env.read_bytes(), self.original)
        self.assertFalse((self.root / "backups").exists())

    def test_custom_state_path_is_backed_up_and_absent_configuration_is_removed_on_failure(self):
        state = self.root / "custom-state"
        state.mkdir()
        self.env.write_bytes(self.original + f"GATEWAY_STATE_DIR={state}\n".encode())
        original_execute = self.execute

        def execute(command, timeout):
            if "vpn-smoke.py" in command[1] and command[-1] == "xhttp":
                (state / "xray.json").write_bytes(b"new temporary configuration")
                raise xhttp.ConfigurationError("failed test transport")
            return original_execute(command, timeout)

        before = self.env.read_bytes()
        with self.assertRaisesRegex(xhttp.ConfigurationError, "previous settings restored"):
            xhttp.configure(env=self.env, compose=self.compose, backups=self.root / "backups",
                            execute=execute, apply=True)
        self.assertEqual(self.env.read_bytes(), before)
        self.assertFalse((state / "xray.json").exists())

    def test_private_file_check_rejects_symlink_and_public_mode(self):
        self.env.chmod(0o644)
        with self.assertRaises(xhttp.ConfigurationError):
            xhttp.require_private_file(self.env)
        link = self.root / "linked.env"
        link.symlink_to(self.env)
        with self.assertRaises(xhttp.ConfigurationError):
            xhttp.require_private_file(link)

    def test_each_command_failure_reports_its_original_safe_stage(self):
        cases = {
            "preflight": lambda command: command[-1] == "status",
            "keygen": lambda command: command[-1] == "keygen",
            "compose-validation": lambda command: command[-2:] == ["config", "--quiet"],
            "compose-start": lambda command: "up" in command,
            "config-apply": lambda command: command[-2:] == ["config", "apply"],
            "tcp-smoke": lambda command: "vpn-smoke.py" in command[1] and command[-1] == "tcp",
            "xhttp-smoke": lambda command: "vpn-smoke.py" in command[1] and command[-1] == "xhttp",
        }
        execute_before = self.execute
        secret = "private-command-output /private-path-0123456789 203.0.113.99"
        config_before = self.config.read_bytes()
        for stage, matches in cases.items():
            with self.subTest(stage=stage):
                self.env.write_bytes(self.original)
                self.config.write_bytes(config_before)
                self.calls.clear()
                failed = False

                def execute(command, timeout):
                    nonlocal failed
                    if not failed and matches(command):
                        failed = True
                        raise RuntimeError(secret)
                    return execute_before(command, timeout)

                self.execute = execute
                with self.assertRaises(xhttp.ConfigurationError) as caught:
                    self.configure(apply=True)
                self.assertEqual(caught.exception.stage, stage)
                self.assertNotIn(secret, str(caught.exception))
                if stage not in ("preflight", "keygen"):
                    self.assertTrue(caught.exception.restored)
                    self.assertIn("previous settings restored", str(caught.exception))
                self.assertEqual(self.env.read_bytes(), self.original)
                output = io.StringIO()
                with contextlib.redirect_stderr(output):
                    xhttp.report_error(caught.exception, github_actions=True)
                self.assertIn("stage=" + stage, output.getvalue())
                self.assertNotIn(secret, output.getvalue())
                self.assertNotIn(str(self.root), output.getvalue())
                self.assertNotIn("previous settings", output.getvalue())
        self.execute = execute_before

    def test_failed_rollback_retains_original_and_restoration_stage(self):
        execute_before = self.execute
        original_failed = False
        secret = "private-command-result /random-secret-path-0123456789"

        def execute(command, timeout):
            nonlocal original_failed
            if "vpn-smoke.py" in command[1] and command[-1] == "xhttp":
                original_failed = True
                raise RuntimeError(secret)
            if original_failed and command[-2:] == ["config", "apply"]:
                raise RuntimeError(secret)
            return execute_before(command, timeout)

        self.execute = execute
        with self.assertRaisesRegex(xhttp.ConfigurationError, "configuration and rollback failed") as caught:
            self.configure(apply=True)
        self.assertEqual(caught.exception.stage, "xhttp-smoke")
        self.assertEqual(caught.exception.rollback_stage, "config-apply")
        output = io.StringIO()
        with contextlib.redirect_stderr(output):
            xhttp.report_error(caught.exception, github_actions=True)
        self.assertIn("stage=xhttp-smoke; rollback=failed; rollback_stage=config-apply", output.getvalue())
        self.assertNotIn(secret, output.getvalue())

    def test_rollback_file_failure_has_fixed_rollback_stage(self):
        execute_before = self.execute
        write_before = xhttp.atomic_write
        writes = 0

        def execute(command, timeout):
            if command[-2:] == ["config", "--quiet"]:
                raise RuntimeError("private-validation-output")
            return execute_before(command, timeout)

        def atomic_write(*args, **kwargs):
            nonlocal writes
            writes += 1
            if writes > 1:
                raise OSError("private-file-operation-details")
            return write_before(*args, **kwargs)

        self.execute = execute
        with mock.patch.object(xhttp, "atomic_write", side_effect=atomic_write):
            with self.assertRaises(xhttp.ConfigurationError) as caught:
                self.configure(apply=True)
        self.assertEqual(caught.exception.stage, "compose-validation")
        self.assertEqual(caught.exception.rollback_stage, "rollback")
        self.assertNotIn("private-file-operation-details", str(caught.exception))

    def test_annotation_never_uses_error_text_or_unknown_stage(self):
        secret = "secret%0A::error::forged-command-output"
        error = xhttp.ConfigurationError(secret, stage=secret, rollback_stage=secret)
        self.assertEqual(error.stage, "preflight")
        self.assertEqual(error.rollback_stage, "rollback")
        # Проверяется и повторная фильтрация, если поля исключения изменил сторонний код.
        error.stage = secret
        error.rollback_stage = secret
        output = io.StringIO()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}), contextlib.redirect_stderr(output):
            xhttp.report_error(error)
        self.assertEqual(output.getvalue(),
                         "::error file=deploy/docker/configure-xhttp.py::XHTTP configuration failed; "
                         "stage=preflight; rollback=failed; rollback_stage=rollback\n")
        self.assertNotIn(secret, output.getvalue())

    def test_normal_cli_has_safe_stage_and_keeps_restoration_message(self):
        output = io.StringIO()
        error = xhttp.ConfigurationError("configuration failed; previous settings restored", stage="tcp-smoke", restored=True)
        with contextlib.redirect_stderr(output):
            xhttp.report_error(error, github_actions=False)
        self.assertIn("stage=tcp-smoke; rollback=restored", output.getvalue())
        self.assertIn("previous settings restored", output.getvalue())
        self.assertNotIn("::error", output.getvalue())

    def test_invalid_keygen_output_reports_keygen_without_echoing_output(self):
        execute_before = self.execute
        secret = "private-malformed-keygen-output"
        self.execute = lambda command, timeout: secret if command[-1] == "keygen" else execute_before(command, timeout)
        with self.assertRaises(xhttp.ConfigurationError) as caught:
            self.configure()
        self.assertEqual(caught.exception.stage, "keygen")
        self.assertNotIn(secret, str(caught.exception))


if __name__ == "__main__":
    unittest.main()
