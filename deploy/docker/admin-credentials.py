#!/usr/bin/env python3
"""Set the administrator login/password in PostgreSQL; enroll 2FA in the browser."""

import fcntl
import getpass
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from urllib.request import ProxyHandler, Request, build_opener

ENV = Path("/etc/private-gateway-docker/stack.env")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")
OLD_NAMES = ("GATEWAY_ADMIN_USERNAME", "GATEWAY_ADMIN_PASSWORD_HASH", "GATEWAY_ADMIN_TOTP_SECRET")


class SetupError(Exception):
    pass


def run(command, stdin=None):
    try:
        result = subprocess.run(command, input=stdin, capture_output=True, timeout=120, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise SetupError("Docker command could not complete") from error
    if result.returncode:
        raise SetupError("Docker command failed; inspect container health")
    return result.stdout


def backup_database(command, destination):
    with destination.open("xb") as output:
        try:
            result = subprocess.run(command, stdout=output, stderr=subprocess.PIPE, timeout=120, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise SetupError("database backup could not complete") from error
    if result.returncode or destination.stat().st_size == 0:
        raise SetupError("database backup failed")
    os.chmod(destination, 0o600)


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


def without_legacy_admin(old):
    lines = [line for line in old.decode().splitlines()
             if not any(line.startswith(name + "=") for name in OLD_NAMES)]
    return ("\n".join(lines) + "\n").encode()


def verify(domain, username, password):
    opener = build_opener(ProxyHandler({}))
    base = "https://" + domain + "/api/v1"
    request = Request(base + "/auth/login", data=json.dumps({
        "username": username, "password": password,
    }).encode(), headers={"Content-Type": "application/json"}, method="POST")
    with opener.open(request, timeout=12) as response:
        data = json.load(response)
    if data.get("status") != "totp_setup_required" or not data.get("pending"):
        raise SetupError("browser authenticator setup is not ready")


def setup(env=ENV, compose=COMPOSE, backups=BACKUPS, execute=run,
          check=verify, dump=backup_database):
    if not compose.is_file():
        raise SetupError("installed Docker release is incomplete")
    metadata = env.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise SetupError("stack.env must be root-owned and private")
    old = env.read_bytes()
    domains = [line.split("=", 1)[1] for line in old.decode().splitlines() if line.startswith("PANEL_DOMAIN=")]
    if len(domains) != 1 or not re.fullmatch(r"[A-Za-z0-9.-]+", domains[0]):
        raise SetupError("invalid panel domain")
    command = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
    api = command + ["exec", "-T", "api", "/usr/local/bin/gateway-api"]
    current = execute(api + ["admin-name"]).decode().strip()
    username = input(f"Administrator login [{current or 'admin'}]: ").strip() or current or "admin"
    simple_login = re.fullmatch(r"[A-Za-z0-9._-]{3,64}", username)
    email_login = len(username) <= 64 and re.fullmatch(r"[A-Za-z0-9._-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}", username)
    if not (simple_login or email_login):
        raise SetupError("login must be 3-64 Latin letters, digits, dots, dashes or underscores, or an email address")
    password = getpass.getpass("New administrator password (at least 14 characters): ")
    if len(password) < 14 or len(password) > 128 or "\n" in password or "\r" in password:
        raise SetupError("password must contain 14-128 characters")
    if not hmac.compare_digest(password, getpass.getpass("Repeat password: ")):
        raise SetupError("passwords do not match")
    salt = os.urandom(16)
    digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600000)
    password_hash = "pbkdf2_sha256:600000:" + salt.hex() + ":" + digest.hex()

    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="admin-credentials-", dir=backups))
    os.chmod(backup, 0o700)
    (backup / "stack.env").write_bytes(old)
    os.chmod(backup / "stack.env", 0o600)
    snapshot = execute(api + ["admin-backup"])
    if not isinstance(json.loads(snapshot), dict):
        raise SetupError("administrator backup is invalid")
    (backup / "administrator.json").write_bytes(snapshot)
    os.chmod(backup / "administrator.json", 0o600)
    dump(command + ["exec", "-T", "postgres", "pg_dump", "-Fc", "-U", "gateway", "gateway"], backup / "gateway.dump")
    execute(command + ["config", "--quiet"])
    attempted = False
    try:
        attempted = True
        execute(api + ["admin-set"], json.dumps({"username": username, "password_hash": password_hash}).encode())
        atomic_private_file(env, without_legacy_admin(old))
        execute(command + ["config", "--quiet"])
        execute(command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"])
        check(domains[0], username, password)
    except BaseException as error:
        atomic_private_file(env, old)
        if attempted:
            try:
                one_off = command + ["run", "--rm", "--no-deps", "-T", "api", "/usr/local/bin/gateway-api"]
                current = execute(one_off + ["admin-backup"])
                if json.loads(current) != json.loads(snapshot):
                    execute(one_off + ["admin-restore"], snapshot)
                execute(command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"])
            except BaseException as rollback_error:
                raise SetupError(f"setup and rollback failed; inspect backup {backup}") from rollback_error
        raise SetupError(f"setup failed; previous access restored; backup {backup}") from error
    return f"Administrator login and password stored in PostgreSQL. Backup: {backup}. Open the panel to enroll a new authenticator."


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
