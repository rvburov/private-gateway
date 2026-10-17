#!/usr/bin/env python3
"""Сменить внутренний токен CLI, не меняя учётные данные администратора."""

import fcntl
import hmac
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener


ENV = Path("/etc/private-gateway-docker/stack.env")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")


class RotationError(Exception):
    pass


def env_value(content, name):
    found = [line.split("=", 1)[1] for line in content.decode("utf-8").splitlines()
             if line.startswith(name + "=")]
    if len(found) != 1:
        raise RotationError("stack.env must contain exactly one " + name)
    return found[0]


def changed_env(content, token):
    old = env_value(content, "GATEWAY_API_TOKEN")
    return content.replace(("GATEWAY_API_TOKEN=" + old + "\n").encode(),
                           ("GATEWAY_API_TOKEN=" + token + "\n").encode(), 1)


def atomic_private_file(path, content):
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".rotate.", delete=False) as output:
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


def run(command, *, env=None, timeout=120):
    try:
        result = subprocess.run(command, env=env, capture_output=True, text=True,
                                timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise RotationError("Docker command could not complete") from error
    if result.returncode:
        # В выводе Docker могут оказаться значения окружения. Никогда не копировать его в журналы.
        raise RotationError("Docker command failed")
    return result.stdout.strip()


def http_status(url, authorization):
    request = Request(url, headers={"Authorization": authorization})
    try:
        with build_opener(ProxyHandler({})).open(request, timeout=12) as response:
            return response.status
    except HTTPError as error:
        return error.code


def verify_access(new, old, domain):
    base = "https://" + domain
    api_new = http_status(base + "/api/v1/status", "Bearer " + new)
    api_old = http_status(base + "/api/v1/status", "Bearer " + old)
    browser = http_status(base + "/browser/vnc.html", "")
    if (api_new, api_old) != (401, 401) or browser != 403:
        raise RotationError(f"HTTPS checks failed: API {api_new}/{api_old}, browser {browser}")


def require_private(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise RotationError("configuration files must be root-owned and private")


def rotate(env=ENV, compose=COMPOSE, backups=BACKUPS,
           execute=run, verify=verify_access):
    if not compose.is_file():
        raise RotationError("installed Docker release is incomplete")
    old_env = env.read_bytes()
    old_token = env_value(old_env, "GATEWAY_API_TOKEN")
    domain = env_value(old_env, "PANEL_DOMAIN")
    if not re.fullmatch(r"[A-Za-z0-9.-]+", domain) or not re.fullmatch(r"[0-9a-f]{64}", old_token):
        raise RotationError("invalid installed token or panel domain")
    new_token = secrets.token_hex(32)
    if hmac.compare_digest(old_token, new_token):
        raise RotationError("new token unexpectedly equals old token")
    new_env = changed_env(old_env, new_token)
    if new_env == old_env:
        raise RotationError("configuration was not updated")
    compose_command = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
    restart = compose_command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"]
    execute(compose_command + ["config", "--quiet"], timeout=20)

    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="admin-token-", dir=backups))
    os.chmod(backup, 0o700)
    (backup / "stack.env").write_bytes(old_env)
    os.chmod(backup / "stack.env", 0o600)

    atomic_private_file(env, new_env)
    try:
        execute(compose_command + ["config", "--quiet"], timeout=20)
        execute(restart, timeout=120)
        execute(compose_command + ["exec", "-T", "api", "/usr/local/bin/gatewayctl", "status"], timeout=20)
        verify(new_token, old_token, domain)
    except BaseException as error:
        atomic_private_file(env, old_env)
        try:
            execute(restart, timeout=120)
        except BaseException as rollback_error:
            raise RotationError(f"rotation and rollback failed; inspect backup {backup}") from rollback_error
        reason = str(error) if isinstance(error, RotationError) else type(error).__name__
        raise RotationError(f"rotation failed ({reason}); previous access was restored; "
                            f"backup {backup}") from error
    return f"Internal CLI token rotated. New token is in {env}; backup {backup}"


def main():
    if os.geteuid() != 0:
        raise RotationError("run this command with sudo")
    if len(sys.argv) != 1:
        raise RotationError("this rotation does not accept arguments")
    require_private(ENV)
    with LOCK.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise RotationError("another Docker deployment is running") from error
        print(rotate())


if __name__ == "__main__":
    try:
        main()
    except (RotationError, OSError, UnicodeError) as error:
        print("Admin token rotation stopped:", error, file=sys.stderr)
        raise SystemExit(1) from None
