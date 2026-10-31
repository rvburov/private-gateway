#!/usr/bin/env python3
"""Подготовить дополнительный XHTTP-вход и при запросе применить его с откатом.

Ключи создаются на сервере и сохраняются только в закрытом stack.env. Скрипт не
печатает значения профиля, ключи или случайный путь; прежний TCP/Vision сохраняется.
"""

import argparse
import base64
import fcntl
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


ENV = Path("/etc/private-gateway-docker/stack.env")
COMPOSE = Path("/opt/private-gateway-docker/current/docker/compose.yml")
BACKUPS = Path("/var/backups/private-gateway")
XRAY_CONFIG = Path("/var/lib/private-gateway-docker/xray/xray.json")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")
NAMES = ("GATEWAY_XHTTP_ENABLED", "GATEWAY_XHTTP_PORT", "GATEWAY_XHTTP_PRIVATE_KEY",
         "GATEWAY_XHTTP_SHORT_ID", "GATEWAY_XHTTP_PATH")
STAGES = frozenset(("preflight", "keygen", "compose-validation", "compose-start", "config-apply",
                    "tcp-smoke", "xhttp-smoke", "rollback"))


def safe_stage(value, fallback="preflight"):
    return value if isinstance(value, str) and value in STAGES else fallback


class ConfigurationError(Exception):
    def __init__(self, message, *, stage="preflight", restored=False, rollback_stage=None):
        super().__init__(message)
        self.stage = safe_stage(stage)
        self.restored = restored is True
        self.rollback_stage = None if rollback_stage is None else safe_stage(rollback_stage, "rollback")


def report_error(error, *, github_actions=None):
    # В аннотацию попадают только фиксированные категории, даже если error или
    # его поля пришли от подставленного исполнителя команды и содержат секрет.
    stage = safe_stage(getattr(error, "stage", None))
    rollback_stage = getattr(error, "rollback_stage", None)
    state = ""
    if rollback_stage is not None:
        state = "; rollback=failed; rollback_stage=" + safe_stage(rollback_stage, "rollback")
    elif getattr(error, "restored", False) is True:
        state = "; rollback=restored"
    if github_actions is None:
        github_actions = os.environ.get("GITHUB_ACTIONS") == "true"
    if github_actions:
        print("::error file=deploy/docker/configure-xhttp.py::XHTTP configuration failed; stage=" + stage + state,
              file=sys.stderr)
    else:
        message = str(error) if isinstance(error, ConfigurationError) else "file or operating system operation failed"
        print(f"XHTTP configuration stopped: stage={stage}{state}; {message}", file=sys.stderr)


def run(command, timeout=120):
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ConfigurationError("configuration command could not complete") from error
    if result.returncode:
        # Сообщения дочерних команд могут включать конфигурацию; наружу идёт только категория.
        raise ConfigurationError("configuration command failed; command output suppressed")
    return result.stdout


