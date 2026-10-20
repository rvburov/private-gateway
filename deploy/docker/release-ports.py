#!/usr/bin/env python3
"""Определить публичные порты проверенного релиза по нормализованной конфигурации Compose."""

import argparse
import json
import sys
from urllib.parse import urlsplit


def valid_port(value, name, allow_zero=False):
    try:
        if isinstance(value, bool) or not str(value).isascii() or not str(value).isdigit():
            raise ValueError
        result = int(value)
        if not (0 if allow_zero else 1) <= result <= 65535:
            raise ValueError
        return result
    except (TypeError, ValueError):
        raise ValueError(f"Invalid {name} port") from None


def release_ports(config):
    services = config["services"]
    environment = services["api"].get("environment", {})
    browser = urlsplit(environment["GATEWAY_BROWSER_PUBLIC_URL"])
    if browser.scheme != "https" or not browser.hostname or browser.username or browser.password:
        raise ValueError("The public browser URL must be HTTPS without credentials")
    result = {
        "panel": valid_port(443 if browser.port is None else browser.port, "panel"),
        "vpn": valid_port(environment.get("GATEWAY_PORT", 443), "VPN"),
        "fallback": valid_port(environment.get("GATEWAY_FALLBACK_PORT", 0), "fallback", True),
        "xray": valid_port(environment.get("GATEWAY_XRAY_PORT", 443), "Xray"),
    }
    if result["fallback"] == result["vpn"]:
        raise ValueError("The fallback port must differ from the VPN port")
    published = {}
    for name, service in services.items():
        for mapping in service.get("ports", []):
            if mapping.get("protocol", "tcp") != "tcp":
                continue
            port = valid_port(mapping["published"], "published")
            published.setdefault(port, []).append((name, valid_port(mapping["target"], "target")))
    for name, owners in (("panel", {"edge", "caddy"}), ("vpn", {"edge", "xray"}), ("fallback", {"edge"})):
        port = result[name]
        if not port:
            continue
        mappings = published.get(port, [])
        if len(mappings) != 1 or mappings[0][0] not in owners:
            raise ValueError(f"The public {name} port must have exactly one valid owner")
        owner, target = mappings[0]
        expected = 443 if owner == "edge" else result["xray"] if owner == "xray" else port
        # До HAProxy Caddy принимал панель на8443; в общей TLS-схеме он слушает443.
        if target != expected:
            raise ValueError(f"The public {name} port forwards to an unexpected internal port")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--field", choices=("panel", "vpn", "fallback", "xray"))
    args = parser.parse_args()
    try:
        ports = release_ports(json.load(sys.stdin))
    except (KeyError, TypeError, ValueError) as error:
        raise SystemExit(f"Invalid release ports: {error}") from None
    print(ports[args.field] if args.field else json.dumps(ports, sort_keys=True))


if __name__ == "__main__":
    main()
