#!/usr/bin/env python3
"""Проверки методики замеров и безопасного отчёта без настоящих сетевых запросов."""

from contextlib import redirect_stderr, redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("benchmark_transports", ROOT / "scripts/benchmark-transports.py")
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)


class BenchmarkTransportTests(unittest.TestCase):
    def options(self, *extra):
        return benchmark.parser().parse_args([
            "--tcp-socks", "127.0.0.1:1080", "--xhttp-socks", "127.0.0.1:1081",
            "--url", "https://example.org/benchmark", *extra,
        ])

    def completed(self, ttfb=0.5, status="204", total=None, returncode=0, stderr=""):
        return subprocess.CompletedProcess([], returncode, json.dumps({
            "http_code": status, "time_starttransfer": ttfb, "time_total": ttfb + 0.1 if total is None else total,
        }), stderr)

    def probe(self):
        return benchmark.probe("127.0.0.1:1080", "https://example.org/benchmark", 204, 10, 30)

    def test_defaults_and_only_loopback_socks(self):
        options = self.options()
        self.assertEqual((options.requests, options.warmup, options.expected_status), (10, 1, 204))
        self.assertEqual(benchmark.socks_endpoint("localhost:1080"), "127.0.0.1:1080")
        self.assertEqual(benchmark.socks_endpoint("[::1]:1080"), "[::1]:1080")
        for endpoint in ("192.0.2.1:1080", "example.com:1080", "127.0.0.1:0", "127.0.0.1:65536",
                         "user:placeholder@127.0.0.1:1080", "127.0.0.1:1080/path", "[::1%lo]:1080"):
            with self.subTest(endpoint=endpoint), self.assertRaises(benchmark.argparse.ArgumentTypeError):
                benchmark.socks_endpoint(endpoint)

    def test_rejects_credential_urls_and_unbounded_values_without_echoing_them(self):
        for url in ("file:///placeholder", "https://user:placeholder@example.com/",
                    "https://example.org/?token=placeholder", "https://example.org/#fragment",
                    "https://example.org:0/", "https://example.org/\npath"):
            with self.subTest(url=url), self.assertRaises(benchmark.argparse.ArgumentTypeError) as error:
                benchmark.target_url(url)
            self.assertNotIn("placeholder", str(error.exception))
        for arguments in (("--requests", "0"), ("--requests", "1001"), ("--warmup", "-1"),
                          ("--max-time", "nan"), ("--connect-timeout", "inf"), ("--max-time", "0")):
            with self.subTest(arguments=arguments), redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.options(*arguments)

    def test_curl_is_explicit_bounded_and_keeps_inherited_environment(self):
        with mock.patch.object(benchmark.subprocess, "run", return_value=self.completed()) as run:
            sample = self.probe()
        self.assertTrue(sample["success"])
        command = run.call_args.args[0]
        self.assertEqual(command[:2], ["curl", "-q"])
        self.assertEqual(command[command.index("--proxy") + 1], "socks5h://127.0.0.1:1080")
        self.assertEqual(command[command.index("--noproxy") + 1], "")
        self.assertEqual(command[command.index("--retry") + 1], "0")
        self.assertIn("--connect-timeout", command)
        self.assertIn("--max-time", command)
        self.assertIn("--show-error", command)
        self.assertNotIn("--insecure", command)
        self.assertNotIn("-k", command)
        self.assertNotIn("--location", command)
        self.assertNotIn("env", run.call_args.kwargs)
        self.assertEqual(run.call_args.kwargs["timeout"], 35)
        self.assertEqual(run.call_args.kwargs["stdin"], subprocess.DEVNULL)

    def test_ten_measured_requests_follow_first_probe_and_warmup(self):
        calls = {"tcp": 0, "xhttp": 0}
        order = []

        def perform(command, **unused):
            name = "xhttp" if command[command.index("--proxy") + 1].endswith(":1081") else "tcp"
            order.append(name)
            calls[name] += 1
            number = calls[name]
            value = 9.0 if number == 1 else 8.0 if number == 2 else (number - 2) / 10
            return self.completed(ttfb=value if name == "tcp" else value / 2)

        with mock.patch.object(benchmark.subprocess, "run", side_effect=perform):
            report = benchmark.benchmark(self.options())
        self.assertEqual(order, ["tcp", "xhttp"] * 12)
        self.assertEqual(calls, {"tcp": 12, "xhttp": 12})
        self.assertEqual(report["results"]["tcp"]["cold"]["ttfb_seconds"], 9)
        for result in report["results"].values():
            self.assertEqual(result["warmup"]["attempts"], 1)
            self.assertEqual(result["measured"]["attempts"], 10)
            self.assertEqual(result["measured"]["successes"], 10)
        self.assertAlmostEqual(report["results"]["tcp"]["measured"]["median_ttfb_seconds"], 0.55)
        self.assertAlmostEqual(report["comparison"]["median_reduction_seconds"], 0.275)
        self.assertAlmostEqual(report["comparison"]["median_reduction_percent"], 50)
        self.assertFalse(report["reuse_verified"])

    def test_curl_failure_with_zero_time_is_not_a_fast_success(self):
        replies = [self.completed(), self.completed(),
                   self.completed(ttfb=0, returncode=28), self.completed(ttfb=0.2),
                   self.completed(ttfb=0.5), self.completed(ttfb=0.3),
                   self.completed(ttfb=0.7), self.completed(ttfb=0.4)]
        with mock.patch.object(benchmark.subprocess, "run", side_effect=replies):
            report = benchmark.benchmark(self.options("--requests", "3", "--warmup", "0"))
        tcp = report["results"]["tcp"]["measured"]
        self.assertEqual((tcp["attempts"], tcp["successes"], tcp["errors"]), (3, 2, 1))
        self.assertAlmostEqual(tcp["median_ttfb_seconds"], 0.6)
        self.assertIsNone(tcp["samples"][0]["ttfb_seconds"])
        self.assertEqual(report["comparison"]["status"], "unavailable")
        self.assertIsNone(report["comparison"]["median_reduction_percent"])

    def test_unexpected_http_status_is_an_error(self):
        with mock.patch.object(benchmark.subprocess, "run", return_value=self.completed(ttfb=0.001, status="403")):
            sample = self.probe()
        self.assertFalse(sample["success"])
        self.assertEqual((sample["error"], sample["http_status"]), ("unexpected_status", 403))
        self.assertIsNone(sample["ttfb_seconds"])

    def test_zero_and_invalid_metrics_are_not_successes(self):
        replies = [self.completed(ttfb=0), self.completed(ttfb=-1), self.completed(ttfb=True),
                   self.completed(ttfb=float("nan")), self.completed(ttfb=float("inf")),
                   self.completed(ttfb=0.5, total=0.1), self.completed(status="000"),
                   subprocess.CompletedProcess([], 0, "not-json", ""),
                   subprocess.CompletedProcess([], 0, "[]", "")]
        for reply in replies:
            with self.subTest(stdout=reply.stdout), mock.patch.object(benchmark.subprocess, "run", return_value=reply):
                sample = self.probe()
            self.assertFalse(sample["success"])
            self.assertEqual(sample["error"], "invalid_metrics")
            self.assertIsNone(sample["ttfb_seconds"])

    def test_subprocess_failures_are_sanitized(self):
        failures = [(FileNotFoundError("placeholder-private-path"), "curl_unavailable"),
                    (OSError("placeholder-private-path"), "process_start_failed"),
                    (subprocess.TimeoutExpired(["curl", "https://example.org/private"], 35,
                                               output="placeholder-output", stderr="placeholder-secret"), "process_timeout")]
        for error, code in failures:
            with self.subTest(code=code), mock.patch.object(benchmark.subprocess, "run", side_effect=error):
                sample = self.probe()
            self.assertEqual(sample["error"], code)
            self.assertNotIn("placeholder", json.dumps(sample))
            self.assertNotIn("example.org", json.dumps(sample))

    def test_json_report_does_not_expose_urls_stderr_or_extra_curl_fields(self):
        raw = json.dumps({"http_code": "204", "time_starttransfer": 0.5, "time_total": 0.6,
                          "url_effective": "https://example.org/private", "remote_ip": "192.0.2.7"})
        reply = subprocess.CompletedProcess([], 0, raw, "placeholder-secret")
        output = io.StringIO()
        with mock.patch.object(benchmark.subprocess, "run", return_value=reply), redirect_stdout(output):
            code = benchmark.main(["--tcp-socks", "127.0.0.1:1080", "--xhttp-socks", "127.0.0.1:1081",
                                   "--url", "https://example.org/private", "--requests", "1", "--warmup", "0", "--json"])
        self.assertEqual(code, 0)
        text = output.getvalue()
        for hidden in ("example.org", "private", "192.0.2.7", "placeholder-secret", "1080", "1081"):
            self.assertNotIn(hidden, text)
        report = json.loads(text)
        self.assertEqual(report["comparison"]["median_reduction_seconds"], 0)
        self.assertFalse(report["reuse_verified"])

    def test_exit_code_reports_failures_and_human_report_lists_samples(self):
        output = io.StringIO()
        reply = self.completed(ttfb=0, returncode=28, stderr="placeholder-secret")
        with mock.patch.object(benchmark.subprocess, "run", return_value=reply), redirect_stdout(output):
            code = benchmark.main(["--tcp-socks", "127.0.0.1:1080", "--xhttp-socks", "127.0.0.1:1081",
                                   "--url", "https://example.org/", "--requests", "1", "--warmup", "0"])
        self.assertEqual(code, 1)
        self.assertIn("ошибок 1", output.getvalue())
        self.assertIn("1: ошибка", output.getvalue())
        self.assertNotIn("placeholder-secret", output.getvalue())

    def test_slower_xhttp_gives_negative_reduction_not_claimed_acceleration(self):
        replies = [self.completed(), self.completed(), self.completed(ttfb=0.2), self.completed(ttfb=0.3)]
        with mock.patch.object(benchmark.subprocess, "run", side_effect=replies):
            report = benchmark.benchmark(self.options("--requests", "1", "--warmup", "0"))
        self.assertAlmostEqual(report["comparison"]["median_reduction_seconds"], -0.1)
        self.assertAlmostEqual(report["comparison"]["median_reduction_percent"], -50)
        self.assertIn("XHTTP оказался медленнее", benchmark.display(report))

    def test_rejects_same_local_endpoint_without_running_curl(self):
        with mock.patch.object(benchmark.subprocess, "run") as run, redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            benchmark.main(["--tcp-socks", "localhost:1080", "--xhttp-socks", "127.0.0.1:1080"])
        run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
