#!/usr/bin/env python3
"""Проверить и установить фиксированные TCP-маршруты отдельного VPS-моста."""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import tempfile

IMAGE = "haproxy:3.2-alpine@sha256:254e6370f01cd654e0744edf607c67f33777c1b6a9dc0d4b28bcba7171460122"
PROJECT = "private-gateway-bridge"
STATE = Path("/etc/private-gateway-bridge")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate JSON key")
        result[key] = value
    return result


def port(value):
    if type(value) is not int or not 1 <= value <= 65535:
        raise ValueError("Invalid TCP port")
    return value


def hostname(value):
    if not isinstance(value, str) or not value or len(value) > 253:
        raise ValueError("Invalid target host")
    try:
        address = ipaddress.ip_address(value)
        if address.is_unspecified or address.is_multicast or address.is_link_local or "%" in value:
            raise ValueError("Invalid target address")
        return str(address)
    except ValueError:
        if not re.fullmatch(r"[A-Za-z0-9.-]+", value):
            raise ValueError("Invalid target host") from None
        value = value.rstrip(".").lower()
        if not value or any(not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", part)
                            for part in value.split(".")):
            raise ValueError("Invalid target host")
        # Некорректный IPv4 нельзя повторно трактовать как домен.
        if re.fullmatch(r"[0-9.]+", value):
            raise ValueError("Invalid target address")
        return value


def validate(config):
    if (not isinstance(config, dict) or set(config) != {"version", "listeners"}
            or type(config["version"]) is not int or config["version"] != 1
            or not isinstance(config["listeners"], list) or not 1 <= len(config["listeners"]) <= 64):
        raise ValueError("Expected bridge configuration version 1 with 1 to 64 listeners")
    names, ports, listeners = set(), set(), []
    for row in config["listeners"]:
        if (not isinstance(row, dict) or set(row) != {
                "name", "transport", "listen_port", "target_host", "target_port"}
                or not isinstance(row["name"], str)
                or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", row["name"])
                or row["transport"] not in ("tcp", "xhttp")):
            raise ValueError("Invalid bridge listener")
        item = dict(row, listen_port=port(row["listen_port"]),
                    target_host=hostname(row["target_host"]), target_port=port(row["target_port"]))
        if item["name"] in names or item["listen_port"] in ports:
            raise ValueError("Duplicate listener name or port")
        if item["listen_port"] == 22:
            raise ValueError("Port 22 is reserved for SSH; use a different bridge port")
        names.add(item["name"])
        ports.add(item["listen_port"])
        listeners.append(item)
    return {"version": 1, "listeners": sorted(listeners, key=lambda row: row["name"])}


def read_config(path):
    path = Path(path)
    if not path.is_file() or path.is_symlink() or path.stat().st_size > 65536:
        raise ValueError("Configuration must be a regular file of at most 64 KiB")
    return validate(json.loads(path.read_text(), object_pairs_hook=unique_object))


def render(config, transport):
    rows = [row for row in config["listeners"] if row["transport"] == transport]
    if not rows:
        return None
    text = """# Зашифрованный поток пересылается без завершения TLS и без PROXY protocol.
global
    maxconn 4096
    uid 65534
    gid 65534

defaults
    mode tcp
    timeout connect 5s
    timeout client 1h
    timeout server 1h
    timeout client-fin 5s
    timeout server-fin 5s

"""
    uses_dns = any(not is_ip(row["target_host"]) for row in rows)
    if uses_dns:
        text += """
resolvers bridge_dns
    parse-resolv-conf
    resolve_retries 3
    timeout resolve 2s
    timeout retry 1s
    hold valid 30s
"""
    for row in rows:
        host = row["target_host"]
        endpoint = f'[{host}]:{row["target_port"]}' if ":" in host else f'{host}:{row["target_port"]}'
        dns = " resolvers bridge_dns init-addr libc,none" if not is_ip(host) else ""
        text += (f'\nlisten {row["name"]}\n'
                 f'    bind :::{row["listen_port"]} v4v6\n'
                 f'    server exit {endpoint} check inter 10s fall 3 rise 2{dns}\n')
    return text


