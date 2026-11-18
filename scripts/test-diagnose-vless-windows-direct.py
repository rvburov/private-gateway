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
args = sys.argv[1:]
with (root / "curl-args.jsonl").open("a") as out:
    out.write(json.dumps(args) + "\n")
mode = os.environ.get("MOCK_MODE")
secret = os.environ["MOCK_SECRET"]
url = args[-1]
if url != "https://api.ipify.org/":
    health = url == "https://cp.cloudflare.com/generate_204"
    assert health or url == "https://1.1.1.1/cdn-cgi/trace"
    status, tcp, tls, total, code = ("204" if health else "200"), "0.02", "0.15", "0.16", 0
    body = "" if health else "fl=synthetic\nip=162.35.172.6\nuag=" + secret + "\n"
    if health and mode == "health_timeout":
        status, tcp, tls, total, code = "000", "0.000000", "0.000000", "10.000000", 28
        body = secret
    elif health and mode == "health_certificate":
        status, tls, code = "000", "0.000000", 60
        body = secret
    elif health and mode.startswith("health_status_"):
        status = mode.rsplit("_", 1)[1]
        body = secret
    elif health and mode == "health_headers_timeout":
        status, total, code = "200", "10.000000", 28
        body = secret
    elif health and mode == "health_headers_large":
        status, code = "200", 63
        body = secret
    elif not health and mode == "trace_wrong_ip":
        body = "ip=203.0.113.7\nsecret=" + secret
    elif not health and mode == "trace_invalid_ip":
        body = "ip=999.35.172.6\nsecret=" + secret
    elif not health and mode == "trace_missing_ip":
        body = "secret=" + secret
    elif not health and mode.startswith("trace_status_"):
        status = mode.rsplit("_", 1)[1]
        body = "ip=162.35.172.6\nsecret=" + secret
    elif not health and mode == "trace_timeout":
        status, total, code = "000", "10.000000", 28
        body = secret
    malformed = health and mode == "health_malformed_metrics"
    if malformed:
        body = secret + "\nPG_METRICS:204:0.02:0.15:0.16"
    sys.stdout.write(body + "\nPG_METRICS:" +
                     ("invalid:0.02:0.15:0.16" if malformed else ":".join((status, tcp, tls, total))))
    print(secret, file=sys.stderr)
    raise SystemExit(code)
(root / "curl-args.json").write_text(json.dumps(args))
config = json.loads((root / "configs.jsonl").read_text().splitlines()[-1])
bound = "interface" in config["outbounds"][0]["streamSettings"].get("sockopt", {})
if mode == "curl_fail" or (mode == "interface_curl_fail" and bound):
    print(os.environ["MOCK_SECRET"], file=sys.stderr)
    print(os.environ["MOCK_SECRET"])
    raise SystemExit(28)
