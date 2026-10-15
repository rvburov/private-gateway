#!/usr/bin/env python3
"""Check one active production profile through the running Docker Xray gateway.

The profile is fetched inside the API container and never printed. The client
connects through the host's loopback publication of the Xray container.
"""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from urllib.parse import parse_qs, urlsplit
import uuid


XRAY = Path("/opt/private-gateway-docker/current/bin/xray")
SERVER_CONFIG = Path("/var/lib/private-gateway-docker/xray/xray.json")
EXPORT = """
import hashlib
import json
import os
import urllib.request
from urllib.parse import parse_qs, urlsplit

headers = {"Authorization": "Bearer " + os.environ["GATEWAY_API_TOKEN"]}
base = "http://127.0.0.1:8080/api/v1"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
def get(path):
    request = urllib.request.Request(base + path, headers=headers)
    with opener.open(request, timeout=5) as response:
        return json.load(response)
devices = get("/devices")["devices"]
active = [device for device in devices if device.get("revoked_at") is None]
if not active:
    raise SystemExit(2)
device = active[0]
profile = get("/devices/" + device["id"] + "/profile")["uri"]
node = next(item for item in get("/nodes")["nodes"] if item["id"] == device["node_id"])
uri = urlsplit(profile)
query = parse_qs(uri.query)
with open("/state/xray.json", "rb") as source:
    content = source.read()
    modified = os.fstat(source.fileno()).st_mtime_ns
config = json.loads(content)
with open("/state/xray.status.json") as source:
    status = json.load(source)
inbound = config["inbounds"][0]
reality = inbound["streamSettings"]["realitySettings"]
checks = {
    "profile_client": any(client["id"].lower() == (uri.username or "").lower() and
                          client.get("flow") == "xtls-rprx-vision"
                          for client in inbound["settings"]["clients"]),
    "profile_endpoint": uri.hostname == node["host"] and uri.port == node["port"] and
                        uri.port == inbound["port"],
    "profile_reality": query.get("pbk") == [node["public_key"]] and
                       query.get("sid") == [node["short_id"].lower()] and
                       query.get("sid", [None])[0] in reality["shortIds"] and
                       query.get("sni") == [node["server_name"]] and
                       query.get("sni", [None])[0] in reality["serverNames"],
    "server_key": reality["privateKey"] == os.environ["GATEWAY_REALITY_PRIVATE_KEY"],
    "config_loaded": status.get("running") is True and
                     status.get("sha256") == hashlib.sha256(content).hexdigest() and
                     status.get("mtime_ns") == modified,
}
print(json.dumps({"uri": profile, "checks": checks}))
"""


class SmokeError(Exception):
    pass


