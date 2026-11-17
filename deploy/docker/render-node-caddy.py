#!/usr/bin/env python3
"""Создать закрытый Caddyfile TLS/H3 для удалённого узла."""

import os
from pathlib import Path
import re
import sys
import tempfile


def main():
    if len(sys.argv) != 4:
        raise SystemExit("Usage: render-node-caddy.py TEMPLATE OUTPUT STACK_ENV")
    source, destination, env_file = map(Path, sys.argv[1:])
    values = dict(line.split("=", 1) for line in env_file.read_text().splitlines() if "=" in line)
    domain = values.get("GATEWAY_XHTTP_H3_DOMAIN", "")
    path = values.get("GATEWAY_XHTTP_H3_PATH", "")
    try:
        port = int(values.get("GATEWAY_XHTTP_H3_PORT", "8444"))
        internal = int(values.get("GATEWAY_XHTTP_H3_INTERNAL_PORT", "8081"))
    except ValueError:
        raise SystemExit("H3 ports must be integers") from None
    if (values.get("GATEWAY_XHTTP_H3_ENABLED") != "1" or
            not re.fullmatch(r"[A-Za-z0-9.-]+", domain) or
            not re.fullmatch(r"/[A-Za-z0-9_-]{16,128}", path) or
            not (1 <= port <= 65535 and 1 <= internal <= 65535)):
        raise SystemExit("H3 settings are invalid")
    content = source.read_text()
    for name, value in {"DOMAIN": domain, "PORT": str(port), "PATH": path,
                        "INTERNAL_PORT": str(internal)}.items():
        content = content.replace("__H3_" + name + "__", value)
    if "__" in content:
        raise SystemExit("Caddyfile template has an unresolved placeholder")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=destination.parent, prefix=".Caddyfile.", delete=False) as staged:
        staged.write(content)
        staged_path = Path(staged.name)
    os.chmod(staged_path, 0o600)
    os.replace(staged_path, destination)


if __name__ == "__main__":
    main()
