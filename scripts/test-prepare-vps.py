#!/usr/bin/env python3
"""Проверить подготовку VPS в закрытых фикстурах без изменения хоста."""

import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import stat
import struct
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'deploy/docker/prepare-vps.sh'
NODE_PATH = ROOT / 'deploy/docker/setup-node.py'
SPEC = importlib.util.spec_from_file_location('prepare_node', NODE_PATH)
NODE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(NODE)
VERIFY_SPEC = importlib.util.spec_from_file_location('prepare_verify', ROOT / 'scripts/verify-release.py')
VERIFY = importlib.util.module_from_spec(VERIFY_SPEC)
VERIFY_SPEC.loader.exec_module(VERIFY)
PUBLIC_KEY = 'ssh-ed25519 ' + base64.b64encode(
    struct.pack('>I', 11) + b'ssh-ed25519' + struct.pack('>I', 32) + b'\x01' * 32).decode() + ' fixture'
PAIR_ONE = {'private': base64.urlsafe_b64encode(b'\x11' * 32).decode().rstrip('='),
            'public': base64.urlsafe_b64encode(b'\x22' * 32).decode().rstrip('=')}
PAIR_TWO = {'private': base64.urlsafe_b64encode(b'\x33' * 32).decode().rstrip('='),
            'public': base64.urlsafe_b64encode(b'\x44' * 32).decode().rstrip('=')}
NODE_ID = '11111111-2222-4333-8444-555555555555'


class PrepareVpsTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix='pg-prepare-test-')
        self.root = Path(self.work.name)
        self.key = self.root / 'fixture.pub'
        self.key.write_text(PUBLIC_KEY + '\n')
        self.uid = max(1000, os.getuid())
        self.gid = os.getgid()
        for name in ('etc/sudoers.d', 'etc/apt/sources.list.d', 'run/lock', 'home/admin/.ssh'):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        (self.root / 'etc/os-release').write_text('ID=ubuntu\nVERSION_ID=26.04\nVERSION_CODENAME=resolute\n')
        self.install_dir = self.root / 'install'
        self.install_dir.mkdir()
        for source in (SOURCE, NODE_PATH, ROOT / 'scripts/verify-release.py',
                       ROOT / 'deploy/docker/install-bridge.sh', ROOT / 'deploy/docker/install-node.sh'):
            shutil.copyfile(source, self.install_dir / source.name)
        self.script = self.install_dir / 'prepare-vps.sh'
        self.config = self.root / 'bridge.json'
        self.config.write_text(json.dumps({'version': 1, 'listeners': [
            {'name': 'test-tcp', 'transport': 'tcp', 'listen_port': 443,
             'target_host': '203.0.113.11', 'target_port': 8443},
            {'name': 'test-xhttp', 'transport': 'xhttp', 'listen_port': 8444,
             'target_host': '203.0.113.11', 'target_port': 8444}]}))
        self.archive = self.root / 'release.tar.gz'
        self.write_release()

    def tearDown(self):
        self.work.cleanup()

    def write_release(self):
        entries = {name: b'fixture\n' for name in VERIFY.REQUIRED}
        for source in self.install_dir.iterdir():
            entries['docker/' + source.name] = source.read_bytes()
        entries['docker/bridge.py'] = (ROOT / 'deploy/docker/bridge.py').read_bytes()
        entries['manifest.json'] = json.dumps({
            'version': 'a' * 40, 'os': 'linux', 'arch': 'amd64',
            'files': {name: hashlib.sha256(content).hexdigest() for name, content in entries.items()}
        }).encode()
        with tarfile.open(self.archive, 'w:gz') as archive:
            for name, content in entries.items():
                member = tarfile.TarInfo(name)
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
        self.checksum = self.root / 'release.tar.gz.sha256'
        self.checksum.write_text(hashlib.sha256(self.archive.read_bytes()).hexdigest() + '  release.tar.gz\n')

    def run_body(self, body, *, overrides=''):
        # Все пути системных файлов перенаправлены; внешние команды изменения
        # учётных записей и пакетов либо подменены, либо тест явно запрещает их.
        preamble = f'''
source {shlex.quote(str(self.script))}
fixture={shlex.quote(str(self.root))}
system_path() {{ printf '%s%s\\n' "$fixture" "$1"; }}
require_root() {{ :; }}
ensure_base_tools() {{ :; }}
uname() {{ if [[ $1 == -s ]]; then printf 'Linux\\n'; else printf 'x86_64\\n'; fi; }}
getent() {{ [[ ! -f "$fixture/no-user" ]] || return 2; printf 'admin:x:{self.uid}:{self.gid}:Fixture:/home/admin:/bin/bash\\n'; }}
adduser() {{ printf 'adduser\\n' >> "$fixture/mutations"; rm -f "$fixture/no-user"; }}
stat() {{ if [[ $1 == -c && $2 == %u && $3 == "$fixture/home/admin" ]]; then printf '{self.uid}\\n'; else command stat "$@"; fi; }}
install() {{ local rest=(); while (($#)); do case "$1" in -o|-g) shift 2;; *) rest+=("$1"); shift;; esac; done; command install "${{rest[@]}}"; }}
visudo() {{ printf 'visudo %s\\n' "$*" >> "$fixture/visudo.log"; [[ ! -e "$fixture/visudo-fail" ]]; }}
runuser() {{ [[ ! -e "$fixture/runuser-fail" ]] || return 3; printf '0\\n'; }}
apt-get() {{ printf 'unexpected apt-get\\n' >> "$fixture/mutations"; return 91; }}
systemctl() {{ printf 'unexpected systemctl\\n' >> "$fixture/mutations"; return 91; }}
'''
        return subprocess.run(['bash', '-c', preamble + '\n' + overrides + '\n' + body],
                              text=True, capture_output=True, check=False, timeout=20)

    def arguments(self, *arguments):
        return shlex.join(['--role', 'bridge', '--user', 'admin', '--public-key', str(self.key), *arguments])

    def full_arguments(self, *arguments):
        return self.arguments('--archive', str(self.archive), '--config', str(self.config), *arguments)

    def test_help_and_invalid_user_stop_without_mutations(self):
        for arguments, expected in (('--help', 0),
                                    ('--role bridge --user root --public-key ' + shlex.quote(str(self.key)), 1),
                                    ('--role bridge --user "bad name" --public-key ' + shlex.quote(str(self.key)), 1)):
            with self.subTest(arguments=arguments):
                result = self.run_body('main ' + arguments)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertFalse((self.root / 'mutations').exists())

    def test_rejects_private_key_options_multiline_and_malformed_payload(self):
        for text in ('-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n',
                     'restrict ' + PUBLIC_KEY, PUBLIC_KEY + '\n' + PUBLIC_KEY,
                     'ssh-ed25519 YWJj fixture', 'ssh-rsa YWJj fixture'):
            with self.subTest(text=text):
                self.key.write_text(text)
                result = self.run_body('main ' + self.arguments('--access-only'))
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(text, result.stderr)
                self.assertFalse((self.root / 'mutations').exists())

    def test_access_creates_user_appends_key_and_is_idempotent(self):
        (self.root / 'no-user').touch()
        keys = self.root / 'home/admin/.ssh/authorized_keys'
        keys.write_text('# прежний ключ сохраняется\nssh-ed25519 other-key original\n')
        result = self.run_body('main ' + self.arguments('--access-only'))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('ACCESS_READY user=admin', result.stdout)
        self.assertEqual((self.root / 'mutations').read_text(), 'adduser\n')
        first = keys.read_text()
        self.assertIn('other-key original', first)
        self.assertIn(PUBLIC_KEY, first)
        self.assertEqual(stat.S_IMODE(keys.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(keys.parent.stat().st_mode), 0o700)
        sudoers = self.root / 'etc/sudoers.d/private-gateway-admin-admin'
        self.assertEqual(sudoers.read_text(), 'admin ALL=(ALL:ALL) NOPASSWD: ALL\n')
        self.assertEqual(stat.S_IMODE(sudoers.stat().st_mode), 0o440)
        self.assertIn('visudo -cf ', (self.root / 'visudo.log').read_text())
        result = self.run_body('main ' + self.arguments('--access-only'))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(keys.read_text(), first)

    def test_failed_passwordless_sudo_restores_previous_rule(self):
        sudoers = self.root / 'etc/sudoers.d/private-gateway-admin-admin'
        sudoers.write_text('admin ALL=(root) /usr/bin/id\n')
        (self.root / 'runuser-fail').touch()
        result = self.run_body('main ' + self.arguments('--access-only'))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sudoers.read_text(), 'admin ALL=(root) /usr/bin/id\n')
        self.assertNotIn('ACCESS_READY', result.stdout)

    def test_invalid_sudoers_is_not_installed(self):
        (self.root / 'visudo-fail').touch()
        result = self.run_body('main ' + self.arguments('--access-only'))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'etc/sudoers.d/private-gateway-admin-admin').exists())

    def test_refuses_authorized_keys_and_sudoers_symlinks(self):
        victim = self.root / 'victim'
        victim.write_text('preserved\n')
        for target in (self.root / 'home/admin/.ssh/authorized_keys',
                       self.root / 'etc/sudoers.d/private-gateway-admin-admin'):
            with self.subTest(target=target):
                target.symlink_to(victim)
                result = self.run_body('main ' + self.arguments('--access-only'))
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(victim.read_text(), 'preserved\n')
                target.unlink()

    def test_check_verifies_real_bundle_and_config_without_access_or_docker(self):
        result = self.run_body('main ' + self.full_arguments('--check'), overrides='''
prepare_access() { printf 'unexpected access\\n' >> "$fixture/mutations"; return 91; }
ensure_docker() { printf 'unexpected Docker\\n' >> "$fixture/mutations"; return 91; }
install_role() { printf 'unexpected role\\n' >> "$fixture/mutations"; return 91; }
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('PREPARE_CHECK_OK role=bridge', result.stdout)
        self.assertFalse((self.root / 'mutations').exists())
        self.assertFalse((self.root / 'etc/sudoers.d/private-gateway-admin-admin').exists())

    def test_full_bridge_orders_verification_access_docker_role(self):
        result = self.run_body('main ' + self.full_arguments(), overrides='''
prepare_access() { printf 'access\\n' >> "$fixture/mutations"; }
ensure_docker() { printf 'docker\\n' >> "$fixture/mutations"; }
install_role() { printf 'role:%s\\n' "$role" >> "$fixture/mutations"; }
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'mutations').read_text(), 'access\ndocker\nrole:bridge\n')
        self.assertIn('RELEASE_OK sha=' + 'a' * 40, result.stdout)
        self.assertIn('PREPARE_DONE role=bridge', result.stdout)

    def test_corrupt_release_and_changed_installer_stop_before_access(self):
        self.archive.write_bytes(self.archive.read_bytes() + b'corrupt')
        result = self.run_body('main ' + self.full_arguments(), overrides='prepare_access() { touch "$fixture/mutations"; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'mutations').exists())
        self.write_release()
        with (self.install_dir / 'install-node.sh').open('a') as destination:
            destination.write('\n# изменён после сборки\n')
        result = self.run_body('main ' + self.full_arguments(), overrides='prepare_access() { touch "$fixture/mutations"; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('install-node.sh', result.stderr)
        self.assertFalse((self.root / 'mutations').exists())

    def test_bad_bridge_config_stops_before_access_and_docker(self):
        data = json.loads(self.config.read_text())
        data['listeners'][1]['listen_port'] = 443
        self.config.write_text(json.dumps(data))
        result = self.run_body('main ' + self.full_arguments(), overrides='prepare_access() { touch "$fixture/mutations"; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'mutations').exists())

    def test_node_22_is_rejected_before_mutations(self):
        (self.root / 'etc/os-release').write_text('ID=ubuntu\nVERSION_ID=22.04\nVERSION_CODENAME=jammy\n')
        args = shlex.join(['--role', 'node', '--user', 'admin', '--public-key', str(self.key),
                           '--archive', str(self.archive), '--initialize-node', '--node-host', '203.0.113.20'])
        result = self.run_body('main ' + args)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Ubuntu 26.04', result.stderr)
        self.assertFalse((self.root / 'mutations').exists())

    def test_advanced_node_env_validation_precedes_access(self):
        env = self.root / 'node.env'
        env.write_text('GATEWAY_CONTROL_URL=https://panel.example.com:8443\n')
        args = shlex.join(['--role', 'node', '--user', 'admin', '--public-key', str(self.key),
                           '--archive', str(self.archive), '--config', str(env)])
        result = self.run_body('main ' + args, overrides='prepare_access() { touch "$fixture/mutations"; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'mutations').exists())

    def test_initialize_node_verifies_release_but_does_not_install_access_or_docker(self):
        args = shlex.join(['--role', 'node', '--user', 'admin', '--public-key', str(self.key),
                           '--archive', str(self.archive), '--initialize-node', '--node-host', '203.0.113.20'])
        result = self.run_body('main ' + args, overrides='''
initialize_node() { printf 'NODE_PARAMETERS_READY\\n'; }
prepare_access() { touch "$fixture/mutations"; return 91; }
ensure_docker() { touch "$fixture/mutations"; return 91; }
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('RELEASE_OK sha=' + 'a' * 40, result.stdout)
        self.assertIn('NODE_PARAMETERS_READY', result.stdout)
        self.assertFalse((self.root / 'mutations').exists())

    def test_installed_node_cannot_initialize_new_keys(self):
        saved = self.root / 'etc/private-gateway-node/stack.env'
        saved.parent.mkdir()
        saved.write_text('preserved-private-key-material')
        args = shlex.join(['--role', 'node', '--user', 'admin', '--public-key', str(self.key),
                           '--archive', str(self.archive), '--initialize-node', '--node-host', '203.0.113.20'])
        result = self.run_body('main ' + args)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('--resume', result.stderr)
        self.assertNotIn('preserved-private-key-material', result.stdout + result.stderr)
        self.assertFalse((self.root / 'etc/private-gateway-node-setup').exists())
        self.assertEqual(saved.read_text(), 'preserved-private-key-material')

    def test_docker_existing_is_not_reinstalled(self):
        result = self.run_body('ensure_docker', overrides='''
docker() { printf 'docker:%s\\n' "$*" >> "$fixture/docker.log"; }
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('DOCKER_READY existing=true', result.stdout)
        self.assertEqual((self.root / 'docker.log').read_text(), 'docker:compose version\ndocker:info\n')
        self.assertFalse((self.root / 'mutations').exists())

    def test_unavailable_existing_docker_is_not_replaced(self):
        result = self.run_body('ensure_docker', overrides='docker() { return 5; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'mutations').exists())


    def test_missing_docker_uses_official_signed_repository_without_removing_packages(self):
        result = self.run_body('temporary="$fixture/temporary"; mkdir "$temporary"; ubuntu_codename=jammy; ensure_docker', overrides='''
command() { if [[ "$*" == '-v docker' && ! -e "$fixture/docker-installed" ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { return 1; }
curl() { printf 'fixture Docker signing key\\n' > "${@: -1}"; }
apt-get() { printf 'apt:%s\\n' "$*" >> "$fixture/packages.log"; touch "$fixture/docker-installed"; }
systemctl() { printf 'service:%s\\n' "$*" >> "$fixture/packages.log"; }
docker() { printf 'docker:%s\\n' "$*" >> "$fixture/packages.log"; }
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('DOCKER_READY existing=false', result.stdout)
        sources = (self.root / 'etc/apt/sources.list.d/docker.sources').read_text()
        self.assertIn('URIs: https://download.docker.com/linux/ubuntu', sources)
        self.assertIn('Suites: jammy', sources)
        self.assertIn('Signed-By: /etc/apt/keyrings/docker.asc', sources)
        operations = (self.root / 'packages.log').read_text()
        self.assertIn('docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin', operations)
        self.assertIn('service:enable --now docker', operations)
        self.assertNotIn('remove', operations)

    def test_conflicting_containerd_is_not_removed(self):
        result = self.run_body('ensure_docker', overrides='''
command() { if [[ "$*" == '-v docker' ]]; then return 1; fi; builtin command "$@"; }
dpkg-query() { if [[ "${@: -1}" == containerd ]]; then printf 'install ok installed'; else return 1; fi; }
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('несовместимые пакеты', result.stderr)
        self.assertFalse((self.root / 'mutations').exists())


class NodeSetupTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix='pg-node-setup-test-')
        self.root = Path(self.work.name)
        self.state = self.root / 'state'
        self.config = NODE.settings('203.0.113.20', 'www.example.org', 8443, 8444)

    def tearDown(self):
        self.work.cleanup()

    def initialize(self):
        with patch.object(NODE, 'keypair', side_effect=[PAIR_ONE.copy(), PAIR_TWO.copy()]):
            return NODE.initialize(self.state, self.root / 'fixture-xray', self.config)

    def test_creates_separate_pairs_and_reuses_exact_saved_keys(self):
        first = self.initialize()
        with patch.object(NODE, 'keypair', side_effect=AssertionError('must not regenerate')):
            second = NODE.initialize(self.state, self.root / 'fixture-xray', self.config)
        self.assertEqual(first, second)
        self.assertNotEqual(first['tcp']['private'], first['xhttp']['private'])
        self.assertNotEqual(first['tcp_short_id'], first['xhttp_short_id'])
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE((self.state / 'parameters.json').stat().st_mode), 0o600)
        public = json.dumps(NODE.public_parameters(first))
        self.assertNotIn(PAIR_ONE['private'], public)
        self.assertNotIn(PAIR_TWO['private'], public)
        self.assertIn(PAIR_ONE['public'], public)
        self.assertIn(PAIR_TWO['public'], public)

    def test_changed_metadata_and_identical_pairs_do_not_replace_keys(self):
        self.initialize()
        before = (self.state / 'parameters.json').read_bytes()
        with self.assertRaises(ValueError):
            NODE.initialize(self.state, 'unused', NODE.settings('203.0.113.21', 'www.example.org', 8443, 8444))
        self.assertEqual((self.state / 'parameters.json').read_bytes(), before)
        second = self.root / 'duplicate-state'
        with patch.object(NODE, 'keypair', return_value=PAIR_ONE.copy()), self.assertRaises(ValueError):
            NODE.initialize(second, 'unused', self.config)
        self.assertFalse((second / 'parameters.json').exists())

    def test_keypair_accepts_supported_xray_labels_without_private_argv(self):
        for label in ('PublicKey', 'Password (PublicKey)', 'Password'):
            with self.subTest(label=label):
                output = f"PrivateKey: {PAIR_ONE['private']}\n{label}: {PAIR_ONE['public']}\nHash32: ignored\n"
                with patch.object(NODE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, output, '')) as run:
                    self.assertEqual(NODE.keypair(Path('/fixture/xray')), PAIR_ONE)
                    self.assertEqual(run.call_args.args[0], ['/fixture/xray', 'x25519'])

    def test_failed_xray_does_not_repeat_private_stderr(self):
        result = subprocess.CompletedProcess([], 1, '', PAIR_ONE['private'])
        with patch.object(NODE.subprocess, 'run', return_value=result), self.assertRaises(ValueError) as caught:
            NODE.keypair(Path('/fixture/xray'))
        self.assertNotIn(PAIR_ONE['private'], str(caught.exception))

    def test_generated_env_is_private_and_matches_public_settings(self):
        first = self.initialize()
        path = NODE.write_env(self.state, '203.0.113.20', NODE_ID, 'https://panel.example.com:8443')
        values = NODE.validate_env(path)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(values['GATEWAY_REALITY_PRIVATE_KEY'], first['tcp']['private'])
        self.assertEqual(values['GATEWAY_XHTTP_PRIVATE_KEY'], first['xhttp']['private'])
        self.assertEqual(values['GATEWAY_XHTTP_PATH'], first['xhttp_path'])
        self.assertEqual(values['GATEWAY_SHORT_ID'], first['tcp_short_id'])
        self.assertNotIn('TOKEN', path.read_text())

    def test_check_validates_environment_without_writing_it(self):
        self.initialize()
        path = NODE.write_env(self.state, '203.0.113.20', NODE_ID, 'https://panel.example.com:8443', check=True)
        self.assertFalse(path.exists())
        with self.assertRaises(ValueError):
            NODE.write_env(self.state, '203.0.113.21', NODE_ID, 'https://panel.example.com:8443', check=True)
        self.assertFalse(path.exists())

    def test_rejects_invalid_control_urls_and_ids(self):
        data = self.initialize()
        for url in ('http://panel.example.com', 'https://panel.example.com/', 'https://user:secret@panel.example.com',
                    'https://panel.example.com/path', 'https://panel.example.com?token=secret',
                    'https://panel.example.com\nGATEWAY_NODE_ID=other', 'https://panel.example.com:99999'):
            with self.subTest(url=url), self.assertRaises(ValueError):
                NODE.environment(data, '203.0.113.20', NODE_ID, url)
        for identifier in ('not-uuid', '00000000-0000-0000-0000-000000000000'):
            with self.subTest(identifier=identifier), self.assertRaises(ValueError):
                NODE.environment(data, '203.0.113.20', identifier, 'https://panel.example.com')

    def test_node_port_conflict_is_detected_without_writing_environment(self):
        self.initialize()
        with patch.object(NODE.socket, 'socket') as socket_factory:
            socket_factory.return_value.__enter__.return_value.bind.side_effect = OSError('occupied fixture port')
            with self.assertRaises(ValueError):
                NODE.write_env(self.state, '203.0.113.20', NODE_ID, 'https://panel.example.com:8443',
                               check=True, check_ports=True)
        self.assertFalse((self.state / 'node.env').exists())

    def test_refuses_symlink_or_open_permissions_on_private_state(self):
        self.initialize()
        path = self.state / 'parameters.json'
        path.chmod(0o644)
        with self.assertRaises(ValueError):
            NODE.load(path)
        path.chmod(0o600)
        victim = self.root / 'victim'
        victim.write_text('preserved')
        path.unlink()
        path.symlink_to(victim)
        with self.assertRaises(ValueError):
            NODE.load(path)
        self.assertEqual(victim.read_text(), 'preserved')
        self.state.chmod(0o755)
        with self.assertRaises(ValueError):
            NODE.private_directory(self.state)

    def test_rejects_duplicate_ports_and_injected_host_metadata(self):
        for args in (('203.0.113.20', 'www.example.org', 8443, 8443),
                     ('203.0.113.20', 'www.example.org', 22, 8444),
                     ('203.0.113.20\nINJECT=1', 'www.example.org', 8443, 8444),
                     ('203.0.113.20', '127.0.0.1', 8443, 8444)):
            with self.subTest(args=args), self.assertRaises(ValueError):
                NODE.settings(*args)


if __name__ == '__main__':
    unittest.main()
