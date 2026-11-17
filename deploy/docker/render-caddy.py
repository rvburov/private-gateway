#!/usr/bin/env python3
"""Создать закрытый Caddyfile панели и необязательного TLS/H3 входа."""

import os
from pathlib import Path
import re
import sys
import tempfile


def main():
    if len(sys.argv) != 4:
        raise SystemExit("Usage: render-caddy.py TEMPLATE OUTPUT STACK_ENV")
    source, destination, env_file = map(Path, sys.argv[1:])
    values = {}
    for line in env_file.read_text().splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            if key in values:
                raise SystemExit("Duplicate stack setting")
            values[key] = value
    domain = values.get("PANEL_DOMAIN", "")
    if not re.fullmatch(r"[A-Za-z0-9.-]+", domain):
        raise SystemExit("Panel domain is invalid")
    content = source.read_text().replace("__PANEL_DOMAIN__", domain)
    if "__" in content:
        raise SystemExit("Caddyfile template has an unresolved placeholder")
    enabled = values.get("GATEWAY_XHTTP_H3_ENABLED", "0")
    if enabled not in ("0", "1"):
        raise SystemExit("GATEWAY_XHTTP_H3_ENABLED must be 0 or 1")
    if enabled == "1":
        h3_domain = values.get("GATEWAY_XHTTP_H3_DOMAIN", "")
        h3_path = values.get("GATEWAY_XHTTP_H3_PATH", "")
        try:
            port = int(values.get("GATEWAY_XHTTP_H3_PORT", "8444"))
            internal_port = int(values.get("GATEWAY_XHTTP_H3_INTERNAL_PORT", "8081"))
        except ValueError:
            raise SystemExit("H3 ports must be integers") from None
        if not re.fullmatch(r"[A-Za-z0-9.-]+", h3_domain) or not re.fullmatch(r"/[A-Za-z0-9_-]{16,128}", h3_path) or not (1 <= port <= 65535 and 1 <= internal_port <= 65535):
            raise SystemExit("H3 domain, path or ports are invalid")
        content += f"""
https://{h3_domain}:{port} {{
    tls {{
        issuer acme {{
            disable_tlsalpn_challenge
        }}
    }}
    @xhttp path {h3_path} {h3_path}/*
    handle @xhttp {{
        reverse_proxy xray-xhttp:{internal_port}
    }}
    respond 404
}}
"""
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=destination.parent, prefix=".Caddyfile.", delete=False) as staged:
        staged.write(content)
        staged_path = Path(staged.name)
    os.chmod(staged_path, 0o600)
    os.replace(staged_path, destination)


if __name__ == "__main__":
    main()
