#!/usr/bin/env python3
"""Проверить границы конфигурации и откат установки отдельного VPS-моста."""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("bridge", ROOT / "deploy/docker/bridge.py")
bridge = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bridge)


def configuration():
    return {"version": 1, "listeners": [
        {"name": "us-tcp", "transport": "tcp", "listen_port": 8443,
         "target_host": "198.51.100.10", "target_port": 8443},
        {"name": "us-xhttp", "transport": "xhttp", "listen_port": 8444,
         "target_host": "198.51.100.10", "target_port": 8444},
    ]}


class BridgeTests(unittest.TestCase):
    def test_transport_processes_are_independent_and_no_tls_or_proxy_protocol_added(self):
        config = bridge.validate(configuration())
        tcp, xhttp = bridge.render(config, "tcp"), bridge.render(config, "xhttp")
        self.assertIn("bind :::8443 v4v6", tcp)
        self.assertNotIn("8444", tcp)
        self.assertIn("bind :::8444 v4v6", xhttp)
        self.assertNotIn("8443", xhttp)
        self.assertNotIn(" ssl ", tcp + xhttp)
        self.assertNotIn("send-proxy", tcp + xhttp)
        compose = bridge.compose_document(config, {"tcp": "/tmp/tcp.cfg", "xhttp": "/tmp/xhttp.cfg"})
        self.assertEqual(set(compose["services"]), {"tcp", "xhttp"})
        for service in compose["services"].values():
            self.assertEqual(service["network_mode"], "host")
            self.assertEqual(service["user"], "0:0")
            self.assertEqual(service["cap_add"], ["NET_BIND_SERVICE", "SETUID", "SETGID"])
            self.assertNotIn("privileged", service)
            self.assertNotIn("ports", service)
        self.assertIn("    uid 65534\n    gid 65534", tcp)

    def test_rejects_collisions_unknown_fields_udp_and_injection(self):
        mutations = [
            lambda c: c.update(version=True), lambda c: c.update(listeners=[]),
            lambda c: c.update(password="hidden"),
            lambda c: c["listeners"][0].update(transport="h3"),
            lambda c: c["listeners"][0].update(target_host="example.com\nlisten malicious"),
            lambda c: c["listeners"][0].update(target_host="example.com:8443"),
            lambda c: c["listeners"][0].update(target_host="0.0.0.0"),
            lambda c: c["listeners"][0].update(target_host="224.0.0.1"),
            lambda c: c["listeners"][0].update(target_host="169.254.169.254"),
            lambda c: c["listeners"][0].update(target_host="256.1.1.1"),
            lambda c: c["listeners"][0].update(name="tcp\nbackend"),
            lambda c: c["listeners"][0].update(listen_port=22),
            lambda c: c["listeners"][0].update(listen_port=True),
            lambda c: c["listeners"][0].update(target_port=65536),
            lambda c: c["listeners"][1].update(listen_port=8443),
            lambda c: c["listeners"][1].update(name="us-tcp"),
        ]
        for mutation in mutations:
            value = configuration()
            mutation(value)
            with self.subTest(config=value), self.assertRaises(ValueError):
                bridge.validate(value)

    def test_normalized_hosts_ipv6_and_multiple_exits(self):
        config = configuration()
        config["listeners"][0].update(target_host="Exit.Example.COM.")
        config["listeners"].append({"name": "second-tcp", "transport": "tcp", "listen_port": 9443,
                                    "target_host": "2001:4860:4860::8888", "target_port": 443})
        checked = bridge.validate(config)
        text = bridge.render(checked, "tcp")
        self.assertIn("exit.example.com:8443", text)
        self.assertIn("[2001:4860:4860::8888]:443", text)
        self.assertIn("bind :::9443 v4v6", text)

    def test_duplicate_json_keys_and_nonregular_config_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bridge.json"
            path.write_text('{"version":1,"version":1,"listeners":[]}')
            with self.assertRaises(ValueError):
                bridge.read_config(path)
            link = Path(directory) / "link.json"
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                bridge.read_config(link)

    def test_preflight_refuses_self_loop_before_connecting(self):
        config = configuration()
        with patch.object(bridge, "local_addresses", return_value={"198.51.100.10"}), \
                patch.object(bridge.socket, "getaddrinfo", return_value=[(2, 1, 6, "", ("198.51.100.10", 8443))]), \
                patch.object(bridge.socket, "create_connection") as connection:
            with self.assertRaisesRegex(ValueError, "separate VPS"):
                bridge.preflight(config)
            connection.assert_not_called()

    def test_failed_preflight_never_changes_state_or_starts_docker(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(bridge, "run", return_value=subprocess.CompletedProcess([], 0, "")) as run, \
                patch.object(bridge, "preflight", side_effect=OSError("unreachable")):
            state = Path(directory) / "state"
            with self.assertRaises(OSError):
                bridge.deploy(configuration(), state=state)
            self.assertFalse(state.exists())
            self.assertFalse(any("up" in call.args[0] for call in run.call_args_list))

    def test_refuses_install_on_exit_or_unknown_existing_bridge(self):
        for label in ("private-gateway", "private-gateway-node", "private-gateway-bridge"):
            def fake(arguments, **kwargs):
                output = "existing" if f"label=com.docker.compose.project={label}" in arguments else ""
                return subprocess.CompletedProcess(arguments, 0, output)
            with tempfile.TemporaryDirectory() as directory, patch.object(bridge, "run", side_effect=fake), \
                    patch.object(bridge, "preflight") as preflight:
                with self.assertRaises(ValueError):
                    bridge.deploy(configuration(), state=Path(directory) / "state")
                preflight.assert_not_called()

    def simulate(self, state, *, config=None, check=False, fail=False):
        commands = []

        def fake_compose(path, *arguments):
            commands.append((str(path), arguments))
            if fail and arguments[0] == "up" and len([c for c in commands if c[1][0] == "up"]) == 1:
                raise subprocess.CalledProcessError(1, ["docker", "compose", "up"])

        with patch.object(bridge, "run", return_value=subprocess.CompletedProcess([], 0, "")), \
                patch.object(bridge, "preflight"), patch.object(bridge, "verify_haproxy"), \
                patch.object(bridge.socket, "create_connection"), patch.object(bridge, "compose", side_effect=fake_compose):
            if fail:
                with self.assertRaises(subprocess.CalledProcessError):
                    bridge.deploy(config or configuration(), check=check, state=state)
            else:
                bridge.deploy(config or configuration(), check=check, state=state)
        return commands

    def test_check_validates_without_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state"
            self.assertEqual(self.simulate(state, check=True), [])
            self.assertFalse(state.exists())

    def test_repeated_install_keeps_paths_and_unchanged_transport(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state"
            self.simulate(state)
            before = json.loads((state / "compose.json").read_text())
            self.simulate(state)
            self.assertEqual(before, json.loads((state / "compose.json").read_text()))
            changed = configuration()
            changed["listeners"][0]["target_port"] = 443
            self.simulate(state, config=changed)
            after = json.loads((state / "compose.json").read_text())
            self.assertEqual(before["services"]["xhttp"], after["services"]["xhttp"])
            self.assertNotEqual(before["services"]["tcp"], after["services"]["tcp"])

    def test_failed_update_restores_previous_compose_and_state(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state"
            self.simulate(state)
            before = (state / "installed.json").read_text()
            changed = configuration()
            changed["listeners"][0]["target_port"] = 443
            commands = self.simulate(state, config=changed, fail=True)
            self.assertEqual((state / "installed.json").read_text(), before)
            self.assertEqual(commands[-1][0], json.loads(before)["compose"])
            self.assertEqual(commands[-1][1][0], "up")
            self.assertFalse(any(c[1][0] == "down" for c in commands))

    def test_failed_first_install_stops_only_own_stack(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "state"
            commands = self.simulate(state, fail=True)
            self.assertEqual(commands[-1][1], ("down",))
            self.assertFalse((state / "installed.json").exists())


if __name__ == "__main__":
    unittest.main()
