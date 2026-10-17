#!/usr/bin/env python3
"""Проверить записи deploy/servers, которые читает CI/CD и пишет scripts/add-server.sh."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("servers", Path(__file__).with_name("servers.py"))
servers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(servers)

KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFw93RU6Xzb2UJEPnTVXhyuvAw4MYQZuEooLREfOZIv0"
RECORD = "host=192.0.2.10\nport=22\nuser=gateway-deploy\ndomain=gateway.example.test\nhost_key=" + KEY + "\n"


class ServerRecordTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.directory = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def write(self, name, text):
        (self.directory / f"{name}.conf").write_text(text, encoding="utf-8", newline="\n")

    def test_valid_record_and_known_hosts_line(self):
        self.write("main", "# comment\n\n" + RECORD)
        record = servers.load(self.directory, "main")
        self.assertEqual(record["domain"], "gateway.example.test")
        self.assertEqual(servers.known_hosts(record), "192.0.2.10 " + KEY)
        record["port"] = "2222"
        self.assertEqual(servers.known_hosts(record), "[192.0.2.10]:2222 " + KEY)

    def test_invalid_records_are_rejected(self):
        broken = {
            "missing field": RECORD.replace("user=gateway-deploy\n", ""),
            "unknown field": RECORD + "password=x\n",
            "repeated field": RECORD + "port=22\n",
            "not key=value": RECORD + "host\n",
            "hostname": RECORD.replace("192.0.2.10", "vps.example.test"),
            "port": RECORD.replace("port=22", "port=70000"),
            "domain": RECORD.replace("gateway.example.test", "-bad.example"),
            "RSA host key": RECORD.replace(KEY, "ssh-rsa AAAAB3NzaC1yc2E"),
            "two host keys": RECORD.replace(KEY, KEY + " " + KEY[12:]),
        }
        for case, text in broken.items():
            with self.subTest(case):
                self.write("bad", text)
                with self.assertRaises(servers.RecordError):
                    servers.load(self.directory, "bad")

    def test_list_fails_on_any_broken_record(self):
        self.write("b", RECORD)
        self.write("a", RECORD.replace("192.0.2.10", "192.0.2.11").replace("gateway.", "other."))
        self.assertEqual(servers.names(self.directory), ["a", "b"])
        self.write("c", RECORD.replace("port=22", "port=x"))
        with self.assertRaises(servers.RecordError):
            servers.names(self.directory)

    def test_add_writes_lf_record_once_and_refuses_duplicates(self):
        record = {"host": "192.0.2.10", "port": "22", "user": "gateway-deploy",
                  "domain": "gateway.example.test", "host_key": KEY}
        path = servers.add(self.directory, "new", dict(record))
        self.assertNotIn(b"\r", path.read_bytes())
        self.assertEqual(servers.load(self.directory, "new"), record)
        for name, change in (("new", {}), ("same-host", {"domain": "other.example.test"}),
                             ("same-domain", {"host": "192.0.2.11"})):
            with self.subTest(name):
                with self.assertRaises(servers.RecordError):
                    servers.add(self.directory, name, {**record, **change})
        with self.assertRaises(servers.RecordError):
            servers.add(self.directory, "Bad_Name", {**record, "host": "192.0.2.12", "domain": "x.example.test"})

    def test_repository_records_and_deploy_key(self):
        names = servers.names(servers.SERVERS)
        self.assertIn("main", names)
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(servers.main(["list"]), 0)
        self.assertEqual(json.loads(output.getvalue().removeprefix("names=")), names)
        key = (servers.SERVERS / "deploy-key.pub").read_text(encoding="utf-8")
        self.assertRegex(key, r"\Assh-ed25519 [A-Za-z0-9+/]{68}( [^\r\n]*)?\n\Z")
        self.assertNotIn("PRIVATE KEY", key)
        for name in names:
            record = servers.load(servers.SERVERS, name)
            self.assertEqual(record["user"], servers.DEPLOY_USER)
            self.assertTrue(re.fullmatch(r"[^\s]+ ssh-ed25519 \S+", servers.known_hosts(record)))


if __name__ == "__main__":
    unittest.main()
