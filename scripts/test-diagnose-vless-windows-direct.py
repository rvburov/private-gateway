#!/usr/bin/env python3
"""Проверить скрытый ввод и изоляцию временного Windows-клиента синтетическим профилем."""

import base64
import errno
import json
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import tempfile
import termios
import time
import unittest
from urllib.parse import urlencode

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/diagnose-vless-windows-direct.sh"
UUID = "11111111-2222-4333-8444-555555555555"
PUBLIC_KEY = base64.urlsafe_b64encode(bytes(range(32))).rstrip(b"=").decode()
SPIDER = '/probe?text="quoted"\\ok'
PARAMETERS = {
    "encryption": "none", "flow": "xtls-rprx-vision", "type": "tcp",
    "security": "reality", "sni": "www.cloudflare.com", "fp": "chrome",
    "pbk": PUBLIC_KEY, "sid": "aabb0011", "spx": SPIDER,
}
PROFILE = f"vless://{UUID}@87.199.131.176:443?{urlencode(PARAMETERS)}#Synthetic"

MOCK_XRAY = r'''#!/usr/bin/env python3
import json, os, signal, sys, time
from pathlib import Path
root = Path(os.environ["MOCK_ROOT"])
if sys.argv[1:] == ["version"]:
    print("Xray 26.10.21 synthetic")
    raise SystemExit(0)
config = json.load(sys.stdin)
with (root / "configs.jsonl").open("a") as out:
    out.write(json.dumps(config) + "\n")
if "-test" in sys.argv:
    if os.environ.get("MOCK_MODE") == "invalid_config":
        print(os.environ["MOCK_SECRET"], file=sys.stderr)
        raise SystemExit(2)
    raise SystemExit(0)
(root / "xray.pid").write_text(str(os.getpid()))
if os.environ.get("MOCK_MODE") == "startup_exit":
    print(os.environ["MOCK_SECRET"], file=sys.stderr)
    raise SystemExit(7)
signal.signal(signal.SIGTERM, lambda *args: sys.exit(0))
while True:
    time.sleep(0.1)
'''

MOCK_NETSTAT = r'''#!/usr/bin/env python3
import os
from pathlib import Path
root = Path(os.environ["MOCK_ROOT"])
pidfile = root / "xray.pid"
if os.environ.get("MOCK_MODE") == "busy":
    pid = os.environ["MOCK_FOREIGN_PID"]
elif pidfile.exists():
    pid = pidfile.read_text()
else:
    raise SystemExit(0)
print("  TCP    127.0.0.1:" + os.environ["PG_DIAG_LOCAL_PORT"] + "    0.0.0.0:0    LISTENING    " + pid)
'''

MOCK_CURL = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
root = Path(os.environ["MOCK_ROOT"])
(root / "curl-args.json").write_text(json.dumps(sys.argv[1:]))
if os.environ.get("MOCK_MODE") == "curl_fail":
    print(os.environ["MOCK_SECRET"], file=sys.stderr)
    print(os.environ["MOCK_SECRET"])
    raise SystemExit(28)
