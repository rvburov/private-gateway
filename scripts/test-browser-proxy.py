#!/usr/bin/env python3
"""Прогнать маршрут авторизации noVNC в production-Caddy с подставными службами."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import copy
import json
import re
import socket
import subprocess
import tempfile
import threading
import time
import uuid
from urllib.error import HTTPError, URLError
from urllib.request import HTTPRedirectHandler, Request, build_opener, ProxyHandler


ROOT = Path(__file__).resolve().parents[1]
# Тот же закреплённый образ, что в production-стеке.
CADDY = re.search(r"^ +image: (caddy:\S+)$", (ROOT / "deploy/docker/compose.yml").read_text(), re.M).group(1)
opener = build_opener(ProxyHandler({}))
seen = []
DOMAIN = "panel.example.test"


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


redirect_opener = build_opener(ProxyHandler({}), NoRedirect())


def production_config(source):
    config = json.loads(subprocess.run([
        "docker", "run", "--rm", "-i", "--network", "none", CADDY,
        "caddy", "adapt", "--config", "/dev/stdin", "--adapter", "caddyfile"],
        input=source, text=True, capture_output=True, check=True).stdout)
    policies = config["apps"]["tls"]["automation"]["policies"]
    issuers = [issuer for policy in policies for issuer in policy.get("issuers", [])]
    assert issuers, "no explicit ACME policy"
    for issuer in issuers:
        assert issuer["module"] == "acme", "the public certificate must use ACME"
        challenges = issuer.get("challenges", {})
        assert challenges.get("tls-alpn", {}).get("disabled") is True, "TLS-ALPN must be disabled"
        assert challenges.get("http", {}).get("disabled") is not True, "HTTP-01 must stay enabled"
    print("Caddy certificate policy: ACME HTTP-01 enabled, TLS-ALPN disabled")
    return config


def http_fixture(config, panel_port, redirect_port):
    # Только тестовая копия JSON использует HTTP на loopback. Production сохраняет ACME и TLS.
    fixture = copy.deepcopy(config)
    fixture["apps"].pop("tls")
    listeners = {":443": panel_port, ":80": redirect_port}
    observed = set()
    for server in fixture["apps"]["http"]["servers"].values():
        original = server["listen"]
        assert len(original) == 1 and original[0] in listeners, original
        observed.add(original[0])
        server["listen"] = [f"127.0.0.1:{listeners[original[0]]}"]
        server["automatic_https"] = {"disable": True}
        server.pop("tls_connection_policies", None)
    assert observed == set(listeners), observed
    return fixture


def check_redirect(port):
    path = "/devices/example?format=html&tab=all"
    try:
        redirect_opener.open(Request(f"http://127.0.0.1:{port}" + path,
                                    headers={"Host": DOMAIN}), timeout=2)
    except HTTPError as error:
        assert error.code == 301, f"HTTP redirect returned {error.code}"
        assert error.headers["Location"] == f"https://{DOMAIN}:8443" + path, error.headers
    else:
        raise AssertionError("HTTP did not redirect to the canonical port")
    print("Caddy HTTP redirect: HTTPS port 8443, path and query preserved")


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
    headers = {"Authorization": "Bearer should-not-reach-browser", "Host": DOMAIN}
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
    redirect_port = free_port()
    name = "private-gateway-caddy-proxy-" + uuid.uuid4().hex
    source = (ROOT / "deploy/docker/Caddyfile.template").read_text()
    source = (source.replace("__PANEL_DOMAIN__", DOMAIN)
              .replace("api:8080", f"127.0.0.1:{auth.server_port}")
              .replace("browser:6080", f"127.0.0.1:{browser.server_port}"))
    config = http_fixture(production_config(source), port, redirect_port)
    try:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(config))
            path.chmod(0o644)
            process = subprocess.Popen([
                "docker", "run", "--rm", "--name", name, "--network", "host", "--cap-drop=ALL", "--cap-add=NET_BIND_SERVICE",
                "-v", f"{path}:/etc/caddy/config.json:ro", CADDY,
                "caddy", "run", "--config", "/etc/caddy/config.json"],
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
                check_redirect(redirect_port)
                assert status == 403, f"anonymous noVNC returned {status}"
                assert not seen, "anonymous request reached browser"
                status, body = request(port, "browser-test=verified")
                assert (status, body) == (200, b"noVNC test"), f"authorized noVNC returned {status}"
                assert seen == [("/vnc.html", None, None)], f"browser received sensitive headers: {seen}"
                print("Caddy noVNC authorization: anonymous 403, verified 200, credentials stripped")
                # Когда API лежит, Caddy пишет неудачный запрос в журнал, но без секретного пути ссылки.
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
                subprocess.run(["docker", "rm", "--force", name], capture_output=True, check=False)
            assert '"logger":"http.log.error"' in log, "Caddy did not log the failed request"
            assert secret not in log and "/sub/" not in log, "Caddy logged the subscription path"
            print("Caddy error log: the failed request is logged without its URI")
    finally:
        auth.shutdown()
        auth.server_close()
        browser.shutdown()


if __name__ == "__main__":
    main()
