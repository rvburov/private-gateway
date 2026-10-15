#!/usr/bin/env python3
"""Rotate the panel token and browser password without changing VPN devices."""

import base64
import fcntl
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
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener


ENV = Path("/etc/private-gateway-docker/stack.env")
CADDYFILE = Path("/etc/private-gateway-docker/Caddyfile")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")
HASH = re.compile(r"\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}")


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


def changed_caddyfile(content, password_hash):
    text = content.decode("utf-8")
    pattern = r"(?m)^([ \t]*browser )\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$"
    if len(re.findall(pattern, text)) != 1:
        raise RotationError("Caddyfile must contain one browser password hash")
    return re.sub(pattern, lambda match: match[1] + password_hash, text).encode("utf-8")


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
        # Docker output could include environment values. Never copy it to logs.
        raise RotationError("Docker command failed")
    return result.stdout.strip()


def hash_password(token, execute=run):
    output = execute(["docker", "run", "--rm", "--env", "CADDY_PASSWORD",
                      "--entrypoint", "/bin/sh", "caddy:2.10.2-alpine", "-c",
                      'exec caddy hash-password --plaintext "$CADDY_PASSWORD"'],
                     env={**os.environ, "CADDY_PASSWORD": token}, timeout=30)
    if not HASH.fullmatch(output):
        raise RotationError("Caddy did not return a valid password hash")
    return output


def http_status(url, authorization):
    request = Request(url, headers={"Authorization": authorization})
    try:
        with build_opener(ProxyHandler({})).open(request, timeout=12) as response:
            return response.status
    except HTTPError as error:
        return error.code


def login_configured(domain):
    with build_opener(ProxyHandler({})).open(
            "https://" + domain + ":8443/api/v1/auth/config", timeout=12) as response:
        return json.load(response) == {"configured": True}


def verify_access(new, old, domain, configured=False):
    base = "https://" + domain + ":8443"
    basic_new = "Basic " + base64.b64encode(("browser:" + new).encode()).decode()
    basic_old = "Basic " + base64.b64encode(("browser:" + old).encode()).decode()
    api_new = http_status(base + "/api/v1/status", "Bearer " + new)
    api_old = http_status(base + "/api/v1/status", "Bearer " + old)
    browser_new = http_status(base + "/browser/vnc.html", basic_new)
    browser_old = http_status(base + "/browser/vnc.html", basic_old)
    # When the disposable browser is stopped, Caddy accepts its password but
    # returns 502 because noVNC is not listening. The old password must still
    # receive 401; starting Chromium just to rotate credentials is unnecessary.
    expected_api = (401, 401) if configured else (200, 401)
    if (api_new, api_old) != expected_api or browser_new not in (200, 502) or browser_old != 401:
        raise RotationError(f"HTTPS checks failed: API {api_new}/{api_old}, "
                            f"browser {browser_new}/{browser_old}")


def require_private(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise RotationError("configuration files must be root-owned and private")


def rotate(env=ENV, caddyfile=CADDYFILE, compose=COMPOSE, backups=BACKUPS,
           execute=run, verify=verify_access, detect=login_configured):
    if not compose.is_file():
        raise RotationError("installed Docker release is incomplete")
    old_env, old_caddyfile = env.read_bytes(), caddyfile.read_bytes()
    old_token = env_value(old_env, "GATEWAY_API_TOKEN")
    domain = env_value(old_env, "PANEL_DOMAIN")
    configured = detect(domain)
    if not re.fullmatch(r"[A-Za-z0-9.-]+", domain) or not re.fullmatch(r"[0-9a-f]{64}", old_token):
        raise RotationError("invalid installed token or panel domain")
    new_token = secrets.token_hex(32)
    if hmac.compare_digest(old_token, new_token):
        raise RotationError("new token unexpectedly equals old token")
    new_hash = hash_password(new_token, execute)
    new_env = changed_env(old_env, new_token)
    new_caddyfile = changed_caddyfile(old_caddyfile, new_hash)
    if new_env == old_env or new_caddyfile == old_caddyfile:
        raise RotationError("configuration was not updated")
    compose_command = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
    restart = compose_command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api", "caddy"]
    execute(compose_command + ["config", "--quiet"], timeout=20)

    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="admin-token-", dir=backups))
    os.chmod(backup, 0o700)
    for source, name in ((env, "stack.env"), (caddyfile, "Caddyfile")):
        shutil.copyfile(source, backup / name)
        os.chmod(backup / name, 0o600)

    atomic_private_file(env, new_env)
    try:
        atomic_private_file(caddyfile, new_caddyfile)
        execute(compose_command + ["config", "--quiet"], timeout=20)
        execute(restart, timeout=120)
        if configured:
            execute(compose_command + ["exec", "-T", "api", "/usr/local/bin/gatewayctl", "status"], timeout=20)
        verify(new_token, old_token, domain, configured)
    except BaseException as error:
        atomic_private_file(env, old_env)
        atomic_private_file(caddyfile, old_caddyfile)
        try:
            execute(restart, timeout=120)
        except BaseException as rollback_error:
            raise RotationError(f"rotation and rollback failed; inspect backup {backup}") from rollback_error
        reason = str(error) if isinstance(error, RotationError) else type(error).__name__
        raise RotationError(f"rotation failed ({reason}); previous access was restored; "
                            f"backup {backup}") from error
    return f"Admin token and browser password rotated. New token is in {env}; backup {backup}"


def main():
    if os.geteuid() != 0:
        raise RotationError("run this command with sudo")
    if len(sys.argv) != 1:
        raise RotationError("this rotation does not accept arguments")
    require_private(ENV)
    require_private(CADDYFILE)
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
