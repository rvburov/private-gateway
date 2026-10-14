#!/usr/bin/env python3
"""Check one active production profile through the running Docker Xray gateway.

The profile is fetched inside the API container and never printed. The client
connects through the host's loopback publication of the Xray container.
"""

import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from urllib.parse import parse_qs, urlsplit
import uuid


XRAY = Path("/opt/private-gateway-docker/current/bin/xray")
EXPORT = """
import json
import os
import urllib.request

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
print(get("/devices/" + active[0]["id"] + "/profile")["uri"])
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
    return result.stdout.strip()


def client_config(profile, local_port):
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
            "address": "127.0.0.1", "port": uri.port,
            "users": [{"id": credential, "encryption": values["encryption"], "flow": values["flow"]}],
        }]}, "streamSettings": {"network": values["type"], "security": values["security"],
                              "realitySettings": {"serverName": values["sni"],
                                                  "fingerprint": values["fp"],
                                                  "publicKey": values["pbk"],
                                                  "shortId": values["sid"],
                                                  "spiderX": values["spx"]}}}],
    }
    return config, values["sni"]


def read_exact(connection, length):
    data = bytearray()
    while len(data) < length:
        part = connection.recv(length - len(data))
        if not part:
            raise SmokeError("VLESS connection closed before a response")
        data.extend(part)
    return bytes(data)


def probe_socks(port, target, destination=None):
    destination_host, destination_port = destination or (target, 443)
    hostname = destination_host.encode("ascii")
    if not hostname or len(hostname) > 253:
        raise SmokeError("profile server name is invalid")
    with socket.create_connection(("127.0.0.1", port), timeout=8) as connection:
        connection.settimeout(10)
        connection.sendall(b"\x05\x01\x00")
        if read_exact(connection, 2) != b"\x05\x00":
            raise SmokeError("local SOCKS negotiation failed")
        connection.sendall(b"\x05\x01\x00\x03" + bytes([len(hostname)]) + hostname + destination_port.to_bytes(2, "big"))
        reply = read_exact(connection, 4)
        if reply[:2] != b"\x05\x00":
            raise SmokeError("VLESS could not connect to the target")
        length = {1: 4, 4: 16}.get(reply[3])
        if reply[3] == 3:
            length = read_exact(connection, 1)[0]
        if length is None:
            raise SmokeError("unexpected SOCKS response")
        read_exact(connection, length + 2)
        with ssl.create_default_context().wrap_socket(connection, server_hostname=target) as tls:
            tls.sendall(b"GET / HTTP/1.1\r\nHost: " + target.encode("ascii") + b"\r\nConnection: close\r\n\r\n")
            if not tls.recv(128).startswith(b"HTTP/"):
                raise SmokeError("target did not answer through VLESS")
            return tls.version()


def main():
    if not XRAY.is_file():
        raise SmokeError("installed Xray binary is missing")
    profile = get_profile()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    config, target = client_config(profile, port)
    with tempfile.TemporaryDirectory(prefix="private-gateway-vpn-smoke-") as directory:
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
            protocol = probe_socks(port, target)
        except (OSError, ssl.SSLError) as error:
            raise SmokeError(f"VLESS traffic failed ({type(error).__name__})") from error
        finally:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
    print("VLESS traffic OK:", protocol)


if __name__ == "__main__":
    try:
        main()
    except SmokeError as error:
        print("VPN smoke failed:", error, file=sys.stderr)
        raise SystemExit(1) from None
