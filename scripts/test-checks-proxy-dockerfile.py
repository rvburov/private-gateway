#!/usr/bin/env python3
"""Проверки облачной сборки: доверие CA, сохранение закреплённых загрузок и один слой VFS."""

import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("checks_proxy", ROOT / "scripts/checks-proxy-dockerfile.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ProxyDockerfileTest(unittest.TestCase):
    def test_repository_build_retains_pins_and_uses_one_setup_layer(self):
        source = (ROOT / "deploy/checks/Dockerfile").read_text()
        output = MODULE.prepare(source)
        parsed = MODULE.instructions(output)
        self.assertEqual(sum(kind == "RUN" for kind, _ in parsed), 1)
        self.assertEqual([v for k, v in parsed if k == "FROM"], [v for k, v in MODULE.instructions(source) if k == "FROM"])
        for text in ("sha256sum -c -", "sqlc_1.30.0", "actionlint_1.7.12", "npm ci --no-audit --no-fund", "go mod download"):
            self.assertIn(text, output)
        self.assertIn("required=true,mode=0444", output)
        self.assertIn("NODE_EXTRA_CA_CERTS=/run/secrets/proxy_ca", output)
        self.assertIn("runuser -u checks -- /bin/sh -ec", output)
        self.assertNotIn("--mount", source)
        self.assertNotIn("strict-ssl=false", output)
        self.assertIn("cp -a /run/checks-context/. /home/checks/src", output)

    def test_metadata_and_multiline_commands_are_preserved(self):
        source = "FROM node AS node\nFROM go\nRUN echo first \\\n && echo second\nUSER checks\nWORKDIR /home/checks/src\nENV X=y\nCMD [\"bash\"]\n"
        output = MODULE.prepare(source)
        self.assertIn("echo first  && echo second", output)
        self.assertTrue(output.endswith('USER checks\nWORKDIR /home/checks/src\nENV X=y\nCMD ["bash"]\n'))

    def test_unknown_instructions_fail_without_silent_changes(self):
        for source in ("FROM base\nRUN x\n", "FROM node AS node\nFROM go\nADD x y\n", "FROM node AS node\nFROM go\nRUN x \\\n"):
            with self.subTest(source=source), self.assertRaises(ValueError):
                MODULE.prepare(source)


if __name__ == "__main__":
    unittest.main()