print("203.0.113.7" if os.environ.get("MOCK_MODE") == "wrong_ip" else "162.35.172.6")
'''


class WindowsDirectTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pg-windows-direct-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for name, content in (("xray.exe", MOCK_XRAY), ("netstat.exe", MOCK_NETSTAT), ("curl.exe", MOCK_CURL)):
            path = self.root / name
            path.write_text(content)
            path.chmod(0o755)

    def run_probe(self, profile=PROFILE, mode="pass", foreign_pid="", interrupt=False):
        env = os.environ.copy()
        env.update(PATH=str(self.root) + os.pathsep + env["PATH"], MOCK_ROOT=str(self.root),
                   MOCK_MODE=mode, MOCK_SECRET=PROFILE, MOCK_FOREIGN_PID=foreign_pid,
                   PG_DIAG_LOCAL_PORT="55321")
        pid, master = pty.fork()
        if pid == 0:
            os.execve("/bin/bash", ["bash", str(SCRIPT), str(self.root / "xray.exe")], env)
        content = bytearray()
        supplied = False
        deadline = time.monotonic() + 15
        status = None
        echo_restored = False
        try:
            while time.monotonic() < deadline:
                readable, _, _ = select.select([master], [], [], 0.05)
                if readable:
                    try:
                        data = os.read(master, 65536)
                    except OSError as error:
                        if error.errno != errno.EIO:
                            raise
                        break
                    if not data:
                        break
                    content.extend(data)
                if not supplied and b"(\xd0\xb2\xd0\xb2\xd0\xbe\xd0\xb4 \xd1\x81\xd0\xba\xd1\x80\xd1\x8b\xd1\x82): " in content:
                    supplied = True
                    if interrupt:
                        os.kill(pid, signal.SIGTERM)
                    else:
                        os.write(master, profile.encode() + b"\n")
            else:
                self.fail("Диагностический скрипт завис")
            _, status = os.waitpid(pid, 0)
            echo_restored = bool(termios.tcgetattr(master)[3] & termios.ECHO)
        finally:
            if status is None:
                try:
                    os.kill(pid, signal.SIGKILL)
                    os.waitpid(pid, 0)
                except ProcessLookupError:
                    pass
            os.close(master)
        output = content.decode("utf-8", errors="replace")
        for secret in (PROFILE, UUID, PUBLIC_KEY, SPIDER):
            self.assertNotIn(secret, output)
        self.assertTrue(echo_restored, "После завершения не восстановлен ввод терминала")
        pidfile = self.root / "xray.pid"
        if pidfile.exists():
            child = int(pidfile.read_text())
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                try:
                    os.kill(child, 0)
                except ProcessLookupError:
                    break
                time.sleep(0.05)
            else:
                self.fail("Временный процесс Xray не завершён")
        return os.waitstatus_to_exitcode(status), output

    def test_preserves_profile_and_uses_only_one_loopback_proxy(self):
        code, output = self.run_probe()
        self.assertEqual(code, 0, output)
        self.assertIn("RESULT=PASS", output)
        configs = [json.loads(row) for row in (self.root / "configs.jsonl").read_text().splitlines()]
        self.assertEqual(len(configs), 2)
        self.assertEqual(configs[0], configs[1])
        config = configs[0]
        self.assertEqual(len(config["outbounds"]), 1)
        outbound = config["outbounds"][0]
        self.assertEqual(outbound["protocol"], "vless")
        self.assertEqual(outbound["settings"]["vnext"], [{"address": "87.199.131.176", "port": 443,
                         "users": [{"id": UUID, "encryption": "none", "flow": "xtls-rprx-vision"}]}])
        self.assertEqual(outbound["streamSettings"], {"network": "tcp", "security": "reality", "realitySettings": {
            "serverName": PARAMETERS["sni"], "fingerprint": "chrome", "publicKey": PUBLIC_KEY,
            "shortId": PARAMETERS["sid"], "spiderX": SPIDER}})
        self.assertEqual(config["inbounds"], [{"listen": "127.0.0.1", "port": 55321, "protocol": "socks",
                                             "settings": {"auth": "noauth", "udp": False}}])
        curl = json.loads((self.root / "curl-args.json").read_text())
        self.assertEqual(curl[curl.index("--proxy") + 1], "socks5h://127.0.0.1:55321")
        self.assertEqual(curl[curl.index("--noproxy") + 1], "")
        self.assertIn("--fail", curl)
        self.assertNotIn("--insecure", curl)
        self.assertNotIn("-k", curl)

    def test_rejects_xhttp_duplicate_and_wrong_endpoint(self):
        for profile in (PROFILE.replace("type=tcp", "type=xhttp"),
                        PROFILE.replace("#Synthetic", "&sni=other.example#Synthetic"),
                        PROFILE.replace("87.199.131.176:443", "162.35.172.6:8443")):
            with self.subTest(profile_type=profile.split("?", 1)[0].split("@")[-1]):
                code, output = self.run_probe(profile)
                self.assertNotEqual(code, 0)
                self.assertNotIn("RESULT=PASS", output)
                self.assertFalse((self.root / "configs.jsonl").exists())

    def test_failure_paths_hide_core_and_curl_output_and_stop_child(self):
        for mode in ("invalid_config", "startup_exit", "curl_fail", "wrong_ip"):
            with self.subTest(mode=mode):
                for name in ("xray.pid", "configs.jsonl", "curl-args.json"):
                    (self.root / name).unlink(missing_ok=True)
                code, output = self.run_probe(mode=mode)
                self.assertNotEqual(code, 0)
                self.assertNotIn("RESULT=PASS", output)

    def test_busy_port_does_not_start_xray_or_stop_foreign_process(self):
        foreign = subprocess.Popen(["sleep", "30"])
        self.addCleanup(foreign.wait)
        self.addCleanup(foreign.terminate)
        code, output = self.run_probe(mode="busy", foreign_pid=str(foreign.pid))
        self.assertNotEqual(code, 0)
        self.assertNotIn("RESULT=PASS", output)
        self.assertFalse((self.root / "configs.jsonl").exists())
        self.assertIsNone(foreign.poll())

    def test_interrupt_restores_terminal(self):
        code, output = self.run_probe(interrupt=True)
        self.assertEqual(code, 143, output)
        self.assertFalse((self.root / "configs.jsonl").exists())


if __name__ == "__main__":
    unittest.main()
