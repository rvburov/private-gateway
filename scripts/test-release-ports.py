#!/usr/bin/env python3
"""Публичные порты берутся из своего релиза, независимо от внутреннего Xray и наличия HAProxy."""

import copy
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("ports", ROOT / "deploy/docker/release-ports.py")
ports = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ports)


class ReleasePortTests(unittest.TestCase):
    def config(self, panel=443, vpn=443, fallback=8443, xray=443):
        return {"services": {
            "api": {"environment": {"GATEWAY_BROWSER_PUBLIC_URL": f"https://example.com:{panel}/browser/vnc.html",
                                    "GATEWAY_PORT": str(vpn), "GATEWAY_FALLBACK_PORT": str(fallback),
                                    "GATEWAY_XRAY_PORT": str(xray)}},
            "edge": {"ports": [{"published": str(port), "target": 443, "protocol": "tcp"}
                                for port in sorted({panel, vpn, fallback} - {0})]},
            "caddy": {"ports": [{"published": "80", "target": 80, "protocol": "tcp"}]},
        }}

    def test_current_dual_port_release(self):
        self.assertEqual(ports.release_ports(self.config()), {"panel": 443, "vpn": 443, "fallback": 8443, "xray": 443})

    def test_single_external_port_is_independent_of_internal_port(self):
        self.assertEqual(ports.release_ports(self.config(8443, 8443, 0)),
                         {"panel": 8443, "vpn": 8443, "fallback": 0, "xray": 443})

    def test_old_release_before_explicit_xray_port(self):
        config = self.config()
        del config["services"]["api"]["environment"]["GATEWAY_XRAY_PORT"]
        config["services"]["api"]["environment"]["GATEWAY_BROWSER_PUBLIC_URL"] = "https://example.com/browser/vnc.html"
        self.assertEqual(ports.release_ports(config)["xray"], 443)
        self.assertEqual(ports.release_ports(config)["panel"], 443)

    def test_old_topology_with_separate_panel_and_vpn(self):
        config = self.config(8443, 443, 0)
        del config["services"]["edge"]
        config["services"]["caddy"]["ports"].append({"published": "8443", "target": 8443})
        config["services"]["xray"] = {"ports": [{"published": "443", "target": 443}]}
        self.assertEqual(ports.release_ports(config)["panel"], 8443)

    def test_rejects_missing_or_duplicate_owner(self):
        for mutate in (lambda c: c["services"]["edge"]["ports"].pop(),
                       lambda c: c["services"].update({"other": copy.deepcopy(c["services"]["edge"])})):
            config = self.config()
            mutate(config)
            with self.assertRaises(ValueError):
                ports.release_ports(config)

    def test_rejects_wrong_internal_target(self):
        config = self.config(8443, 8443, 0)
        config["services"]["edge"]["ports"][0]["target"] = 8443
        with self.assertRaises(ValueError):
            ports.release_ports(config)

    def test_rejects_invalid_ports_and_urls(self):
        for value in ("0", "65536", "-1", "abc", True):
            config = self.config()
            config["services"]["api"]["environment"]["GATEWAY_XRAY_PORT"] = value
            with self.assertRaises(ValueError):
                ports.release_ports(config)
        for value in ("http://example.com", "https://user:secret@example.com", "https://example.com:0"):
            config = self.config()
            config["services"]["api"]["environment"]["GATEWAY_BROWSER_PUBLIC_URL"] = value
            with self.assertRaises(ValueError):
                ports.release_ports(config)


if __name__ == "__main__":
    unittest.main()