def is_ip(host):
    try:
        ipaddress.ip_address(host)
        return True
    except ValueError:
        return False


def run(arguments, **kwargs):
    return subprocess.run(arguments, check=True, text=True, **kwargs)


def compose_document(config, config_paths):
    services = {}
    for transport in ("tcp", "xhttp"):
        if transport not in config_paths:
            continue
        services[transport] = {
            # Master открывает низкие порты; рабочий HAProxy сбрасывает UID/GID в конфигурации.
            "image": IMAGE, "network_mode": "host", "user": "0:0",
            "command": ["haproxy", "-W", "-db", "-f", "/usr/local/etc/haproxy/haproxy.cfg"],
            "volumes": [{"type": "bind", "source": str(config_paths[transport]),
                         "target": "/usr/local/etc/haproxy/haproxy.cfg", "read_only": True}],
            "read_only": True, "cap_drop": ["ALL"], "cap_add": ["NET_BIND_SERVICE", "SETUID", "SETGID"],
            "security_opt": ["no-new-privileges:true"], "restart": "unless-stopped",
            "healthcheck": {"test": ["CMD", "haproxy", "-c", "-f", "/usr/local/etc/haproxy/haproxy.cfg"],
                            "interval": "10s", "timeout": "5s", "retries": 3},
            "logging": {"driver": "json-file", "options": {"max-size": "5m", "max-file": "2"}},
        }
    return {"name": PROJECT, "services": services}


def compose(path, *arguments):
    run(["docker", "compose", "-p", PROJECT, "-f", str(path), *arguments])


def local_addresses():
    interfaces = json.loads(run(["ip", "-json", "address", "show"], capture_output=True).stdout)
    return {item["local"] for interface in interfaces for item in interface.get("addr_info", [])}


def preflight(config, old_config=None):
    local = local_addresses()
    previous_ports = {row["listen_port"] for row in (old_config or {}).get("listeners", [])}
    for row in config["listeners"]:
        addresses = socket.getaddrinfo(row["target_host"], row["target_port"], type=socket.SOCK_STREAM)
        if any(address[4][0] in local or ipaddress.ip_address(address[4][0]).is_loopback
               for address in addresses):
            raise ValueError("Exit resolves to this machine; install the bridge on a separate VPS")
        with socket.create_connection((row["target_host"], row["target_port"]), timeout=5):
            pass
        if row["listen_port"] not in previous_ports:
            with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as probe:
                probe.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
                probe.bind(("::", row["listen_port"]))
        print(f'TCP path to exit OK: {row["name"]}; this does not verify VLESS authentication.')


def verify_haproxy(path):
    run(["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
         "--security-opt", "no-new-privileges:true", "--user", "65534:65534",
         "--mount", f"type=bind,src={path},dst=/bridge.cfg,readonly",
         "--entrypoint", "haproxy", IMAGE, "-c", "-f", "/bridge.cfg"])


def atomic_write(path, content):
    descriptor, name = tempfile.mkstemp(prefix=".new-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w") as destination:
            destination.write(content)
            destination.flush()
            os.fsync(destination.fileno())
        os.chmod(name, 0o644)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def installed(path):
    if not path.exists():
        return None
    if path.is_symlink() or not path.is_file():
        raise ValueError("Installed state must be a regular file")
    data = json.loads(path.read_text())
    validate(data["config"])
    return data