def require_private_file(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or metadata.st_mode & 0o077:
        raise ConfigurationError("stack.env must be a root-owned regular private file")


def parse_env(content):
    values = {}
    for line in content.decode("utf-8").splitlines():
        if not line or line.lstrip().startswith("#") or "=" not in line:
            continue
        name, value = line.split("=", 1)
        if name in values:
            raise ConfigurationError("stack.env contains duplicate variable names")
        values[name] = value
    return values


def valid_private_key(value):
    if not isinstance(value, str):
        return False
    try:
        return (bool(re.fullmatch(r"[A-Za-z0-9_-]{43}", value)) and
                len(base64.b64decode(value + "=", altchars=b"-_", validate=True)) == 32)
    except ValueError:
        return False


def replacement(content, enabled, port, generate_key):
    values = parse_env(content)
    if port is None:
        port = values.get("GATEWAY_XHTTP_PORT", "8444")
    if isinstance(port, bool) or not re.fullmatch(r"[0-9]{1,5}", str(port)):
        raise ConfigurationError("XHTTP port is invalid")
    port = int(port)
    reserved = {80, 443, 8443, 10085}
    for name in ("GATEWAY_PORT", "GATEWAY_XRAY_PORT", "GATEWAY_XRAY_STATS_PORT"):
        if name in values:
            try:
                reserved.add(int(values[name]))
            except ValueError:
                raise ConfigurationError("existing service port is invalid") from None
    if not 1 <= port <= 65535 or port in reserved:
        raise ConfigurationError("XHTTP port conflicts with an existing service or is invalid")
    changes = {"GATEWAY_XHTTP_ENABLED": "1" if enabled else "0", "GATEWAY_XHTTP_PORT": str(port)}
    if enabled:
        key = values.get("GATEWAY_XHTTP_PRIVATE_KEY") or generate_key()
        sid = values.get("GATEWAY_XHTTP_SHORT_ID") or secrets.token_hex(8)
        path = values.get("GATEWAY_XHTTP_PATH") or "/" + secrets.token_urlsafe(24)
        if not valid_private_key(key) or key == values.get("GATEWAY_REALITY_PRIVATE_KEY"):
            raise ConfigurationError("XHTTP requires a valid separate private key")
        if not re.fullmatch(r"[0-9a-f]{16}", sid) or sid == values.get("GATEWAY_SHORT_ID"):
            raise ConfigurationError("XHTTP requires a separate 8-byte short ID")
        if not re.fullmatch(r"/[A-Za-z0-9_-]{16,128}", path):
            raise ConfigurationError("XHTTP path must have at least 16 random URL-safe characters")
        changes.update(GATEWAY_XHTTP_PRIVATE_KEY=key, GATEWAY_XHTTP_SHORT_ID=sid,
                       GATEWAY_XHTTP_PATH=path)
    lines = content.decode("utf-8").splitlines()
    for name, value in changes.items():
        matches = [index for index, line in enumerate(lines) if line.startswith(name + "=")]
        if matches:
            lines[matches[0]] = name + "=" + value
        else:
            lines.append(name + "=" + value)
    return ("\n".join(lines) + "\n").encode("utf-8")


def atomic_write(path, content, metadata=None):
    staged = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".xhttp.", delete=False) as output:
            staged = Path(output.name)
            os.chmod(staged, stat.S_IMODE(metadata.st_mode) if metadata else 0o600)
            if metadata:
                os.chown(staged, metadata.st_uid, metadata.st_gid)
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(staged, path)
    finally:
        if staged is not None:
            staged.unlink(missing_ok=True)


