#!/usr/bin/env python3
"""Проверить настоящие раздельные HAProxy-процессы моста и неизменность TCP-потока."""

import importlib.util
import json
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import threading
import uuid

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("bridge", ROOT / "deploy/docker/bridge.py")
bridge = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bridge)


def listener(label, context):
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    sock.listen()
    sock.settimeout(0.2)
    stop = threading.Event()
    received = []

    def serve():
        while not stop.is_set():
            try:
                peer, _ = sock.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            with peer:
                peer.settimeout(2)
                try:
                    with context.wrap_socket(peer, server_side=True) as tls:
                        content = bytearray()
                        while len(content) < 1024:
                            part = tls.recv(1024 - len(content))
                            if not part:
                                break
                            content.extend(part)
                        if content:
                            received.append(bytes(content))
                            tls.sendall(label + content)
                except OSError:
                    pass

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    return sock, stop, thread, received


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def free_low_port():
    # Отдельно ловим регрессию bind на 443: высокий случайный порт не проверяет права процесса.
    # Не занимаем рабочий 443/SSH; выбираем свободный тестовый порт ниже 1024.
    for port in range(1023, 999, -1):
        occupied = False
        for host in ("127.0.0.1", "::1"):
            try:
                with socket.create_connection((host, port), timeout=0.1):
                    occupied = True
            except OSError:
                pass
        if not occupied:
            return port
    raise RuntimeError("No free privileged TCP port for the isolated bridge test")


def main():
    name = "private-gateway-bridge-test-" + uuid.uuid4().hex
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        root.chmod(0o755)
        cert, key = root / "cert.pem", root / "key.pem"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
                        "-keyout", str(key), "-out", str(cert)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        server_tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_tls.minimum_version = ssl.TLSVersion.TLSv1_3
        server_tls.load_cert_chain(cert, key)
        client_tls = ssl.create_default_context(cafile=str(cert))
        exits = [listener(b"tcp:", server_tls), listener(b"xhttp:", server_tls)]
        ports = [free_low_port(), free_port()]
        config = bridge.validate({"version": 1, "listeners": [
            {"name": transport, "transport": transport, "listen_port": ports[i],
             "target_host": "127.0.0.1", "target_port": exits[i][0].getsockname()[1]}
            for i, transport in enumerate(("tcp", "xhttp"))]})
        # Пользовательская установка запрещает localhost; здесь это изолированные тестовые backend.
        paths = {}
        for transport in ("tcp", "xhttp"):
            path = root / f"{transport}.cfg"
            path.write_text(bridge.render(config, transport))
            path.chmod(0o644)
            bridge.verify_haproxy(path)
            paths[transport] = path
        document = bridge.compose_document(config, paths)
        document["name"] = name
        # Не восстанавливать тестовые контейнеры после перезагрузки машины CI.
        for service in document["services"].values():
            service["restart"] = "no"
        compose_file = root / "compose.json"
        compose_file.write_text(json.dumps(document))
        base = ["docker", "compose", "-p", name, "-f", str(compose_file)]

        def check(index):
            content = bytes(range(256)) * 4
            with socket.create_connection(("127.0.0.1", ports[index]), timeout=3) as peer:
                peer.settimeout(3)
                with client_tls.wrap_socket(peer, server_hostname="localhost") as tls:
                    assert tls.version() == "TLSv1.3"
                    tls.sendall(content)
                    expected = (b"tcp:" if index == 0 else b"xhttp:") + content
                    result = bytearray()
                    while len(result) < len(expected):
                        part = tls.recv(len(expected) - len(result))
                        if not part:
                            break
                        result.extend(part)
                    assert bytes(result) == expected, (index, len(result))
                    assert exits[index][3][-1] == content

        try:
            subprocess.run([*base, "up", "-d", "--wait", "--wait-timeout", "40"], check=True)
            check(0)
            check(1)
            workers = subprocess.check_output([*base, "exec", "-T", "tcp", "ps", "-o", "pid,user,args"], text=True)
            assert "nobody" in workers, workers
            before = subprocess.check_output([*base, "ps", "-q"], text=True)
            subprocess.run([*base, "up", "-d", "--wait"], check=True)
            assert subprocess.check_output([*base, "ps", "-q"], text=True) == before
            subprocess.run([*base, "stop", "tcp"], check=True)
            check(1)
            print("Bridge low TCP port/worker uid65534/TLS1.3 verified; repeat preserves containers; stopping TCP leaves XHTTP working.")
        finally:
            subprocess.run([*base, "down"], check=True)
            for sock, stop, thread, _ in exits:
                stop.set()
                sock.close()
                thread.join(timeout=3)


if __name__ == "__main__":
    main()