def get_profile():
    try:
        result = subprocess.run(
            ["docker", "exec", "private-gateway-api-1", "python3", "-c", EXPORT],
            capture_output=True, text=True, timeout=12, check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SmokeError("API container could not be queried") from error
    if result.returncode == 2:
        raise SmokeError("no active device; create one and apply the configuration")
    if result.returncode != 0:
        raise SmokeError("API container could not export an active profile")
    try:
        exported = json.loads(result.stdout)
        failed = [name for name, passed in exported["checks"].items() if passed is not True]
        if failed:
            raise SmokeError("profile and running Xray differ: " + ", ".join(failed))
        return exported["uri"]
    except (KeyError, TypeError, ValueError) as error:
        raise SmokeError("API container returned an invalid profile check") from error


def client_config(profile, local_port, server_port=None):
    if len(profile) > 4096:
        raise SmokeError("profile is too long")
    try:
        uri = urlsplit(profile)
        parameters = parse_qs(uri.query, strict_parsing=True)
        credential = str(uuid.UUID(uri.username or ""))
        if uri.scheme != "vless" or not uri.hostname or not 1 <= (uri.port or 0) <= 65535:
            raise ValueError("invalid endpoint")
        required = ("encryption", "flow", "type", "security", "sni", "fp", "pbk", "sid", "spx")
        values = {key: parameters[key][0] for key in required if len(parameters[key]) == 1}
        if len(values) != len(required) or any(len(value) > 256 for value in values.values()):
            raise ValueError("invalid parameters")
        if (values["encryption"], values["flow"], values["type"], values["security"]) != (
            "none", "xtls-rprx-vision", "tcp", "reality"
        ):
            raise ValueError("unsupported profile")
    except (KeyError, TypeError, ValueError) as error:
        raise SmokeError("active profile is malformed") from error
    config = {
        "log": {"loglevel": "warning"},
        "inbounds": [{"listen": "127.0.0.1", "port": local_port, "protocol": "socks",
                      "settings": {"auth": "noauth", "udp": False}}],
        "outbounds": [{"protocol": "vless", "settings": {"vnext": [{
            "address": "127.0.0.1", "port": server_port or uri.port,
            "users": [{"id": credential, "encryption": values["encryption"], "flow": values["flow"]}],
        }]}, "streamSettings": {"network": values["type"], "security": values["security"],
                              "realitySettings": {"serverName": values["sni"],
                                                  "fingerprint": values["fp"],
                                                  "publicKey": values["pbk"],
                                                  "shortId": values["sid"],
                                                  "spiderX": values["spx"]}}}],
    }
    return config, values["sni"]


def read_exact(connection, length, stage):
    data = bytearray()
    while len(data) < length:
        part = connection.recv(length - len(data))
        if not part:
            raise SmokeError(stage + ": connection closed before a response")
        data.extend(part)
    return bytes(data)


def probe_socks(port, target, destination=None):
    destination_host, destination_port = destination or (target, 443)
    hostname = destination_host.encode("ascii")
    if not hostname or len(hostname) > 253:
        raise SmokeError("profile server name is invalid")
    stage = "local SOCKS connection"
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=8) as connection:
            connection.settimeout(10)
            connection.sendall(b"\x05\x01\x00")
            if read_exact(connection, 2, stage) != b"\x05\x00":
                raise SmokeError("local SOCKS negotiation failed")
            stage = "VLESS target connection"
            connection.sendall(b"\x05\x01\x00\x03" + bytes([len(hostname)]) + hostname + destination_port.to_bytes(2, "big"))
            reply = read_exact(connection, 4, stage)
            if reply[:2] != b"\x05\x00":
                raise SmokeError("VLESS could not connect to the target")
            length = {1: 4, 4: 16}.get(reply[3])
            if reply[3] == 3:
                length = read_exact(connection, 1, stage)[0]
            if length is None:
                raise SmokeError("unexpected SOCKS response")
            read_exact(connection, length + 2, stage)
            stage = "HTTPS handshake through VLESS"
            with ssl.create_default_context().wrap_socket(connection, server_hostname=target) as tls:
                stage = "HTTPS response through VLESS"
                tls.sendall(b"GET / HTTP/1.1\r\nHost: " + target.encode("ascii") + b"\r\nConnection: close\r\n\r\n")
                if not tls.recv(128).startswith(b"HTTP/"):
                    raise SmokeError("target did not answer through VLESS")
                return tls.version()
    except (OSError, ssl.SSLError) as error:
        raise SmokeError(f"{stage} failed ({type(error).__name__})") from error


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)


