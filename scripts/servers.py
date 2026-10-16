#!/usr/bin/env python3
"""Servers that CI/CD deploys to: one public record per server in deploy/servers/NAME.conf.

  servers.py list         every server name as `names=[...]` for $GITHUB_OUTPUT
  servers.py show NAME    host, port, user, domain and the known_hosts line for $GITHUB_OUTPUT
  servers.py check NAME --host IPV4 --port PORT --domain DOMAIN
                          validate a new server before scripts/add-server.sh touches it
  servers.py add NAME --host IPV4 --port PORT --domain DOMAIN --host-key 'ssh-ed25519 KEY'
                          write the record of a prepared server

A record holds only public values: the SSH address, the restricted deploy account, the panel
domain and the server's SSH host key. The private deploy key is the DEPLOY_SSH_KEY secret of the
repository; deploy/servers/deploy-key.pub is its public half, which every server authorizes.
"""

import argparse
import ipaddress
import json
from pathlib import Path
import re
import sys

SERVERS = Path(__file__).resolve().parents[1] / "deploy" / "servers"
FIELDS = ("host", "port", "user", "domain", "host_key")
DEPLOY_USER = "gateway-deploy"
NAME = re.compile(r"[a-z0-9][a-z0-9-]{0,19}")
USER = re.compile(r"[a-z_][a-z0-9_-]{0,31}")
LABEL = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?")
# An ED25519 public key is 51 bytes, which base64 writes as exactly 68 characters.
HOST_KEY = re.compile(r"ssh-ed25519 [A-Za-z0-9+/]{68}")
PLACEHOLDER_KEY = "ssh-ed25519 " + "A" * 68


class RecordError(ValueError):
    pass


def validate(name, record):
    if not NAME.fullmatch(name):
        raise RecordError(f"{name}: a name is lowercase Latin letters, digits and dashes, at most 20 characters")
    missing = [field for field in FIELDS if field not in record]
    if missing:
        raise RecordError(f"{name}: missing {', '.join(missing)}")
    unknown = sorted(set(record) - set(FIELDS))
    if unknown:
        raise RecordError(f"{name}: unknown {', '.join(unknown)}")
    try:
        ipaddress.IPv4Address(record["host"])
    except ValueError:
        raise RecordError(f"{name}: host must be an IPv4 address") from None
    if not record["port"].isdigit() or not 1 <= int(record["port"]) <= 65535:
        raise RecordError(f"{name}: port must be 1-65535")
    if not USER.fullmatch(record["user"]):
        raise RecordError(f"{name}: invalid user")
    domain = record["domain"]
    if len(domain) > 253 or "." not in domain or not all(LABEL.fullmatch(label) for label in domain.split(".")):
        raise RecordError(f"{name}: invalid domain")
    if not HOST_KEY.fullmatch(record["host_key"]):
        raise RecordError(f"{name}: host_key must be one 'ssh-ed25519 KEY'")
    return record


def parse(name, text):
    record = {}
    for number, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator:
            raise RecordError(f"{name}:{number}: expected key=value")
        key, value = key.strip(), value.strip()
        if key in record:
            raise RecordError(f"{name}:{number}: {key} is repeated")
        record[key] = value
    return validate(name, record)


def load(directory, name):
    path = directory / f"{name}.conf"
    if not NAME.fullmatch(name) or not path.is_file():
        raise RecordError(f"no server record {path}")
    return parse(name, path.read_text(encoding="utf-8"))


def names(directory):
    """Every server name; one broken record fails the whole list instead of being skipped."""
    found = sorted(path.stem for path in directory.glob("*.conf"))
    for name in found:
        load(directory, name)
    return found


def known_hosts(record):
    host = record["host"] if record["port"] == "22" else f"[{record['host']}]:{record['port']}"
    return f"{host} {record['host_key']}"


def check_new(directory, name, record):
    validate(name, record)
    if (directory / f"{name}.conf").exists():
        raise RecordError(f"server {name} already exists")
    for other in names(directory):
        existing = load(directory, other)
        for field in ("host", "domain"):
            if existing[field] == record[field]:
                raise RecordError(f"{record[field]} already belongs to server {other}")


def add(directory, name, record):
    check_new(directory, name, record)
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{name}.conf"
    with open(path, "x", encoding="utf-8", newline="\n") as output:
        output.write(f"# Server {name}: panel https://{record['domain']}/\n"
                     "# Public values only; the private deploy key is the DEPLOY_SSH_KEY secret.\n")
        output.writelines(f"{field}={record[field]}\n" for field in FIELDS)
    return path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("list")
    commands.add_parser("show").add_argument("name")
    for command in ("check", "add"):
        new = commands.add_parser(command)
        new.add_argument("name")
        new.add_argument("--host", required=True)
        new.add_argument("--port", default="22")
        new.add_argument("--domain", required=True)
        if command == "add":
            new.add_argument("--host-key", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "list":
            print("names=" + json.dumps(names(SERVERS), separators=(",", ":")))
        elif args.command == "show":
            record = load(SERVERS, args.name)
            for field in ("host", "port", "user", "domain"):
                print(f"{field}={record[field]}")
            print(f"known_hosts={known_hosts(record)}")
        else:
            record = {"host": args.host, "port": args.port, "user": DEPLOY_USER, "domain": args.domain,
                      "host_key": getattr(args, "host_key", PLACEHOLDER_KEY)}
            if args.command == "check":
                check_new(SERVERS, args.name, record)
            else:
                print(add(SERVERS, args.name, record).relative_to(SERVERS.parents[1]).as_posix())
    except RecordError as error:
        print(f"Server record error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
