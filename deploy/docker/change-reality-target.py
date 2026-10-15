#!/usr/bin/env python3
"""Switch the installed REALITY target after an isolated VLESS check.

This one-time repair preserves the database, devices and REALITY keys. It
recreates only the API container so its node metadata follows the new SNI.
"""

import fcntl
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile


TARGET = "www.cloudflare.com"
ENV = Path("/etc/private-gateway-docker/stack.env")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
SMOKE = Path("/opt/private-gateway-docker/current/docker/vpn-smoke.py")
XRAY_CONFIG = Path("/var/lib/private-gateway-docker/xray/xray.json")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")


class MigrationError(Exception):
    pass


def replacement(content):
    lines = content.decode("utf-8").splitlines(keepends=True)
    changes = {"GATEWAY_SERVER_NAME": TARGET,
               "GATEWAY_REALITY_TARGET": TARGET + ":443"}
    for key, value in changes.items():
        positions = [i for i, line in enumerate(lines) if line.startswith(key + "=")]
        if len(positions) != 1:
            raise MigrationError("stack.env must contain exactly one " + key)
        lines[positions[0]] = key + "=" + value + "\n"
    return "".join(lines).encode("utf-8")


def atomic_env(path, content):
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".stack.", delete=False) as output:
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


def run(command, timeout=120):
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise MigrationError("command could not complete: " + command[0]) from error
    if result.returncode:
        # Command output may contain credentials, so it is deliberately hidden.
        raise MigrationError("command failed: " + " ".join(command[-3:]))
    return result.stdout


def require_private_file(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise MigrationError("stack.env must be a root-owned regular private file")


def migrate(env=ENV, compose=COMPOSE, smoke=SMOKE, backups=BACKUPS, xray_config=XRAY_CONFIG,
            execute=run):
    if not compose.is_file() or not smoke.is_file():
        raise MigrationError("installed Docker release is incomplete")
    before = env.read_bytes()
    after = replacement(before)
    compose_command = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
    api_up = compose_command + ["up", "-d", "--no-deps", "--force-recreate", "--wait", "api"]
    apply = compose_command + ["exec", "-T", "api", "/usr/local/bin/gatewayctl", "config", "apply"]
    smoke_command = [sys.executable, str(smoke)]

    if before == after:
        result = execute(smoke_command, timeout=75)
        if "VLESS traffic OK:" not in result:
            raise MigrationError("installed VLESS check did not pass")
        return "already configured; " + result.strip()

    isolated = execute(smoke_command + ["--isolated", "--isolated-target", TARGET], timeout=75)
    if "VLESS traffic OK:" not in isolated:
        raise MigrationError("temporary Cloudflare VLESS check did not pass")
    execute(compose_command + ["config", "--quiet"], timeout=20)

    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="reality-target-", dir=backups))
    os.chmod(backup, 0o700)
    shutil.copyfile(env, backup / "stack.env")
    os.chmod(backup / "stack.env", 0o600)
    if xray_config.is_file():
        shutil.copyfile(xray_config, backup / "xray.json")
        os.chmod(backup / "xray.json", 0o600)

    atomic_env(env, after)
    try:
        execute(compose_command + ["config", "--quiet"], timeout=20)
        execute(api_up, timeout=120)
        execute(apply, timeout=90)
        result = execute(smoke_command, timeout=75)
        if "VLESS traffic OK:" not in result:
            raise MigrationError("installed VLESS check did not pass")
    except BaseException as error:
        atomic_env(env, before)
        try:
            execute(api_up, timeout=120)
            execute(apply, timeout=90)
        except BaseException as rollback_error:
            raise MigrationError(f"repair and rollback failed; inspect backup {backup}") from rollback_error
        raise MigrationError(f"repair failed and previous settings were restored; backup {backup}") from error
    return f"{result.strip()}; target {TARGET}; backup {backup}"


def main():
    if os.geteuid() != 0:
        raise MigrationError("run this command with sudo")
    if len(sys.argv) != 1:
        raise MigrationError("this repair does not accept arguments")
    require_private_file(ENV)
    with LOCK.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise MigrationError("another Docker deployment is running") from error
        print(migrate())


if __name__ == "__main__":
    try:
        main()
    except (MigrationError, OSError, UnicodeError) as error:
        print("REALITY target change stopped:", error, file=sys.stderr)
        raise SystemExit(1) from None