def configure(*, enabled=True, port=None, apply=False, env=ENV, compose=COMPOSE,
              backups=BACKUPS, xray_config=None, execute=run):
    active_stage = "preflight"

    def execute_at(stage, arguments, *, timeout):
        nonlocal active_stage
        active_stage = safe_stage(stage)
        try:
            result = execute(arguments, timeout=timeout)
            if not isinstance(result, str):
                raise ConfigurationError("configuration command returned an invalid result", stage=active_stage)
            return result
        except Exception as error:
            # Исполнитель и дочерняя программа не определяют текст публичной ошибки.
            raise ConfigurationError("configuration command failed; command output suppressed",
                                     stage=active_stage) from error

    if not compose.is_file() or "GATEWAY_XHTTP_ENABLED" not in compose.read_text():
        raise ConfigurationError("install a verified release with XHTTP support first")
    smoke = compose.parent / "vpn-smoke.py"
    overlay = compose.parent / "compose.xhttp.yml"
    api = compose.parent.parent / "bin/gateway-api"
    if not smoke.is_file() or not api.is_file() or not overlay.is_file():
        raise ConfigurationError("installed Docker release is incomplete")
    before = env.read_bytes()
    metadata = env.stat()
    before_values = parse_env(before)
    if xray_config is None:
        state = Path(before_values.get("GATEWAY_STATE_DIR", str(XRAY_CONFIG.parent)))
        if not state.is_absolute():
            raise ConfigurationError("GATEWAY_STATE_DIR must be an absolute path")
        xray_config = state / "xray.json"
    def command():
        result = ["docker", "compose", "--env-file", str(env), "-f", str(compose)]
        if parse_env(env.read_bytes()).get("GATEWAY_XHTTP_ENABLED") == "1":
            result += ["-f", str(overlay)]
        return result

    if apply:
        status = execute_at("preflight", command() + ["exec", "-T", "api", "/usr/local/bin/gatewayctl", "status"], timeout=20)
        active = re.search(r"^ACTIVE\s+DEVICES\s+([0-9]+)\s*$", status, flags=re.M)
        if active is None or int(active[1]) == 0:
            raise ConfigurationError("an active device is required before applying XHTTP")

    def generate_key():
        try:
            generated = json.loads(execute_at("keygen", [str(api), "keygen"], timeout=15))
            key = generated["reality_private_key"]
            if not valid_private_key(key):
                raise ConfigurationError("private key could not be generated", stage="keygen")
            return key
        except (KeyError, TypeError, ValueError) as error:
            raise ConfigurationError("private key could not be generated", stage="keygen") from error

    after = replacement(before, enabled, port, generate_key)
    if before == after and not apply:
        return "XHTTP settings already prepared; secret values preserved"
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="xhttp-", dir=backups))
    os.chmod(backup, 0o700)
    shutil.copyfile(env, backup / "stack.env")
    os.chmod(backup / "stack.env", 0o600)
    xray_before = None
    xray_metadata = None
    if xray_config.exists():
        xray_metadata = xray_config.lstat()
        if not stat.S_ISREG(xray_metadata.st_mode):
            raise ConfigurationError("Xray configuration must be a regular file")
        xray_before = xray_config.read_bytes()
        (backup / "xray.json").write_bytes(xray_before)
        os.chmod(backup / "xray.json", 0o600)
    def start_and_apply():
        execute_at("compose-start", command() + ["up", "-d", "--wait", "--no-deps", "api", "xray"], timeout=120)
        execute_at("config-apply", command() + ["exec", "-T", "api", "/usr/local/bin/gatewayctl", "config", "apply"], timeout=90)

    def check_traffic(transport):
        stage = "xhttp-smoke" if transport == "xhttp" else "tcp-smoke"
        result = execute_at(stage, [sys.executable, str(smoke), "--transport", transport], timeout=75)
        if "VLESS traffic OK:" not in result:
            raise ConfigurationError("the transport traffic check did not pass", stage=stage)

    atomic_write(env, after, metadata)
    try:
        execute_at("compose-validation", command() + ["config", "--quiet"], timeout=20)
        if apply:
            start_and_apply()
            for transport in (["tcp", "xhttp"] if enabled else ["tcp"]):
                check_traffic(transport)
    except BaseException as error:
        failure_stage = safe_stage(getattr(error, "stage", active_stage))
        active_stage = "rollback"
        try:
            atomic_write(env, before, metadata)
            if xray_before is not None:
                atomic_write(xray_config, xray_before, xray_metadata)
            else:
                xray_config.unlink(missing_ok=True)
            if apply:
                start_and_apply()
                check_traffic("tcp")
                if parse_env(before).get("GATEWAY_XHTTP_ENABLED") == "1":
                    check_traffic("xhttp")
        except BaseException as rollback_error:
            rollback_stage = safe_stage(getattr(rollback_error, "stage", active_stage), "rollback")
            raise ConfigurationError(f"configuration and rollback failed; inspect backup {backup}",
                                     stage=failure_stage, rollback_stage=rollback_stage) from rollback_error
        raise ConfigurationError(f"configuration failed; previous settings restored; backup {backup}",
                                 stage=failure_stage, restored=True) from error
    action = "applied and checked" if apply else "prepared for the next release; use --apply to activate"
    return f"XHTTP settings {action}; backup {backup}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    state = parser.add_mutually_exclusive_group(required=True)
    state.add_argument("--enable", action="store_true")
    state.add_argument("--disable", action="store_true")
    parser.add_argument("--port", type=int)
    parser.add_argument("--apply", action="store_true", help="Применить настройки через Compose/API с проверкой обоих входов")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise ConfigurationError("run this command with sudo")
    require_private_file(ENV)
    with LOCK.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ConfigurationError("another Docker deployment is running") from error
        print(configure(enabled=args.enable, port=args.port, apply=args.apply))


if __name__ == "__main__":
    try:
        main()
    except (ConfigurationError, OSError, UnicodeError) as error:
        report_error(error)
        raise SystemExit(1) from None
