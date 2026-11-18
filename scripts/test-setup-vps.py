#!/usr/bin/env python3
"""Проверить локальный мастер установки без SSH, sudo и изменения настоящего VPS."""

import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("setup-vps.sh").resolve()
HELPERS = ("prepare-vps.sh", "verify-release.py", "install-bridge.sh", "install-node.sh", "setup-node.py")
PUBLIC = "ssh-ed25519 AAAATESTPRIVATEGATEWAY automation\n"
STUB = r'''#!/usr/bin/env python3
import json, os, pathlib, shlex, sys
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
log = pathlib.Path(os.environ['SETUP_TEST_CALLS'])
with log.open('a') as output:
    output.write(json.dumps([tool, *args], ensure_ascii=False) + '\n')
if tool == 'ssh-keygen':
    key = pathlib.Path(args[args.index('-f') + 1])
    if '-y' in args:
        if key.read_text() == 'encrypted':
            raise SystemExit(1)
        print('ssh-ed25519 AAAATESTPRIVATEGATEWAY automation')
    else:
        if key.exists() or key.with_name(key.name + '.pub').exists():
            raise SystemExit('existing key would be overwritten')
        key.write_text('private-key-synthetic')
        key.with_name(key.name + '.pub').write_text('ssh-ed25519 AAAATESTPRIVATEGATEWAY automation\n')
    raise SystemExit(0)
if tool == 'scp':
    if os.environ.get('SETUP_TEST_SCP_FAILURE') == '1' and args[-1].endswith('/input.conf'):
        raise SystemExit(8)
    raise SystemExit(0)
command = args[-1]
if 'mktemp -d /tmp/private-gateway-access.' in command:
    print(os.environ.get('SETUP_TEST_ACCESS_DIR', '/tmp/private-gateway-access.ABCDef12'))
elif 'mktemp -d /tmp/private-gateway-install.' in command:
    print('/tmp/private-gateway-install.ABCDef12')
elif 'SSH_KEY_OK' in command:
    if os.environ.get('SETUP_TEST_ACCESS_FAILURE') == '1':
        raise SystemExit(9)
    print('SSH_KEY_OK\nSUDO_PASSWORDLESS_OK')
elif '--access-only' in command:
    print('ACCESS_READY user=pgadmin')
elif '--initialize-node' in command:
    print('NODE_HOST=198.51.100.10\nTCP_PUBLIC_KEY=synthetic-public-tcp\nXHTTP_PUBLIC_KEY=synthetic-public-xhttp\nNODE_PARAMETERS_READY')
elif 'prepare-vps.sh' in command:
    if os.environ.get('SETUP_TEST_INSTALL_FAILURE') == '1':
        raise SystemExit(7)
    print('PREPARE_DONE')
'''


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pg-setup-tests-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "Дом пользователя с пробелом ' $(touch SHOULD_NOT_EXIST)"
        self.home.mkdir()
        self.downloads = self.home / "Downloads"
        self.artifact = self.downloads / "private-gateway-linux-amd64"
        (self.artifact / "install").mkdir(parents=True)
        payload = {f"docker/{name}": f"synthetic verified {name}\n".encode() for name in HELPERS}
        with tarfile.open(self.artifact / "release.tar.gz", "w:gz") as archive:
            for name, content in payload.items():
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
                (self.artifact / "install" / Path(name).name).write_bytes(content)
        self.checksum()
        self.config = self.downloads / "bridge-11111111-2222-4333-8444-555555555555.json"
        self.config.write_text('{"not_a_secret": true}\n')
        self.calls_file = self.root / "calls.jsonl"
        self.tools = self.root / "tools"
        self.tools.mkdir()
        for name in ("ssh", "scp", "ssh-keygen"):
            tool = self.tools / name
            tool.write_text(STUB)
            tool.chmod(0o755)
        self.env = dict(os.environ, HOME=str(self.home), PATH=f"{self.tools}:{os.environ['PATH']}",
                        SETUP_TEST_CALLS=str(self.calls_file))
        self.key = self.home / ".ssh" / "private_gateway_bridge_198.51.100.10_ed25519"

    def checksum(self):
        digest = hashlib.sha256((self.artifact / "release.tar.gz").read_bytes()).hexdigest()
        (self.artifact / "release.tar.gz.sha256").write_text(f"{digest}  release.tar.gz\n")

    def run_setup(self, *args, role="bridge", config=True, answers="", environment=None):
        command = ["bash", str(SCRIPT), "--role", role, "--host", "198.51.100.10", "--port", "22",
                   "--initial-user", "ubuntu", "--user", "pgadmin", "--artifact", str(self.artifact)]
        if config and role == "bridge":
            command += ["--config", str(self.config)]
        env = self.env | (environment or {})
        result = subprocess.run(command + list(args), input=answers, text=True, capture_output=True,
                                cwd=self.root, env=env, timeout=20)
        return result

    def calls(self):
        return [json.loads(line) for line in self.calls_file.read_text().splitlines()] if self.calls_file.exists() else []

    def remote_calls(self):
        return [call for call in self.calls() if call[0] == "ssh"]

    def prepare_calls(self):
        return [shlex.split(call[-1]) for call in self.remote_calls() if "prepare-vps.sh" in call[-1]]

    def existing_key(self, *, encrypted=False):
        self.key.parent.mkdir(parents=True, exist_ok=True)
        self.key.write_text("encrypted" if encrypted else "private-key-synthetic")
        Path(str(self.key) + ".pub").write_text(PUBLIC)

    def test_check_is_read_only_even_without_an_existing_key(self):
        result = self.run_setup("--check")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("LOCAL_SETUP_CHECK_OK role=bridge", result.stdout)
        self.assertFalse(self.key.exists())
        self.assertFalse((self.home / ".private-gateway").exists())
        self.assertEqual(self.calls(), [])

    def test_first_run_creates_an_unencrypted_key_and_verifies_fresh_access(self):
        result = self.run_setup()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.calls()
        generated = next(call for call in calls if call[0] == "ssh-keygen" and "-t" in call)
        self.assertEqual(generated[generated.index("-N") + 1], "")
        access = next(call for call in self.prepare_calls() if "--access-only" in call)
        self.assertEqual(access[:3], ["sudo", "-H", "bash"])
        install = next(call for call in self.prepare_calls() if "--archive" in call)
        self.assertEqual(install[:3], ["sudo", "-n", "bash"])
        self.assertIn("SSH_KEY_OK", result.stdout)
        self.assertIn("SUDO_PASSWORDLESS_OK", result.stdout)
        self.assertIn("SETUP_DONE role=bridge", result.stdout)
        fresh = next(call for call in self.remote_calls() if "SSH_KEY_OK" in call[-1])
        for option in ("IdentitiesOnly=yes", "BatchMode=yes", "PasswordAuthentication=no",
                       "KbdInteractiveAuthentication=no", "IdentityAgent=none"):
            self.assertIn(option, fresh)
        self.assertNotIn("StrictHostKeyChecking=no", json.dumps(calls))
        self.assertNotIn("UserKnownHostsFile=/dev/null", json.dumps(calls))
        self.assertFalse((self.root / "SHOULD_NOT_EXIST").exists())
        self.assertEqual(len(list((self.home / ".private-gateway/setup").glob("*/setup.log"))), 1)

    def test_staging_uploads_the_complete_verified_bundle_and_only_public_ssh_key(self):
        result = self.run_setup()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        uploads = [call for call in self.calls() if call[0] == "scp"]
        uploaded_names = {Path(call[-2]).name for call in uploads}
        self.assertTrue(set(HELPERS) <= uploaded_names)
        self.assertIn("release.tar.gz", uploaded_names)
        self.assertIn("release.tar.gz.sha256", uploaded_names)
        self.assertIn(str(self.key) + ".pub", [call[-2] for call in uploads])
        self.assertNotIn(str(self.key), [call[-2] for call in uploads])
        self.assertTrue(all("-O" in call for call in uploads))
        for call in self.remote_calls():
            self.assertEqual(shlex.split(call[-1])[-1:] == ["SHOULD_NOT_EXIST"], False)

    def test_existing_valid_access_skips_initial_password_and_key_generation(self):
        self.existing_key()
        before = self.key.read_bytes()
        result = self.run_setup()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("EXISTING_ACCESS_OK", result.stdout)
        self.assertEqual(self.key.read_bytes(), before)
        self.assertFalse(any("--access-only" in call for call in self.prepare_calls()))
        self.assertFalse(any(call[0] == "ssh-keygen" and "-t" in call for call in self.calls()))
        self.assertFalse(any("ubuntu@198.51.100.10" in call for call in self.calls()))

    def test_encrypted_or_incomplete_existing_key_is_never_replaced(self):
        self.existing_key(encrypted=True)
        before = self.key.read_bytes()
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("парольной фразы", result.stdout + result.stderr)
        self.assertEqual(self.key.read_bytes(), before)
        self.assertFalse(self.remote_calls())
        Path(str(self.key) + ".pub").unlink()
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.key.read_bytes(), before)
        self.assertFalse(self.remote_calls())

    def test_an_initial_provider_key_is_used_only_for_bootstrap_and_never_uploaded(self):
        initial_key = self.home / "первоначальный ключ"
        initial_key.write_text("initial-provider-key")
        result = self.run_setup("--initial-key", str(initial_key))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        initial_calls = [call for call in self.calls() if "ubuntu@198.51.100.10" in call or
                         (call[0] == "scp" and call[-1].startswith("ubuntu@"))]
        self.assertTrue(initial_calls)
        self.assertTrue(all(str(initial_key) in call for call in initial_calls))
        self.assertFalse(any(call[0] == "scp" and call[-2] == str(initial_key) for call in self.calls()))
        self.assertFalse(any(str(initial_key) in call for call in self.remote_calls()
                             if "pgadmin@198.51.100.10" in call))
        self.assertNotIn("initial-provider-key", result.stdout + result.stderr)
        journal = next((self.home / ".private-gateway/setup").glob("*/setup.log")).read_text()
        self.assertNotIn("initial-provider-key", journal)

    def test_initial_root_runs_access_preparation_without_sudo(self):
        result = self.run_setup("--initial-user", "root", "--port", "2222")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        access = next(call for call in self.prepare_calls() if "--access-only" in call)
        self.assertEqual(access[0], "bash")
        self.assertNotIn("sudo", access)
        for call in self.calls():
            if call[0] == "ssh":
                self.assertEqual(call[call.index("-p") + 1], "2222")
            elif call[0] == "scp":
                self.assertEqual(call[call.index("-P") + 1], "2222")

    def test_auto_detects_one_bridge_config_without_renaming_it(self):
        result = self.run_setup("--check", config=False, answers="\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(self.config.exists())
        self.assertFalse((self.downloads / "bridge.json").exists())

    def test_multiple_bridge_configs_require_an_explicit_selection(self):
        (self.downloads / "bridge-other.json").write_text("{}")
        result = self.run_setup("--check", config=False, answers="\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("несколько конфигураций", result.stdout)
        self.assertFalse(self.calls())

    def test_no_bridge_config_requires_an_explicit_path_without_empty_array_failure(self):
        self.config.unlink()
        result = self.run_setup("--check", config=False, answers="\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("укажите один конфигурационный файл", result.stdout + result.stderr)
        self.assertNotIn("unbound variable", result.stdout + result.stderr)
        self.assertFalse(self.calls())

    def test_node_initializes_on_server_and_passes_only_public_metadata(self):
        result = self.run_setup(role="node", answers="11111111-2222-4333-8444-555555555555\nhttps://panel.example.org:8443\n")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        preparations = self.prepare_calls()
        initialize = next(call for call in preparations if "--initialize-node" in call)
        self.assertEqual(initialize[initialize.index("--node-host") + 1], "198.51.100.10")
        self.assertEqual(initialize[initialize.index("--tcp-port") + 1], "8443")
        self.assertEqual(initialize[initialize.index("--xhttp-port") + 1], "8444")
        full = next(call for call in preparations if "--node-id" in call)
        self.assertEqual(full[full.index("--control-url") + 1], "https://panel.example.org:8443")
        self.assertNotIn("--config", full)
        self.assertNotIn("--join-token", full)
        install_call = next(call for call in self.remote_calls() if "--node-id" in call[-1])
        self.assertIn("-tt", install_call)
        self.assertIn("NODE_PARAMETERS_READY", result.stdout)
        self.assertIn("SETUP_DONE role=node", result.stdout)

    def test_resume_uses_saved_configuration_without_initialization_or_new_config(self):
        result = self.run_setup("--resume", role="node")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        preparations = self.prepare_calls()
        self.assertFalse(any("--initialize-node" in call for call in preparations))
        full = next(call for call in preparations if "--resume" in call)
        self.assertNotIn("--config", full)
        self.assertNotIn("--node-id", full)
        self.assertNotIn("--control-url", full)

    def test_checksum_and_installer_mismatch_stop_before_key_creation_or_ssh(self):
        (self.artifact / "release.tar.gz").write_bytes(b"tampered")
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA-256", result.stdout + result.stderr)
        self.assertFalse(self.calls())
        self.assertFalse(self.key.exists())

    def test_changed_prepare_helper_is_rejected_even_when_tar_checksum_is_valid(self):
        (self.artifact / "install/prepare-vps.sh").write_text("changed helper")
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("не совпадает", result.stdout + result.stderr)
        self.assertFalse(self.calls())

    def test_old_artifact_without_setup_helper_is_rejected_with_download_instruction(self):
        (self.artifact / "install/prepare-vps.sh").unlink()
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("новый артефакт", result.stdout + result.stderr)
        self.assertFalse(self.calls())

    def test_invalid_addresses_usernames_and_ports_never_reach_ssh(self):
        for arguments in (("--host", "-oProxyCommand=bad"), ("--host", "300.1.2.3"),
                          ("--host", "198.051.100.10"), ("--user", "root"),
                          ("--initial-user", "ubuntu; touch bad"), ("--port", "0"),
                          ("--port", "65536")):
            with self.subTest(arguments=arguments):
                result = self.run_setup(*arguments)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.calls())

    def test_node_rejects_credentials_in_control_url_and_shared_transport_ports(self):
        for arguments in (("--control-url", "https://user:password@panel.example.org"),
                          ("--control-url", "https://panel.example.org/path?token=bad"),
                          ("--tcp-port", "8444")):
            with self.subTest(arguments=arguments):
                result = self.run_setup(*arguments, role="node")
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.calls())

    def test_target_username_obeys_the_server_helper_limit_before_any_ssh(self):
        result = self.run_setup("--user", "a" * 32)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.calls())
        result = self.run_setup("--check", "--user", "a" * 31)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_node_canonicalizes_browser_origin_and_uuid_before_remote_install(self):
        result = self.run_setup("--node-id", "ABCDEFAB-2222-4333-8444-555555555555",
                                "--control-url", "https://panel.example.org:8443/", role="node")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        full = next(call for call in self.prepare_calls() if "--node-id" in call)
        self.assertEqual(full[full.index("--node-id") + 1], "abcdefab-2222-4333-8444-555555555555")
        self.assertEqual(full[full.index("--control-url") + 1], "https://panel.example.org:8443")

    def test_node_rejects_the_actual_ssh_port_before_creating_access_or_keys(self):
        for flag in ("--tcp-port", "--xhttp-port"):
            with self.subTest(flag=flag):
                result = self.run_setup("--port", "2222", flag, "2222", role="node")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("SSH-порт", result.stdout + result.stderr)
                self.assertFalse(self.calls())
                self.assertFalse(self.key.exists())

    def test_unexpected_remote_stage_path_is_not_executed_or_cleaned(self):
        result = self.run_setup(environment={"SETUP_TEST_ACCESS_DIR": "/tmp/one;touch BAD"})
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(call[0] == "scp" for call in self.calls()))
        self.assertFalse(any("rm -rf" in call[-1] for call in self.remote_calls()))

    def test_failed_access_check_does_not_upload_a_configuration(self):
        result = self.run_setup(environment={"SETUP_TEST_ACCESS_FAILURE": "1"})
        self.assertNotEqual(result.returncode, 0)
        uploads = [call for call in self.calls() if call[0] == "scp"]
        self.assertFalse(any(call[-1].endswith("/input.conf") for call in uploads))
        self.assertFalse(any("--archive" in call for call in self.prepare_calls()))

    def test_failed_config_upload_cleans_both_owned_remote_stages(self):
        result = self.run_setup(environment={"SETUP_TEST_SCP_FAILURE": "1"})
        self.assertNotEqual(result.returncode, 0)
        cleanup = [parts for call in self.remote_calls() if "rm" in (parts := shlex.split(call[-1]))]
        self.assertIn(["rm", "-rf", "--", "/tmp/private-gateway-install.ABCDef12"], cleanup)
        self.assertIn(["sudo", "-n", "rm", "-rf", "--", "/tmp/private-gateway-access.ABCDef12"], cleanup)
        self.assertNotIn("SETUP_DONE", result.stdout)

    def test_failed_remote_install_never_reports_success_and_preserves_a_journal(self):
        result = self.run_setup(environment={"SETUP_TEST_INSTALL_FAILURE": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("SETUP_DONE", result.stdout)
        self.assertIn("Установка остановилась", result.stdout + result.stderr)
        journal = next((self.home / ".private-gateway/setup").glob("*/setup.log")).read_text()
        self.assertIn("Установка остановилась", journal)
        self.assertNotIn("private-key-synthetic", journal)


if __name__ == "__main__":
    unittest.main()
