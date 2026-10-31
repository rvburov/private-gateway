#!/usr/bin/env python3
"""Проверить безопасные аннотации CI без запуска установки и команд рабочего сервера."""

from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
# Берём только обработчики до чтения аргумента архива: CI-only тело не исполняется.
SOURCE = (ROOT / "scripts/test-install-update.sh").read_text()
PREAMBLE = SOURCE.split("\narchive=", 1)[0]
# Исполняем настоящие определения и место запуска сборщиков, не установку и не
# их упрощённую копию: изменение наследования trap проверяется на рабочем коде.
COLLECTORS = "watch_xray_status() {" + SOURCE.split("\nwatch_xray_status() {", 1)[1].split("\nfinish() {", 1)[0]
START_COLLECTORS = 'mkdir "$work/logs"\n' + SOURCE.split('\nmkdir "$work/logs"\n', 1)[1].split("\n# Шаг выкладки", 1)[0]


class InstallationDiagnosticsTests(unittest.TestCase):
    def run_fixture(self, body):
        return subprocess.run(["bash", "-c", PREAMBLE + "\n" + body],
                              capture_output=True, text=True, check=False, timeout=5)

    def test_failed_command_retains_status_without_exposing_command_or_unknown_phase(self):
        result = self.run_fixture('install_test_phase=test-secret-not-for-output\n'
                                  'bash -c "exit 7" -- test-command-credential\n')
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, "")
        self.assertIn("::error file=scripts/test-install-update.sh,line=", result.stderr)
        self.assertIn("phase=unknown; exit_status=7", result.stderr)
        self.assertNotIn("test-secret-not-for-output", result.stderr)
        self.assertNotIn("test-command-credential", result.stderr)

    def test_nested_failure_keeps_exit_trap_status_and_stderr_separate(self):
        result = self.run_fixture('install_test_phase=xhttp-enable\n'
                                  'trap \'printf "EXIT:%s\\n" "$?"\' EXIT\n'
                                  'nested() { bash -c "exit 13"; }\n'
                                  'value=$(nested)\n')
        self.assertEqual(result.returncode, 13)
        self.assertEqual(result.stdout, "EXIT:13\n")
        self.assertIn("phase=xhttp-enable; exit_status=13", result.stderr)

    def test_expected_failure_does_not_emit_a_false_annotation(self):
        result = self.run_fixture('install_test_phase=panel-rollback\n'
                                  'if bash -c "exit 9"; then exit 8; fi\n'
                                  'printf "expected failure handled\\n"\n')
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stderr, "")
        self.assertEqual(result.stdout, "expected failure handled\n")

    def test_explicit_assertion_failure_emits_annotation(self):
        result = self.run_fixture('install_test_phase=initial-checks\n'
                                  'fail "synthetic assertion"\n')
        self.assertEqual(result.returncode, 1)
        self.assertIn("phase=initial-checks; exit_status=1", result.stderr)
        self.assertIn("Installation test failed: synthetic assertion", result.stderr)

    def test_exit_fallback_does_not_claim_an_invalid_line_number(self):
        result = self.run_fixture('install_test_phase=bootstrap\n'
                                  'report_install_failure 3 0\n')
        self.assertEqual(result.returncode, 0)
        self.assertIn("::error title=Installation check::phase=bootstrap; exit_status=3", result.stderr)
        self.assertNotIn("line=0", result.stderr)

    def test_background_collection_failure_has_no_annotation_and_preserves_main_trap(self):
        for fail_main in (False, True):
            with self.subTest(fail_main=fail_main), tempfile.TemporaryDirectory() as work:
                body = ('install_test_phase=panel-update\n'
                        'work=' + shlex.quote(work) + '\n'
                        'docker() { return 7; }\n'
                        'sudo() { return 7; }\n' + COLLECTORS + '\n' + START_COLLECTORS + '\n'
                        'sleep 0.1\n'
                        'kill $collector 2>/dev/null || true\n'
                        'for child in $collector; do wait "$child" 2>/dev/null || true; done\n')
                body += 'bash -c "exit 17"\n' if fail_main else 'printf "main-script-success\\n"\n'
                result = self.run_fixture(body)
                self.assertNotIn("exit_status=7", result.stderr)
                if fail_main:
                    self.assertEqual(result.returncode, 17)
                    self.assertIn("phase=panel-update; exit_status=17", result.stderr)
                else:
                    self.assertEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, "main-script-success\n")
                    self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
