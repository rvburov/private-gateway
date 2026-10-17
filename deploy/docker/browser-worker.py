#!/usr/bin/env python3
"""Управлять одним одноразовым браузером, не давая API сокет Docker."""

import hmac
import json
import os
import pathlib
import shutil
import signal
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


TOKEN = os.environ["BROWSER_WORKER_TOKEN"]
if len(TOKEN) < 32:
    raise SystemExit("BROWSER_WORKER_TOKEN must contain at least 32 characters")

lock = threading.Lock()
browser = None
temporary_paths = (pathlib.Path("/home/browser/private"), pathlib.Path("/tmp/chromium"), pathlib.Path("/tmp/browser-runtime"))


def clean_temporary_state():
    for path in temporary_paths:
        if path.exists() or path.is_symlink():
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path)
            else:
                path.unlink()
    pathlib.Path("/tmp/browser.pids").unlink(missing_ok=True)


def healthy():
    if browser is None or browser.poll() is not None:
        return False
    try:
        return subprocess.run(
            ["/bin/sh", "/usr/local/bin/healthcheck.sh"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            timeout=4
        ).returncode == 0
    except subprocess.TimeoutExpired:
        return False


def stop_browser():
    global browser
    if browser is not None and browser.poll() is None:
        os.killpg(browser.pid, signal.SIGTERM)
        try:
            browser.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(browser.pid, signal.SIGKILL)
            browser.wait(timeout=5)
    browser = None
    clean_temporary_state()


def start_browser():
    global browser
    if healthy():
        return
    stop_browser()
    # Скрипты запускаются через интерпретатор: некоторые хосты отказываются запускать их по строке #!.
    browser = subprocess.Popen(["/bin/sh", "/usr/local/bin/start.sh"], start_new_session=True)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if browser.poll() is not None:
            stop_browser()
            raise RuntimeError("browser process exited during startup")
        if healthy():
            return
        time.sleep(0.25)
    stop_browser()
    raise RuntimeError("browser did not become ready")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, _format, *_args):
        # Профилям, токенам и трафику браузера не место в журнале worker.
        pass

    def reply(self, status, data):
        body = json.dumps(data, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authorized(self):
        value = self.headers.get("Authorization", "")
        if not hmac.compare_digest(value, "Bearer " + TOKEN):
            self.reply(401, {"error": "unauthorized"})
            return False
        return True

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, {"status": "ready"})
            return
        if self.path != "/status":
            self.reply(404, {"error": "not found"})
            return
        if not self.authorized():
            return
        with lock:
            self.reply(200, {"running": healthy()})

    def do_POST(self):
        if self.path not in ("/start", "/stop"):
            self.reply(404, {"error": "not found"})
            return
        if not self.authorized():
            return
        with lock:
            try:
                if self.path == "/start":
                    start_browser()
                else:
                    stop_browser()
            except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
                print(f"browser worker: {error}", flush=True)
                self.reply(503, {"error": "browser lifecycle failed"})
                return
            self.reply(200, {"running": healthy()})


def shutdown(_signum, _frame):
    with lock:
        stop_browser()
    raise SystemExit(0)


signal.signal(signal.SIGTERM, shutdown)
signal.signal(signal.SIGINT, shutdown)
ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
