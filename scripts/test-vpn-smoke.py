#!/usr/bin/env python3
"""Прогнать клиент проверки VPN из релиза против настоящего локального сервера Xray."""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from unittest import mock
import uuid


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("vpn_smoke", ROOT / "deploy/docker/vpn-smoke.py")
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)


def unused_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def wait_port(process, port):
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise AssertionError("test process exited before listening")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                return
        except OSError:
            time.sleep(0.05)
    raise AssertionError("test process did not listen")


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)


class VPNSmokeTest(unittest.TestCase):
    def exported_checks(self, public_port=8443, internal_port=443, configured_internal=443,
                        profile_port=None, xhttp=False, xhttp_path=None, xhttp_flow=None):
        credential = "00000000-0000-4000-8000-000000000000"
        public_key, short_id, server_name = "public-key", "0123456789abcdef", "reality.example"
        profile = (f"vless://{credential}@vpn.example:{profile_port or public_port}?"
                   f"pbk={public_key}&sid={short_id}&sni={server_name}")
        config = {"inbounds": [{"port": internal_port,
                               "settings": {"clients": [{"id": credential, "flow": "xtls-rprx-vision"}]},
                               "streamSettings": {"realitySettings": {
                                   "privateKey": "private-key", "shortIds": [short_id],
                                   "serverNames": [server_name]}}}]}
        responses = {
            "/devices": {"devices": [{"id": "test-device", "node_id": "local"}]},
            "/devices/test-device/profile": {"uri": profile},
            "/nodes": {"nodes": [{"id": "local", "host": "vpn.example", "port": public_port,
                                  "public_key": public_key, "short_id": short_id,
                                  "server_name": server_name}]},
        }
        if xhttp:
            credential = "11111111-2222-3333-4444-555555555555"
            sid, path = "1111222233334444", "/test-xhttp-path-1234"
            node = responses["/nodes"]["nodes"][0]
            node.update(host="192.0.2.10", server_name="example.com", xhttp={
                "port": 8444, "public_key": "xhttp-public-key", "short_id": sid, "path": path, "mode": "auto",
            })
            responses["/devices/test-device/profile"]["xhttp_uri"] = (
                f"vless://{credential}@192.0.2.10:8444?encryption=none&type=xhttp&security=reality&"
                f"pbk=xhttp-public-key&sid={sid}&sni=example.com&path={path}&mode=auto")
            account = {"id": credential}
            if xhttp_flow is not None:
                account["flow"] = xhttp_flow
            config["inbounds"].append({"tag": "vless-xhttp-reality", "port": 8444,
                                       "settings": {"clients": [account]},
                                       "streamSettings": {"network": "xhttp",
                                                          "xhttpSettings": {"path": xhttp_path or path, "mode": "auto"},
                                                          "realitySettings": {"privateKey": "xhttp-private-key",
                                                                              "shortIds": [sid],
                                                                              "serverNames": ["example.com"]}}})
        opener = mock.Mock()
        def open_response(request, timeout):
            path = request.full_url.removeprefix("http://127.0.0.1:8080/api/v1")
            return io.BytesIO(json.dumps(responses[path]).encode())
        opener.open.side_effect = open_response
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            content = json.dumps(config).encode()
            (root / "xray.json").write_bytes(content)
            (root / "xray.status.json").write_text(json.dumps({
                "running": True, "sha256": hashlib.sha256(content).hexdigest(),
                "mtime_ns": (root / "xray.json").stat().st_mtime_ns,
            }))
            original_open = open
            def state_open(path, *args, **kwargs):
                return original_open(root / Path(path).name, *args, **kwargs)
            output = io.StringIO()
            with mock.patch("builtins.open", side_effect=state_open), \
                 mock.patch("urllib.request.build_opener", return_value=opener), \
                 mock.patch.dict(os.environ, {"GATEWAY_API_TOKEN": "test-token",
                                               "GATEWAY_REALITY_PRIVATE_KEY": "private-key",
                                               "GATEWAY_XRAY_PORT": str(configured_internal),
                                               "GATEWAY_SMOKE_TRANSPORT": "xhttp" if xhttp else "tcp",
                                               "GATEWAY_XHTTP_PORT": "8444",
                                               "GATEWAY_XHTTP_PRIVATE_KEY": "xhttp-private-key"}), \
                 contextlib.redirect_stdout(output):
                exec(smoke.EXPORT, {})
        return json.loads(output.getvalue())["checks"]

    def test_public_and_internal_ports_are_checked_independently(self):
        self.assertTrue(all(self.exported_checks().values()))
        self.assertTrue(all(self.exported_checks(public_port=9443, internal_port=2443,
                                                 configured_internal=2443).values()))
        self.assertFalse(self.exported_checks(internal_port=8443)["xray_listen_port"])
        self.assertFalse(self.exported_checks(profile_port=443)["profile_endpoint"])

    def test_xhttp_export_is_checked_against_its_own_running_inbound(self):
        self.assertTrue(all(self.exported_checks(xhttp=True).values()))
        self.assertFalse(self.exported_checks(xhttp=True, xhttp_path="/different-test-path")["profile_xhttp"])
        self.assertFalse(self.exported_checks(xhttp=True, xhttp_flow="xtls-rprx-vision")["profile_client"])

    def test_client_uses_public_profile_port_or_explicit_published_ingress(self):
        profile = ("vless://00000000-0000-4000-8000-000000000000@vpn.example:8443?"
                   "encryption=none&flow=xtls-rprx-vision&type=tcp&security=reality&"
                   "sni=reality.example&fp=chrome&pbk=public-key&sid=0123456789abcdef&spx=%2F")
        config, _ = smoke.client_config(profile, 1080)
        self.assertEqual(config["outbounds"][0]["settings"]["vnext"][0]["port"], 8443)
        config, _ = smoke.client_config(profile.replace(":8443?", ":443?"), 1080, 8443)
        self.assertEqual(config["outbounds"][0]["settings"]["vnext"][0]["port"], 8443)
        for invalid in (0, -1, 65536, True):
            with self.subTest(invalid=invalid), self.assertRaises(smoke.SmokeError):
                smoke.client_config(profile, 1080, invalid)

    def test_xhttp_client_has_no_vision_and_preserves_transport_parameters(self):
        profile = ("vless://11111111-2222-3333-4444-555555555555@192.0.2.10:8444?"
                   "encryption=none&type=xhttp&security=reality&sni=example.com&fp=chrome&"
                   "pbk=public-key&sid=0123456789abcdef&path=%2Ftest-xhttp-path-1234&mode=auto#XHTTP")
        config, name = smoke.client_config(profile, 1080)
        outbound = config["outbounds"][0]
        self.assertEqual(name, "example.com")
        self.assertNotIn("flow", outbound["settings"]["vnext"][0]["users"][0])
        self.assertEqual(outbound["streamSettings"]["network"], "xhttp")
        self.assertEqual(outbound["streamSettings"]["xhttpSettings"],
                         {"path": "/test-xhttp-path-1234", "mode": "auto"})
        for invalid in (profile + "&flow=xtls-rprx-vision", profile.replace("mode=auto", "mode=packet-up"),
                        profile.replace("test-xhttp-path-1234", "short")):
            # Параметры добавляются до фрагмента, иначе они являются именем профиля.
            invalid = invalid.replace("#XHTTP&", "&").replace("#XHTTP", "")
            with self.assertRaises(smoke.SmokeError):
                smoke.client_config(invalid, 1080)

    def test_actual_xhttp_traffic(self):
        binary = os.environ.get("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
        if not binary:
            self.skipTest("set PRIVATE_GATEWAY_REAL_XRAY_BINARY")
        target_port, server_port, client_port = unused_port(), unused_port(), unused_port()
        credential = "11111111-2222-3333-4444-555555555555"
        keys = subprocess.run([binary, "x25519"], capture_output=True, text=True, check=True).stdout
        values = dict(line.split(": ", 1) for line in keys.splitlines() if ": " in line)
        private_key, public_key = values["PrivateKey"], values["Password (PublicKey)"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                "-nodes", "-days", "1", "-subj", "/CN=example.com", "-addext", "subjectAltName=DNS:example.com",
                "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem"),
            ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
            server_config = {
                "log": {"loglevel": "none", "access": "none"},
                "inbounds": [{"tag": "vless-xhttp-reality", "listen": "127.0.0.1", "port": server_port,
                              "protocol": "vless", "settings": {"clients": [{"id": credential}], "decryption": "none"},
                              "streamSettings": {"network": "xhttp", "security": "reality",
                                                 "xhttpSettings": {"path": "/test-xhttp-path-1234", "mode": "auto"},
                                                 "realitySettings": {"target": f"127.0.0.1:{target_port}",
                                                                     "serverNames": ["example.com"],
                                                                     "privateKey": private_key,
                                                                     "shortIds": ["0123456789abcdef"]}}}],
                "outbounds": [{"protocol": "freedom"}],
            }
            (root / "server.json").write_text(json.dumps(server_config))
            target = subprocess.Popen([
                "openssl", "s_server", "-quiet", "-accept", f"127.0.0.1:{target_port}",
                "-cert", str(root / "cert.pem"), "-key", str(root / "key.pem"), "-alpn", "h2,http/1.1", "-www",
            ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            server = subprocess.Popen([binary, "run", "-config", str(root / "server.json")],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                wait_port(target, target_port)
                wait_port(server, server_port)
                profile = (f"vless://{credential}@127.0.0.1:{server_port}?encryption=none&type=xhttp&security=reality"
                           "&sni=example.com&fp=chrome&pbk=" + public_key +
                           "&sid=0123456789abcdef&path=%2Ftest-xhttp-path-1234&mode=auto#XHTTP")
                config, name = smoke.client_config(profile, client_port)
                (root / "client.json").write_text(json.dumps(config))
                client = subprocess.Popen([binary, "run", "-config", str(root / "client.json")],
                                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                try:
                    wait_port(client, client_port)
                    original = smoke.ssl.create_default_context
                    def trusted_context():
                        context = original()
                        context.load_verify_locations(cafile=str(root / "cert.pem"))
                        return context
                    with mock.patch.object(smoke.ssl, "create_default_context", side_effect=trusted_context):
                        for _ in range(2):
                            self.assertEqual(smoke.probe_socks(client_port, name, ("127.0.0.1", target_port)), "TLSv1.3")
                finally:
                    stop(client)
            finally:
                stop(server)
                stop(target)

    def test_actual_vless_traffic(self):
        binary = os.environ.get("PRIVATE_GATEWAY_REAL_XRAY_BINARY")
        if not binary:
            self.skipTest("set PRIVATE_GATEWAY_REAL_XRAY_BINARY")
        target_port, server_port, client_port = unused_port(), unused_port(), unused_port()
        credential = str(uuid.uuid4())
        keys = subprocess.run([binary, "x25519"], capture_output=True, text=True, check=True).stdout
        values = dict(line.split(": ", 1) for line in keys.splitlines() if ": " in line)
        private_key, public_key = values["PrivateKey"], values["Password (PublicKey)"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run([
                "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                "-nodes", "-days", "1", "-subj", "/CN=reality.example",
                "-addext", "subjectAltName=DNS:reality.example",
                "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem"),
            ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
            server_config = {
                "log": {"loglevel": "warning"},
                "inbounds": [{"listen": "127.0.0.1", "port": server_port, "protocol": "vless",
                              "settings": {"clients": [{"id": credential, "flow": "xtls-rprx-vision"}],
                                           "decryption": "none"},
                              "streamSettings": {"network": "raw", "security": "reality",
                                                 "realitySettings": {"target": f"127.0.0.1:{target_port}",
                                                                     "serverNames": ["reality.example"],
                                                                     "privateKey": private_key,
                                                                     "shortIds": ["0123456789abcdef"]}}}],
                "outbounds": [{"protocol": "freedom"}],
            }
            (root / "server.json").write_text(json.dumps(server_config))
            target = subprocess.Popen([
                "openssl", "s_server", "-quiet", "-accept", f"127.0.0.1:{target_port}",
                "-cert", str(root / "cert.pem"), "-key", str(root / "key.pem"), "-www",
            ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            server = subprocess.Popen([binary, "run", "-config", str(root / "server.json")],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                wait_port(target, target_port)
                wait_port(server, server_port)
                uri = (f"vless://{credential}@127.0.0.1:{server_port}?encryption=none&flow=xtls-rprx-vision"
                       "&type=tcp&security=reality&sni=reality.example&fp=chrome&pbk=" + public_key
                       + "&sid=0123456789abcdef&spx=%2F#smoke")
                # В профиле основной порт, а проверка идёт через явно выбранный резервный.
                primary_uri = uri.replace(f":{server_port}?", ":443?")
                client_config, name = smoke.client_config(primary_uri, client_port, server_port)
                self.assertEqual(client_config["outbounds"][0]["settings"]["vnext"][0]["address"], "127.0.0.1")
                (root / "client.json").write_text(json.dumps(client_config))
                client = subprocess.Popen([binary, "run", "-config", str(root / "client.json")],
                                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                try:
                    wait_port(client, client_port)
                    original = smoke.ssl.create_default_context
                    def trusted_context():
                        context = original()
                        context.load_verify_locations(cafile=str(root / "cert.pem"))
                        return context
                    smoke.ssl.create_default_context = trusted_context
                    try:
                        self.assertEqual(smoke.probe_socks(client_port, name,
                                                           ("127.0.0.1", target_port)), "TLSv1.3")
                    finally:
                        smoke.ssl.create_default_context = original
                finally:
                    stop(client)
                isolated_port, isolated_client_port = unused_port(), unused_port()
                alternate = smoke.isolated_server_config(json.loads(json.dumps(server_config)), isolated_port,
                                                         "www.cloudflare.com")
                self.assertEqual(alternate["inbounds"][0]["streamSettings"]["realitySettings"]["target"],
                                 "www.cloudflare.com:443")
                self.assertEqual(alternate["inbounds"][0]["streamSettings"]["realitySettings"]["serverNames"],
                                 ["www.cloudflare.com"])
                isolated_dir = root / "isolate"
                isolated_dir.mkdir()
                with mock.patch.object(smoke, "SERVER_CONFIG", root / "server.json"), \
                     mock.patch.object(smoke, "XRAY", Path(binary)):
                    smoke.check_reality_public_key(uri)
                    with self.assertRaisesRegex(smoke.SmokeError, "public key differs"):
                        alternate = ("A" if public_key[0] != "A" else "B") + public_key[1:]
                        smoke.check_reality_public_key(uri.replace(public_key, alternate))
                    isolated, log = smoke.start_isolated_server(isolated_dir, isolated_port)
                    try:
                        isolated_config, name = smoke.client_config(uri, isolated_client_port, isolated_port)
                        (root / "isolated-client.json").write_text(json.dumps(isolated_config))
                        isolated_client = subprocess.Popen(
                            [binary, "run", "-config", str(root / "isolated-client.json")],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                        )
                        try:
                            wait_port(isolated_client, isolated_client_port)
                            smoke.ssl.create_default_context = trusted_context
                            try:
                                self.assertEqual(smoke.probe_socks(isolated_client_port, name,
                                                                   ("127.0.0.1", target_port)), "TLSv1.3")
                            finally:
                                smoke.ssl.create_default_context = original
                        finally:
                            stop(isolated_client)
                    finally:
                        stop(isolated)
                    self.assertEqual(smoke.isolated_result(log), "VLESS request accepted")
            finally:
                stop(server)
                stop(target)

    def test_rejects_bad_profile_without_echoing_secret(self):
        secret = "private-credential-marker"
        with self.assertRaises(smoke.SmokeError) as caught:
            smoke.client_config("vless://" + secret + "@example.com:443", 1080)
        self.assertNotIn(secret, str(caught.exception))

    def test_profile_mismatch_reports_field_without_echoing_uri(self):
        secret = "vless://private-credential-marker@example.com:443"
        response = subprocess.CompletedProcess([], 0, stdout=json.dumps({
            "uri": secret, "checks": {"profile_client": False, "config_loaded": True},
        }))
        with mock.patch.object(smoke.subprocess, "run", return_value=response):
            with self.assertRaises(smoke.SmokeError) as caught:
                smoke.get_profile()
        self.assertIn("profile_client", str(caught.exception))
        self.assertNotIn(secret, str(caught.exception))

    def test_isolated_log_classification_does_not_echo_client_id(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "server.log"
            secret = "eb0ff969-c4ef-458f-9a88-01cd827c85a8"
            log.write_text("invalid request user id: " + secret)
            result = smoke.isolated_result(log)
        self.assertEqual(result, "client UUID rejected")
        self.assertNotIn(secret, result)

    def test_isolated_reality_reason_redacts_unexpected_values(self):
        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "server.log"
            prefix = "REALITY: processed invalid connection from 127.0.0.1:12345: "
            log.write_text(prefix + "failed to read client hello\n" + prefix + "unsupported client hello")
            self.assertEqual(smoke.isolated_result(log), "REALITY rejected: unsupported client hello")
            log.write_text(prefix + "unexpected 0123456789abcdef0123456789abcdef")
            self.assertEqual(smoke.isolated_result(log), "REALITY rejected: unclassified reason")


if __name__ == "__main__":
    unittest.main()