def deploy(config, *, check=False, state=STATE):
    run(["docker", "info"], stdout=subprocess.DEVNULL)
    run(["docker", "compose", "version"], stdout=subprocess.DEVNULL)
    for project in ("private-gateway", "private-gateway-node"):
        if run(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={project}"],
               capture_output=True).stdout.strip():
            raise ValueError("This VPS contains an exit/panel installation; use a separate bridge VPS")
    old = installed(state / "installed.json")
    if not old and run(["docker", "ps", "-aq", "--filter",
                        f"label=com.docker.compose.project={PROJECT}"], capture_output=True).stdout.strip():
        raise ValueError("Unmanaged bridge containers already exist; inspect them before installation")
    preflight(config, old["config"] if old else None)
    run(["docker", "pull", IMAGE])
    with tempfile.TemporaryDirectory(prefix="private-gateway-bridge-") as temp:
        temporary = Path(temp)
        os.chmod(temporary, 0o755)
        rendered = {}
        for transport in ("tcp", "xhttp"):
            content = render(config, transport)
            if content:
                path = temporary / (transport + ".cfg")
                path.write_text(content)
                path.chmod(0o644)
                verify_haproxy(path)
                rendered[transport] = content
        if check:
            print("Bridge configuration and outbound TCP paths checked; no bridge was started.")
            return
    state.mkdir(mode=0o755, parents=True, exist_ok=True)
    configs = state / "configs"
    configs.mkdir(mode=0o755, exist_ok=True)
    paths = {}
    for transport, content in rendered.items():
        digest = hashlib.sha256(content.encode()).hexdigest()
        path = configs / f"{transport}-{digest}.cfg"
        if path.is_symlink():
            raise ValueError("Unsafe existing configuration path")
        if not path.exists():
            atomic_write(path, content)
        elif path.read_text() != content:
            raise ValueError("Stored bridge configuration was modified")
        paths[transport] = path
    document = compose_document(config, paths)
    revision = hashlib.sha256(json.dumps(document, sort_keys=True).encode()).hexdigest()
    compose_path = state / f"compose-{revision}.json"
    atomic_write(compose_path, json.dumps(document, indent=2) + "\n")
    compose(compose_path, "config", "--quiet")
    try:
        compose(compose_path, "up", "-d", "--wait", "--wait-timeout", "45", "--remove-orphans")
        # Это проверка слушателей и процессов; доказательство работы VLESS выполняется клиентом.
        for row in config["listeners"]:
            with socket.create_connection(("127.0.0.1", row["listen_port"]), timeout=3):
                pass
        if old and old["config"] != config:
            atomic_write(state / "previous.json", json.dumps(old, indent=2) + "\n")
        current = {"config": config, "compose": str(compose_path)}
        atomic_write(state / "compose.json", json.dumps(document, indent=2) + "\n")
        atomic_write(state / "installed.json", json.dumps(current, indent=2) + "\n")
    except BaseException:
        print("Bridge deployment failed; restoring the previous deployment.", file=sys.stderr)
        try:
            if old:
                compose(Path(old["compose"]), "up", "-d", "--wait", "--wait-timeout", "45", "--remove-orphans")
                atomic_write(state / "installed.json", json.dumps(old, indent=2) + "\n")
                atomic_write(state / "compose.json", Path(old["compose"]).read_text())
            else:
                compose(compose_path, "down")
        except BaseException:
            print("Automatic bridge rollback failed. Inspect the retained files and containers.", file=sys.stderr)
        raise
    print("Bridge listeners are running. Import a normal profile and verify the exit IP on your device.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("config", type=Path)
    parser.add_argument("--check", action="store_true", help="Проверить конфигурацию и пути без запуска моста")
    parser.add_argument("--validate", action="store_true", help="Проверить только JSON, без сети и Docker")
    args = parser.parse_args()
    config = read_config(args.config)
    if args.validate:
        print("Bridge JSON configuration is valid.")
        return
    if os.geteuid() != 0:
        raise ValueError("Use sudo on the separate bridge VPS")
    if STATE.is_symlink() or (STATE.exists() and (not STATE.is_dir() or STATE.stat().st_uid != 0
                                                or STATE.stat().st_mode & 0o022)):
        raise ValueError("Bridge state must be a root-owned directory without group/other write access")
    import fcntl
    with open("/run/lock/private-gateway-bridge.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        deploy(config, check=args.check)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"Bridge installation stopped: {error}", file=sys.stderr)
        sys.exit(1)
