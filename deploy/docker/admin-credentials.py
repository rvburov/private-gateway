#!/usr/bin/env python3
"""Set or replace the panel username, password and TOTP from a root SSH terminal."""

import base64
import fcntl
import getpass
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote
from urllib.request import ProxyHandler, Request, build_opener

ENV = Path("/etc/private-gateway-docker/stack.env")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")
NAMES = ("GATEWAY_ADMIN_USERNAME", "GATEWAY_ADMIN_PASSWORD_HASH", "GATEWAY_ADMIN_TOTP_SECRET")


class SetupError(Exception):
    pass


def value(data, key):
    found = [line.split("=", 1)[1] for line in data.decode().splitlines() if line.startswith(key + "=")]
    if len(found) != 1:
        raise SetupError("stack.env must contain exactly one " + key)
    return found[0]


def code_at_step(secret, step):
    digest = hmac.digest(secret, step.to_bytes(8, "big"), "sha1")
    offset = digest[-1] & 15
    return str((int.from_bytes(digest[offset:offset + 4], "big") & 0x7fffffff) % 1000000).zfill(6)


def valid_code(secret, code):
    step = int(time.time() // 30)
    return len(code) == 6 and code.isascii() and code.isdecimal() and any(
        hmac.compare_digest(code, code_at_step(secret, candidate))
        for candidate in (step - 1, step, step + 1))


def changed_env(old, username, password_hash, totp_secret):
    lines = old.decode().splitlines()
    replacements = dict(zip(NAMES, (username, password_hash, totp_secret)))
    for name in NAMES:
        positions = [index for index, line in enumerate(lines) if line.startswith(name + "=")]
        if len(positions) > 1:
            raise SetupError("duplicate " + name + " in stack.env")
        if positions:
            lines[positions[0]] = name + "=" + replacements[name]
        else:
            lines.append(name + "=" + replacements[name])
    return ("\n".join(lines) + "\n").encode()


def atomic_private_file(path, content):
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".admin-credentials.", delete=False) as output:
        staged = Path(output.name)
        try:
            os.chmod(staged, 0o600)
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        except BaseException:
            staged.unlink(missing_ok=True)
            raise
    try:
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def run(command):
    try:
        result = subprocess.run(command, capture_output=True, timeout=120, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SetupError("Docker command could not complete") from error
    if result.returncode:
        raise SetupError("Docker command failed; inspect container health")


def verify(domain, username, password, code):
    opener = build_opener(ProxyHandler({}))
    base = "https://" + domain + ":8443/api/v1"
    with opener.open(base + "/auth/config", timeout=12) as response:
        if json.load(response) != {"configured": True}:
            raise SetupError("new login mode is not active")
    request = Request(base + "/auth/login", data=json.dumps({
        "username": username, "password": password, "code": code,
    }).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with opener.open(request, timeout=12) as response:
        session = json.load(response)["session"]
    with opener.open(Request(base + "/status", headers={"Authorization": "Bearer " + session}), timeout=12) as response:
        if response.status != 200:
            raise SetupError("new session cannot access the API")


def setup(env=ENV, compose=COMPOSE, backups=BACKUPS, check=verify, execute=run):
    if not compose.is_file():
        raise SetupError("installed Docker release is incomplete")
    metadata = env.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise SetupError("stack.env must be root-owned and private")
    old = env.read_bytes()
    domain = value(old, "PANEL_DOMAIN")
    if not re.fullmatch(r"[A-Za-z0-9.-]+", domain):
        raise SetupError("invalid panel domain")
    current = ""
    for line in old.decode().splitlines():
        if line.startswith("GATEWAY_ADMIN_USERNAME="):
            current = line.split("=", 1)[1]
    username = input(f"Administrator login [{current or 'admin'}]: ").strip() or current or "admin"
    if not re.fullmatch(r"[A-Za-z0-9._-]{3,64}", username):
        raise SetupError("login must contain 3-64 Latin letters, digits, dots, dashes or underscores")
    password = getpass.getpass("New administrator password (at least 14 characters): ")
    if len(password) < 14 or len(password) > 128 or "\n" in password or "\r" in password:
        raise SetupError("password must contain 14-128 characters")
    if not hmac.compare_digest(password, getpass.getpass("Repeat password: ")):
        raise SetupError("passwords do not match")
    salt = secrets.token_bytes(16)
    digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600000)
    password_hash = "pbkdf2_sha256:600000:" + salt.hex() + ":" + digest.hex()
    secret = secrets.token_bytes(20)
    totp_secret = base64.b32encode(secret).decode().rstrip("=")
    uri = "otpauth://totp/Private%20Gateway:" + quote(username, safe="") + "?secret=" + totp_secret + "&issuer=Private%20Gateway&algorithm=SHA1&digits=6&period=30"
    print("\nAdd this account to an authenticator application. Keep the secret private.")
    print("Account:", username)
    print("Manual setup secret:", totp_secret)
    print("Authenticator URI:", uri)
    code = input("Current six-digit code from the application: ").strip()
    if not valid_code(secret, code):
        raise SetupError("authentication code did not match; no settings changed")
    new = changed_env(old, username, password_hash, totp_secret)
    command = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
    execute(command + ["config", "--quiet"])
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="admin-credentials-", dir=backups))
    os.chmod(backup, 0o700)
    shutil.copyfile(env, backup / "stack.env")
    os.chmod(backup / "stack.env", 0o600)
    atomic_private_file(env, new)
    try:
        execute(command + ["config", "--quiet"])
        execute(command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"])
        check(domain, username, password, code_at_step(secret, int(time.time() // 30)))
    except BaseException as error:
        atomic_private_file(env, old)
        try:
            execute(command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"])
        except BaseException as rollback_error:
            raise SetupError(f"setup and rollback failed; inspect backup {backup}") from rollback_error
        raise SetupError(f"setup failed; previous access restored; backup {backup}") from error
    return f"Administrator login, password and TOTP enabled. Backup: {backup}. Wait for the next authenticator code before logging in."


def main():
    if os.geteuid() != 0:
        raise SetupError("run with sudo")
    if len(sys.argv) != 1:
        raise SetupError("arguments are not accepted")
    with LOCK.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise SetupError("another deployment or credential change is running") from error
        print(setup())


if __name__ == "__main__":
    try:
        main()
    except (SetupError, OSError, UnicodeError, ValueError, KeyError) as error:
        print("Administrator setup stopped:", error, file=sys.stderr)
        raise SystemExit(1) from None