def start_isolated_server(directory, port):
    process = None
    try:
        config = json.loads(SERVER_CONFIG.read_text())
        config["log"]["loglevel"] = "info"
        config["inbounds"][0]["listen"] = "127.0.0.1"
        config["inbounds"][0]["port"] = port
        path = Path(directory) / "server.json"
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            json.dump(config, output)
        log = Path(directory) / "server.log"
        descriptor = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            process = subprocess.Popen([str(XRAY), "run", "-config", str(path)],
                                       stdout=output, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise SmokeError("isolated Xray server exited during startup")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    return process, log
            except OSError:
                time.sleep(0.1)
        raise SmokeError("isolated Xray server did not start")
    except (OSError, ValueError, KeyError, IndexError) as error:
        if process is not None:
            stop(process)
        raise SmokeError("isolated Xray configuration could not be prepared") from error
    except SmokeError:
        if process is not None:
            stop(process)
        raise


def isolated_result(log):
    # Xray logs can contain client IDs and IPs. Report only a fixed category.
    try:
        content = log.read_text(errors="replace")
    except OSError:
        return "log unavailable"
    if "invalid request user id" in content:
        return "client UUID rejected"
    if "invalid request" in content:
        return "VLESS request rejected"
    if "server name mismatch" in content:
        return "REALITY server name mismatch"
    if "authentication failed or validation criteria not met" in content:
        return "REALITY authentication failed"
    for line in content.splitlines():
        marker = "REALITY: processed invalid connection from "
        if marker not in line:
            continue
        cause = line.partition(marker)[2].partition(": ")[2].strip()
        if cause == "failed to read client hello":
            continue  # Readiness probe connects without sending any TLS bytes.
        if (re.fullmatch(r"[A-Za-z][A-Za-z .,_'()-]{0,119}", cause)
                and all(len(word) <= 20 for word in re.findall(r"[A-Za-z]+", cause))):
            return "REALITY rejected: " + cause
        return "REALITY rejected: unclassified reason"
    if "accepted tcp:" in content:
        return "VLESS request accepted"
    return "no classified handshake event"


def check_reality_public_key(profile):
    # OpenSSL receives the private key on stdin, never in argv or terminal output.
    try:
        config = json.loads(SERVER_CONFIG.read_text())
        private_key = config["inbounds"][0]["streamSettings"]["realitySettings"]["privateKey"]
        raw = base64.urlsafe_b64decode(private_key + "=" * (-len(private_key) % 4))
        if len(raw) != 32:
            raise ValueError("invalid private key")
        pkcs8 = bytes.fromhex("302e020100300506032b656e04220420") + raw
        result = subprocess.run(["openssl", "pkey", "-inform", "DER", "-pubout", "-outform", "DER"],
                                input=pkcs8, capture_output=True, timeout=5, check=False)
        spki_prefix = bytes.fromhex("302a300506032b656e032100")
        if result.returncode != 0 or not result.stdout.startswith(spki_prefix) or len(result.stdout) != len(spki_prefix) + 32:
            raise ValueError("could not derive public key")
        public_key = base64.urlsafe_b64encode(result.stdout[len(spki_prefix):]).rstrip(b"=").decode("ascii")
        expected = parse_qs(urlsplit(profile).query)["pbk"][0]
    except (OSError, ValueError, KeyError, IndexError, subprocess.TimeoutExpired) as error:
        raise SmokeError("REALITY public key could not be checked") from error
    if public_key != expected:
        raise SmokeError("profile REALITY public key differs from server private key")


def run_client(directory, config, target, port):
    path = Path(directory) / "client.json"
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as output:
        json.dump(config, output)
    try:
        process = subprocess.Popen([str(XRAY), "run", "-config", str(path)],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except OSError as error:
        raise SmokeError("local Xray client could not be launched") from error
    try:
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise SmokeError("local Xray client exited during startup")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise SmokeError("local Xray client did not start")
        return probe_socks(port, target)
    finally:
        stop(process)


def main():
    parser = argparse.ArgumentParser(description="Check an active profile through the installed Xray gateway")
    parser.add_argument("--isolated", action="store_true", help="check the same server config with a temporary loopback Xray")
    args = parser.parse_args()
    if not XRAY.is_file():
        raise SmokeError("installed Xray binary is missing")
    profile = get_profile()
    if args.isolated:
        check_reality_public_key(profile)
    port = free_port()
    server_port = free_port() if args.isolated else None
    while server_port == port:
        server_port = free_port()
    config, target = client_config(profile, port, server_port)
    with tempfile.TemporaryDirectory(prefix="private-gateway-vpn-smoke-") as directory:
        server = None
        server_log = None
        try:
            if args.isolated:
                server, server_log = start_isolated_server(directory, server_port)
            try:
                protocol = run_client(directory, config, target, port)
            except SmokeError as error:
                if server is not None:
                    stop(server)
                    server = None
                    raise SmokeError(f"{error}; isolated Xray: {isolated_result(server_log)}") from None
                raise
        finally:
            if server is not None:
                stop(server)
    print("VLESS traffic OK:", protocol)


if __name__ == "__main__":
    try:
        main()
    except SmokeError as error:
        print("VPN smoke failed:", error, file=sys.stderr)
        raise SystemExit(1) from None
