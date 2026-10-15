#!/usr/bin/env python3
"""Check the real HTTP -> WebSocket -> VNC path without external dependencies."""
import argparse
import base64
import hashlib
import os
import socket
import struct
import urllib.request


def check(port):
    # A readiness check must not silently use an environment HTTP proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(f"http://127.0.0.1:{port}/vnc.html", timeout=5) as response:
        assert response.status == 200 and b"noVNC" in response.read(), "noVNC HTTP page missing"

    with socket.create_connection(("127.0.0.1", port), timeout=5) as connection:
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET /websockify HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n"
            "Upgrade: websocket\r\nConnection: Upgrade\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Protocol: binary\r\n\r\n"
        )
        connection.sendall(request.encode())
        header = bytearray()
        while not header.endswith(b"\r\n\r\n"):
            chunk = connection.recv(1)
            assert chunk, "WebSocket handshake closed"
            header.extend(chunk)
            assert len(header) <= 65536, "unexpected HTTP header size"
        assert header.startswith((b"HTTP/1.1 101", b"HTTP/1.0 101")), "WebSocket upgrade failed"
        expected = base64.b64encode(
            hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()
        )
        assert expected in header, "invalid WebSocket handshake"
        buffer = bytearray()

        def exact(size):
            data = bytearray()
            while len(data) < size:
                chunk = connection.recv(size - len(data))
                assert chunk, "VNC connection closed"
                data.extend(chunk)
            return bytes(data)

        def read(size):
            while len(buffer) < size:
                first, second = exact(2)
                length = second & 127
                if length == 126:
                    length = struct.unpack("!H", exact(2))[0]
                elif length == 127:
                    length = struct.unpack("!Q", exact(8))[0]
                assert first & 15 in (0, 2), "unexpected WebSocket opcode"
                assert not second & 128, "server frame must not be masked"
                assert length <= 1024 * 1024, "unexpected WebSocket frame size"
                buffer.extend(exact(length))
            result = bytes(buffer[:size])
            del buffer[:size]
            return result

        def write(data):
            mask = os.urandom(4)
            assert len(data) < 126
            connection.sendall(
                bytes((0x82, 0x80 | len(data)))
                + mask
                + bytes(value ^ mask[index % 4] for index, value in enumerate(data))
            )

        version = read(12)
        assert version == b"RFB 003.008\n", "unsupported VNC protocol"
        write(version)
        count = read(1)[0]
        assert 1 in read(count), "expected container-loopback VNC security type"
        write(b"\x01")
        assert read(4) == bytes(4), "VNC authentication result failed"
        write(b"\x01")
        initial = read(24)
        width, height = struct.unpack("!HH", initial[:4])
        name_length = struct.unpack("!I", initial[20:24])[0]
        assert name_length <= 65536, "unexpected VNC desktop name"
        read(name_length)
        assert (width, height) == (1280, 720), "unexpected framebuffer dimensions"
    print(f"noVNC HTTP, WebSocket, VNC handshake: OK; framebuffer {width}x{height}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=6080)
    arguments = parser.parse_args()
    check(arguments.port)