print("203.0.113.7" if mode == "wrong_ip" else "162.35.172.6")
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

    def run_probe(self, profile=PROFILE, mode="pass", foreign_pid="", interrupt=False, app_targets=False,
                  extra_args=(), trailing_args=()):
        env = os.environ.copy()
        env.update(PATH=str(self.root) + os.pathsep + env["PATH"], MOCK_ROOT=str(self.root),
                   MOCK_MODE=mode, MOCK_SECRET=PROFILE, MOCK_FOREIGN_PID=foreign_pid,
                   PG_DIAG_LOCAL_PORT="55321")
        pid, master = pty.fork()
        if pid == 0:
            args = ["bash", str(SCRIPT)]
            if app_targets:
                args.append("--app-targets")
            args.extend(extra_args)
            os.execve("/bin/bash", args + [str(self.root / "xray.exe"), *trailing_args], env)
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
        self.assertIn("OUTSIDE_BINDING=off", output)
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

    def test_unicode_interface_is_the_only_config_difference(self):
        code, output = self.run_probe()
        self.assertEqual(code, 0, output)
        baseline = json.loads((self.root / "configs.jsonl").read_text().splitlines()[-1])
        for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
            (self.root / name).unlink(missing_ok=True)

        interface = "Беспроводная сеть"
        code, output = self.run_probe(extra_args=("--interface", interface), app_targets=True)
        self.assertEqual(code, 0, output)
        self.assertIn("OUTSIDE_BINDING=on", output)
        self.assertNotIn(interface, output)
        self.assertIn("APP_TARGETS_RESULT=PASS", output)
        configs = [json.loads(row) for row in (self.root / "configs.jsonl").read_text().splitlines()]
        self.assertEqual(len(configs), 2)
        self.assertEqual(configs[0], configs[1])
        expected = json.loads(json.dumps(baseline))
        expected["outbounds"][0]["streamSettings"]["sockopt"] = {"interface": interface}
        self.assertEqual(configs[0], expected)

    def test_interface_quotes_backslash_and_utf8_byte_boundary_are_encoded_safely(self):
        for interface in ('Wi-Fi "bridge"\\adapter', "я" * 128):
            with self.subTest(interface_bytes=len(interface.encode())):
                for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
                    (self.root / name).unlink(missing_ok=True)
                code, output = self.run_probe(trailing_args=("--interface", interface, "--app-targets"))
                self.assertEqual(code, 0, output)
                self.assertIn("OUTSIDE_BINDING=on", output)
                self.assertNotIn(interface, output)
                self.assertIn("APP_TARGETS_RESULT=PASS", output)
                configs = [json.loads(row) for row in (self.root / "configs.jsonl").read_text().splitlines()]
                self.assertEqual(len(configs), 2)
                self.assertEqual(configs[0], configs[1])
                self.assertEqual(configs[0]["outbounds"][0]["streamSettings"]["sockopt"],
                                 {"interface": interface})

    def test_interface_invalid_values_are_rejected_before_core_start(self):
        invalid = ("", "bridge\nforbidden", "bridge\tforbidden", "bridge\x7fforbidden",
                   "bridge\x01forbidden", "x" * 257, "я" * 129)
        for interface in invalid:
            with self.subTest(interface_bytes=len(interface.encode())):
                code, output = self.run_probe(trailing_args=("--interface", interface))
                self.assertNotEqual(code, 0, output)
                self.assertNotIn("STAGE=xray_running", output)
                self.assertNotIn("RESULT=PASS", output)
                self.assertNotIn("forbidden", output)
                self.assertFalse((self.root / "configs.jsonl").exists())
                self.assertFalse((self.root / "xray.pid").exists())
                self.assertFalse((self.root / "curl-args.jsonl").exists())
        code, output = self.run_probe(trailing_args=("--interface",))
        self.assertNotEqual(code, 0, output)
        self.assertNotIn("STAGE=xray_running", output)
        self.assertFalse((self.root / "configs.jsonl").exists())

    def test_binding_failure_does_not_change_unbound_result_or_leak_child_output(self):
        code, output = self.run_probe(mode="interface_curl_fail", app_targets=True)
        self.assertEqual(code, 0, output)
        self.assertIn("OUTSIDE_BINDING=off", output)
        self.assertIn("APP_TARGETS_RESULT=PASS", output)
        for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
            (self.root / name).unlink(missing_ok=True)
        interface = "Беспроводная сеть"
        code, output = self.run_probe(mode="interface_curl_fail", app_targets=True,
                                      extra_args=("--interface", interface))
        self.assertNotEqual(code, 0, output)
        self.assertIn("OUTSIDE_BINDING=on", output)
        self.assertNotIn(interface, output)
        self.assertIn("CURL_EXIT=28", output)
        self.assertNotIn("BASELINE_RESULT=PASS", output)
        self.assertNotIn("APP_TARGET=", output)
        self.assertNotIn("RESULT=PASS", output)
        requests = [json.loads(row) for row in (self.root / "curl-args.jsonl").read_text().splitlines()]
        self.assertEqual([request[-1] for request in requests], ["https://api.ipify.org/"])

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
                for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
                    (self.root / name).unlink(missing_ok=True)
                code, output = self.run_probe(mode=mode)
                self.assertNotEqual(code, 0)
                self.assertNotIn("RESULT=PASS", output)

    def app_output(self, mode):
        for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
            (self.root / name).unlink(missing_ok=True)
        code, output = self.run_probe(mode=mode, app_targets=True)
        self.assertIn("BASELINE_RESULT=PASS", output)
        requests = [json.loads(row) for row in (self.root / "curl-args.jsonl").read_text().splitlines()]
        self.assertEqual([request[-1] for request in requests], ["https://api.ipify.org/",
                         "https://cp.cloudflare.com/generate_204", "https://1.1.1.1/cdn-cgi/trace"])
        self.assertEqual(output.count("APP_TARGET="), 2)
        configs = [json.loads(row) for row in (self.root / "configs.jsonl").read_text().splitlines()]
        self.assertEqual(len(configs), 2, "Все адреса должны проверяться одним временным ядром")
        for request in requests[1:]:
            for flag, value in (("--proxy", "socks5h://127.0.0.1:55321"), ("--noproxy", ""),
                                ("--connect-timeout", "10"), ("--max-time", "10"),
                                ("--max-filesize", "4096"), ("--proto", "=https")):
                self.assertEqual(request[request.index(flag) + 1], value)
            self.assertIn("--http1.1", request)
            self.assertNotIn("--insecure", request)
            self.assertNotIn("-k", request)
            self.assertNotIn("--location", request)
            self.assertNotIn("-L", request)
            self.assertNotIn("--fail", request, "Проверка заголовков приложения принимает HTTP-ошибки")
            self.assertEqual(request[request.index("--write-out") + 1],
                             r"\nPG_METRICS:%{http_code}:%{time_connect}:%{time_appconnect}:%{time_total}")
        health, trace = output.split("APP_TARGET=health", 1)[1].split("APP_TARGET=trace", 1)
        return code, output, health, trace

    def test_app_targets_accept_health_headers_and_validate_trace_over_the_same_core(self):
        for mode in ("pass", "health_status_301", "health_status_403", "health_status_500",
                     "health_headers_timeout", "health_headers_large"):
            with self.subTest(mode=mode):
                code, output, health, trace = self.app_output(mode)
                self.assertEqual(code, 0, output)
                self.assertIn("APP_HTTP_REACHED=yes", health)
                self.assertIn("APP_TARGET_RESULT=PASS", health)
                self.assertIn("APP_HTTP=200", trace)
                self.assertIn("APP_PUBLIC_IP=162.35.172.6", trace)
                self.assertIn("APP_TARGET_RESULT=PASS", trace)
                self.assertIn("APP_TARGETS_RESULT=PASS", output)
                self.assertTrue(output.rstrip().endswith("RESULT=PASS"), output)
                if mode == "health_headers_timeout":
                    self.assertIn("APP_CURL_EXIT=28", health)
                    self.assertIn("APP_TOTAL=10.000000", health)

    def test_app_target_failures_continue_without_leaking_raw_responses(self):
        for mode in ("health_timeout", "health_certificate", "health_malformed_metrics",
                     "trace_wrong_ip", "trace_invalid_ip", "trace_missing_ip",
                     "trace_status_301", "trace_status_503", "trace_timeout"):
            with self.subTest(mode=mode):
                code, output, health, trace = self.app_output(mode)
                self.assertEqual(code, 1, output)
                self.assertIn("APP_TARGETS_RESULT=FAIL", output)
                self.assertNotIn("\nRESULT=PASS", output.replace("\r\n", "\n"))
                target = health if mode.startswith("health_") else trace
                self.assertIn("APP_TARGET_RESULT=FAIL", target)
                if mode.startswith("health_"):
                    self.assertIn("APP_TARGET_RESULT=PASS", trace)
                if mode in ("health_timeout", "health_certificate", "trace_timeout"):
                    self.assertIn("APP_HTTP=000", target)
                    self.assertIn("APP_HTTP_REACHED=no", target)
                if mode == "health_timeout":
                    self.assertIn("APP_CURL_EXIT=28", health)
                    self.assertIn("APP_TCP=0.000000", health)
                    self.assertIn("APP_TLS=0.000000", health)
                    self.assertIn("APP_TOTAL=10.000000", health)
                if mode == "trace_invalid_ip":
                    self.assertNotIn("999.35.172.6", output)

    def test_app_targets_stop_if_the_baseline_fails(self):
        for mode in ("curl_fail", "wrong_ip"):
            with self.subTest(mode=mode):
                for name in ("xray.pid", "configs.jsonl", "curl-args.json", "curl-args.jsonl"):
                    (self.root / name).unlink(missing_ok=True)
                code, output = self.run_probe(mode=mode, app_targets=True)
                self.assertNotEqual(code, 0)
                self.assertNotIn("BASELINE_RESULT=PASS", output)
                self.assertNotIn("APP_TARGET=", output)
                requests = [json.loads(row) for row in (self.root / "curl-args.jsonl").read_text().splitlines()]
                self.assertEqual([request[-1] for request in requests], ["https://api.ipify.org/"])

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
