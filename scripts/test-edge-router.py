#!/usr/bin/env python3
"""Проверить, что production-маршрутизатор TCP отправляет SNI панели в Caddy, а SNI VPN — в Xray."""

from pathlib import Path
import json
import os
import re
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import uuid
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[1]
# Тот же закреплённый образ, что в production-стеке.
HAPROXY = re.search(r"^ +image: (haproxy:\S+)$", (ROOT / "deploy/docker/compose.yml").read_text(), re.M).group(1)


def check_public_ports():
    # Compose нормализует короткие записи портов; тест проверяет реальные публикации сервисов.
    environment = dict(os.environ)
    environment.update({
        "POSTGRES_PASSWORD": "test-password", "GATEWAY_API_TOKEN": "test-token",
        "GATEWAY_MASTER_KEY": "test-key", "GATEWAY_REALITY_PRIVATE_KEY": "test-reality-key",
        "GATEWAY_SHORT_ID": "0011223344556677", "GATEWAY_HOST": "vpn.example.test",
        "BROWSER_WORKER_TOKEN": "test-worker-token", "PANEL_DOMAIN": "panel.example.test",
        "GATEWAY_STATE_DIR": "/tmp/private-gateway-port-test-state",
        "GATEWAY_CADDYFILE": "/tmp/private-gateway-port-test-Caddyfile",
        "PG_RELEASE_SHA": "0" * 40,
    })
    config = json.loads(subprocess.check_output([
        "docker", "compose", "-f", str(ROOT / "deploy/docker/compose.yml"),
        "config", "--format", "json"], env=environment, text=True))
    publications = [(name, port.get("host_ip"), str(port["published"]),
                     port["target"], port.get("protocol", "tcp"))
                    for name, service in config["services"].items()
                    for port in service.get("ports", [])]
    assert sorted(publications) == sorted([
        ("caddy", "0.0.0.0", "80", 80, "tcp"),
        ("edge", "0.0.0.0", "443", 443, "tcp"),
        ("edge", "0.0.0.0", "8443", 443, "tcp"),
    ]), publications
    api = config["services"]["api"]["environment"]
    assert str(api["GATEWAY_PORT"]) == "8443", "profiles must use public port 8443"
    assert str(api["GATEWAY_XRAY_PORT"]) == "443", "Xray must keep its internal port 443"
    assert str(api.get("GATEWAY_FALLBACK_PORT", "0")) == "0", "there is one exported endpoint"
    browser = urlsplit(api["GATEWAY_BROWSER_PUBLIC_URL"])
    assert browser.scheme == "https" and browser.port == 8443, browser
    print("Compose ports: Caddy owns 80; HAProxy owns 443 and 8443; private services are not published")


def listener(label):
    server = socket.socket()
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("127.0.0.1", 0))
    server.listen()
    server.settimeout(1)
    stop = threading.Event()

    def serve():
        while not stop.is_set():
            try:
                peer, _ = server.accept()
            except socket.timeout:
                continue
            except OSError:
                break
            with peer:
                peer.settimeout(2)
                try:
                    if peer.recv(4096):
                        peer.sendall(label.encode())
                except OSError:
                    pass

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    return server, stop, thread


def hello(name):
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.check_hostname = False
    incoming, outgoing = ssl.MemoryBIO(), ssl.MemoryBIO()
    connection = context.wrap_bio(incoming, outgoing, server_hostname=name)
    try:
        connection.do_handshake()
    except ssl.SSLWantReadError:
        pass
    return outgoing.read()


def main():
    check_public_ports()
    name = "private-gateway-edge-router-" + uuid.uuid4().hex
    panel, panel_stop, panel_thread = listener("panel")
    vpn, vpn_stop, vpn_thread = listener("vpn")
    edge = socket.socket()
    edge.bind(("127.0.0.1", 0))
    edge_port = edge.getsockname()[1]
    edge.close()
    source = (ROOT / "deploy/docker/haproxy.cfg.template").read_text()
    config = (source.replace("__PANEL_DOMAIN__", "panel.example.test")
              .replace("bind *:443", f"bind 127.0.0.1:{edge_port}")
              .replace("caddy:443", f"127.0.0.1:{panel.getsockname()[1]}")
              .replace("xray:443", f"127.0.0.1:{vpn.getsockname()[1]}"))
    process = None
    try:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "haproxy.cfg"
            path.write_text(config)
            path.chmod(0o644)
            process = subprocess.Popen([
                "docker", "run", "--rm", "--name", name, "--network", "host", "--read-only", "--cap-drop=ALL",
                "--cap-add=NET_BIND_SERVICE", "--security-opt=no-new-privileges",
                "-v", f"{path}:/usr/local/etc/haproxy/haproxy.cfg:ro",
                HAPROXY, "haproxy", "-f", "/usr/local/etc/haproxy/haproxy.cfg"],
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            for _ in range(50):
                try:
                    with socket.create_connection(("127.0.0.1", edge_port), timeout=0.2):
                        break
                except OSError:
                    if process.poll() is not None:
                        raise RuntimeError("HAProxy exited: " + process.stderr.read()[-1000:])
                    time.sleep(0.1)
            else:
                raise RuntimeError("HAProxy did not start")
            for hostname, expected in (("panel.example.test", b"panel"),
                                       ("www.cloudflare.com", b"vpn")):
                with socket.create_connection(("127.0.0.1", edge_port), timeout=3) as connection:
                    connection.settimeout(3)
                    connection.sendall(hello(hostname))
                    actual = connection.recv(100)
                assert actual == expected, (hostname, actual, expected)
            print("HAProxy internal TCP 443 routing: panel SNI -> Caddy; REALITY SNI -> Xray")
    finally:
        if process is not None:
            process.terminate()
            try:
                process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate()
            subprocess.run(["docker", "rm", "--force", name], capture_output=True, check=False)
        panel_stop.set()
        vpn_stop.set()
        panel.close()
        vpn.close()
        panel_thread.join(timeout=2)
        vpn_thread.join(timeout=2)


if __name__ == "__main__":
    main()
