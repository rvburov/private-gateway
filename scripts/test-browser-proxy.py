#!/usr/bin/env python3
"""Exercise the production Caddy noVNC authorization route with mock services."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler


ROOT = Path(__file__).resolve().parents[1]
# The same pinned image as the production stack.
CADDY = re.search(r"^ +image: (caddy:\S+)$", (ROOT / "deploy/docker/compose.yml").read_text(), re.M).group(1)
opener = build_opener(ProxyHandler({}))
seen = []


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Auth(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/browser-authorization":
            self.send_error(404)
        elif self.headers.get("Cookie") == "browser-test=verified":
            self.send_response(204)
            self.end_headers()
        else:
            self.send_error(403)

    def log_message(self, *_args):
        pass


class Browser(BaseHTTPRequestHandler):
    def do_GET(self):
        seen.append((self.path, self.headers.get("Cookie"), self.headers.get("Authorization")))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"noVNC test")

    def log_message(self, *_args):
        pass


def request(port, cookie=None, path="/browser/vnc.html"):
    headers = {"Authorization": "Bearer should-not-reach-browser"}
    if cookie:
        headers["Cookie"] = cookie
    try:
        with opener.open(Request(f"http://127.0.0.1:{port}{path}", headers=headers), timeout=2) as reply:
            return reply.status, reply.read()
    except HTTPError as error:
        return error.code, error.read()


def main():
    auth = ThreadingHTTPServer(("127.0.0.1", 0), Auth)
    browser = ThreadingHTTPServer(("127.0.0.1", 0), Browser)
    for server in (auth, browser):
        threading.Thread(target=server.serve_forever, daemon=True).start()
    port = free_port()
    source = (ROOT / "deploy/docker/Caddyfile.template").read_text()
    config = (source.split("\nhttps://__PANEL_DOMAIN__:8443 {")[0]
              .replace("https://__PANEL_DOMAIN__ {", f"http://127.0.0.1:{port} {{")
              .replace("api:8080", f"127.0.0.1:{auth.server_port}")
              .replace("browser:6080", f"127.0.0.1:{browser.server_port}"))
    assert "__" not in config
    try:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "Caddyfile"
            path.write_text(config)
            path.chmod(0o644)
            process = subprocess.Popen([
                "docker", "run", "--rm", "--network", "host", "--cap-drop=ALL", "--cap-add=NET_BIND_SERVICE",
                "-v", f"{path}:/etc/caddy/Caddyfile:ro", CADDY,
                "caddy", "run", "--config", "/etc/caddy/Caddyfile"],
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            try:
                for _ in range(50):
                    try:
                        status, _ = request(port)
                        break
                    except (URLError, TimeoutError):
                        if process.poll() is not None:
                            raise RuntimeError("Caddy exited before the proxy test: " + process.stderr.read()[-1000:])
                        time.sleep(0.1)
                else:
                    raise RuntimeError("Caddy did not start")
                assert status == 403, f"anonymous noVNC returned {status}"
                assert not seen, "anonymous request reached browser"
                status, body = request(port, "browser-test=verified")
                assert (status, body) == (200, b"noVNC test"), f"authorized noVNC returned {status}"
                assert seen == [("/vnc.html", None, None)], f"browser received sensitive headers: {seen}"
                print("Caddy noVNC authorization: anonymous 403, verified 200, credentials stripped")
                # With the API down, Caddy logs the failed request, but not the link's secret path.
                auth.shutdown()
                auth.server_close()
                secret = "subscription-secret-" + "A" * 23
                status, _ = request(port, path="/sub/" + secret)
                assert status == 502, f"a request to the stopped API returned {status}"
            finally:
                process.terminate()
                try:
                    _, log = process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    _, log = process.communicate()
            assert '"logger":"http.log.error"' in log, "Caddy did not log the failed request"
            assert secret not in log and "/sub/" not in log, "Caddy logged the subscription path"
            print("Caddy error log: the failed request is logged without its URI")
    finally:
        auth.shutdown()
        auth.server_close()
        browser.shutdown()


if __name__ == "__main__":
    main()
