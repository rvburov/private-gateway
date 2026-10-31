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
COMPOSE = "compose() {" + SOURCE.split("\ncompose() {", 1)[1].split("\n# Неудачная установка", 1)[0]
PANEL = "panel_ready() {" + SOURCE.split("\npanel_ready() {", 1)[1].split("\nbrowser_protected() {", 1)[0]
BROWSER = "browser_protected() {" + SOURCE.split("\nbrowser_protected() {", 1)[1].split("\nvless_works() {", 1)[0]
VLESS = "vless_works() {" + SOURCE.split("\nvless_works() {", 1)[1].split("\nxhttp_works() {", 1)[0]


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

    def test_compose_checks_root_only_overlay_with_same_privileges_as_config(self):
        body = ('env_file=/tmp/unused-test.env\n'
                'sudo() {\n'
                ' case "$1" in\n'
                '  test) [[ "$2" == -f && "$3" == */compose.xhttp.yml ]];;\n'
                '  grep) [[ "$overlay_enabled" == true ]];;\n'
                '  docker) if [[ " $* " == *compose.xhttp.yml* ]]; then printf "overlay\\n"; else printf "base\\n"; fi;;\n'
                '  *) return 90;;\n'
                ' esac\n'
                '}\n' + COMPOSE + '\n'
                'overlay_enabled=true\ncompose config --format json\n'
                'overlay_enabled=false\ncompose config --format json\n')
        result = self.run_fixture(body)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "overlay\nbase\n")
        self.assertEqual(result.stderr, "")

    def test_panel_port_resolution_failure_does_not_attempt_an_invalid_url(self):
        result = self.run_fixture('domain=example.com\n'
                                  'public_port() { return 7; }\n'
                                  'curl() { printf "unexpected request\\n"; return 90; }\n' + PANEL + '\n'
                                  'if panel_ready; then exit 91; fi\n')
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("reason=port-resolution", result.stderr)
        self.assertNotIn("request-failed", result.stderr)

    def test_browser_and_vless_stop_on_port_reader_failure(self):
        for definition, name in ((BROWSER, "browser_protected"), (VLESS, "vless_works")):
            with self.subTest(name=name):
                result = self.run_fixture('domain=example.com\n'
                                          'public_port() { return 7; }\n'
                                          'curl() { printf "unexpected request\\n"; return 90; }\n'
                                          'sudo() { printf "unexpected smoke\\n"; return 90; }\n' + definition + '\n'
                                          'if ' + name + '; then exit 91; fi\n')
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
                self.assertEqual(result.stderr, "")

    def test_panel_failure_reports_numbers_without_proxy_or_response_secrets(self):
        result = self.run_fixture('domain=example.com\n'
                                  'HTTPS_PROXY=https://test-user:test-proxy-secret@example.org\n'
                                  'public_port() { printf "8443\\n"; }\n'
                                  'curl() { printf "503 0"; return 22; }\n' + PANEL + '\n'
                                  'if panel_ready; then exit 91; fi\n')
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("port=8443; http_status=503; tls_status=0; curl_exit=22; proxy_configured=true", result.stderr)
        self.assertNotIn("test-proxy-secret", result.stderr)
        self.assertNotIn("example.com", result.stderr)
        self.assertNotIn("example.org", result.stderr)

    def test_panel_requires_http_200_and_verified_tls(self):
        for response, curl_status, accepted in (("200 0", 0, True), ("204 0", 0, False),
                                                ("200 20", 60, False), ("private-response-text", 0, False)):
            with self.subTest(response=response):
                result = self.run_fixture('domain=example.com\n'
                                          'public_port() { printf "8443\\n"; }\n'
                                          'curl() { printf %s ' + shlex.quote(response) + '; return ' + str(curl_status) + '; }\n' + PANEL + '\n'
                                          'if panel_ready; then printf "accepted\\n"; else printf "rejected\\n"; fi\n')
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "accepted\n" if accepted else "rejected\n")
                if accepted:
                    self.assertEqual(result.stderr, "")
                self.assertNotIn("private-response-text", result.stderr)


if __name__ == "__main__":
    unittest.main()
