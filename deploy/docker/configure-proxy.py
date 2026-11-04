#!/usr/bin/env python3
"""Подготовить параметры SOCKS5 в закрытом файле без вывода учётных данных.

Скрипт пишет отдельный proxy.pending.env для следующего выпуска. Работающий
stack.env и конфигурацию Xray он не меняет. Логин и пароль читаются скрыто.
"""

import argparse
import base64
import fcntl
import getpass
import ipaddress
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import tempfile
import warnings


ENV = Path("/etc/private-gateway-docker/stack.env")
PENDING = Path("/etc/private-gateway-docker/proxy.pending.env")
BACKUPS = Path("/var/backups/private-gateway")
LOCK = Path("/run/lock/private-gateway-docker-install.lock")
NAMES = ("GATEWAY_PROXY_ENABLED", "GATEWAY_PROXY_HOST", "GATEWAY_PROXY_PORT",
         "GATEWAY_PROXY_AUTH", "GATEWAY_PROXY_USERNAME_B64", "GATEWAY_PROXY_PASSWORD_B64",
         "GATEWAY_PROXY_EXPECTED_IP")


class ConfigurationError(Exception):
    pass


def valid_host(host):
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        pass
    if not host or len(host) > 253 or host.endswith("."):
        return False
    return all(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", label)
               for label in host.split("."))


def validate(host, port, username, password, *, no_auth=False, expected_ip=""):
    if not valid_host(host) or type(port) is not int or not 1 <= port <= 65535:
        raise ConfigurationError("SOCKS5 address or port is invalid")
    if expected_ip:
        try:
            ipaddress.ip_address(expected_ip)
        except ValueError as error:
            raise ConfigurationError("expected external IP is invalid") from error
    if no_auth:
        if username or password:
            raise ConfigurationError("no-auth SOCKS5 must not contain credentials")
        return
    for value in (username, password):
        encoded = value.encode("utf-8")
        if not 1 <= len(encoded) <= 255 or any(byte < 32 or byte == 127 for byte in encoded):
            raise ConfigurationError("SOCKS5 credentials must be 1 to 255 UTF-8 bytes without control characters")


def require_private_file(path):
    metadata = path.lstat()
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != 0 or stat.S_IMODE(metadata.st_mode) != 0o600:
        raise ConfigurationError("stack.env must be a root-owned regular file with mode 0600")
    return metadata


def replacement(content, host, port, username, password, *, no_auth=False, expected_ip=""):
    values = {
        "GATEWAY_PROXY_ENABLED": "1",
        "GATEWAY_PROXY_HOST": host,
        "GATEWAY_PROXY_PORT": str(port),
        "GATEWAY_PROXY_AUTH": "none" if no_auth else "password",
        # Base64 защищает только синтаксис dotenv и символы $, кавычки и обратный слеш.
        # Файл по-прежнему содержит секрет и обязан быть доступен только root.
        "GATEWAY_PROXY_USERNAME_B64": base64.b64encode(username.encode("utf-8")).decode("ascii"),
        "GATEWAY_PROXY_PASSWORD_B64": base64.b64encode(password.encode("utf-8")).decode("ascii"),
        "GATEWAY_PROXY_EXPECTED_IP": expected_ip,
    }
    lines = content.decode("utf-8").splitlines()
    for name, value in values.items():
        indexes = [index for index, line in enumerate(lines) if line.startswith(name + "=")]
        if len(indexes) > 1:
            raise ConfigurationError("stack.env contains duplicate proxy settings")
        if indexes:
            lines[indexes[0]] = name + "=" + value
        else:
            lines.append(name + "=" + value)
    return ("\n".join(lines) + "\n").encode("utf-8")


def atomic_write(path, data, metadata):
    staged = None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".proxy.", delete=False) as output:
            staged = Path(output.name)
            os.chmod(staged, 0o600)
            os.chown(staged, metadata.st_uid, metadata.st_gid)
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(staged, path)
        staged = None
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if staged is not None:
            staged.unlink(missing_ok=True)


def configure(host, port, username, password, *, no_auth=False, expected_ip="", env=ENV,
              pending=PENDING, backups=BACKUPS):
    validate(host, port, username, password, no_auth=no_auth, expected_ip=expected_ip)
    metadata = require_private_file(env)
    try:
        require_private_file(pending)
        before = pending.read_bytes()
    except FileNotFoundError:
        before = b""
    after = replacement(b"", host, port, username, password, no_auth=no_auth, expected_ip=expected_ip)
    if before == after:
        return "SOCKS5 settings already staged; no file changed"
    backups.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix="proxy-", dir=backups))
    os.chmod(backup, 0o700)
    backup_env = backup / "stack.env"
    backup_env.touch(mode=0o600, exist_ok=False)
    os.chmod(backup_env, 0o600)
    shutil.copyfile(env, backup_env)
    if before:
        backup_pending = backup / "proxy.pending.env"
        backup_pending.touch(mode=0o600, exist_ok=False)
        os.chmod(backup_pending, 0o600)
        backup_pending.write_bytes(before)
    atomic_write(pending, after, metadata)
    return f"SOCKS5 settings staged for the next release; backup {backup}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True, help="Адрес подключения из параметров SOCKS5")
    parser.add_argument("--port", required=True, type=int, help="Порт из параметров SOCKS5")
    parser.add_argument("--no-auth", action="store_true", help="Прокси разрешает подключение по IP без пароля")
    parser.add_argument("--expected-ip", default="", help="Ожидаемый внешний IP для проверки после выкладки")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise ConfigurationError("run this command with sudo")
    if not sys.stdin.isatty() or not sys.stderr.isatty():
        raise ConfigurationError("a private interactive terminal is required")
    if not valid_host(args.host) or not 1 <= args.port <= 65535:
        raise ConfigurationError("SOCKS5 address or port is invalid")
    require_private_file(ENV)
    try:
        require_private_file(PENDING)
    except FileNotFoundError:
        pass
    with LOCK.open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ConfigurationError("another Docker deployment is running") from error
        username = password = ""
        if not args.no_auth:
            with warnings.catch_warnings():
                warnings.simplefilter("error", getpass.GetPassWarning)
                username = getpass.getpass("Логин SOCKS5: ")
                password = getpass.getpass("Пароль SOCKS5: ")
        print(configure(args.host, args.port, username, password,
                        no_auth=args.no_auth, expected_ip=args.expected_ip))


if __name__ == "__main__":
    try:
        main()
    except (ConfigurationError, OSError, UnicodeError, EOFError, KeyboardInterrupt, getpass.GetPassWarning):
        print("SOCKS5 settings were not prepared; inspect the terminal and retry", file=sys.stderr)
        raise SystemExit(1) from None
