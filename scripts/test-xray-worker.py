#!/usr/bin/env python3
"""Проверить телеметрию VPN на искусственных proc/cgroup без доступа к рабочему стеку."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("xray_worker", ROOT / "deploy/docker/xray-worker.py")
worker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(worker)


class XrayWorkerMetricsTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.proc, self.cgroup = self.root / "proc", self.root / "cgroup"

    def write(self, path, value):
        destination = self.root / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(value, encoding="utf-8")

    def test_xhttp_startup_does_not_inherit_container_output(self):
        configuration = self.root / "test-config.json"
        for networks, suppressed in ((["raw"], False), (["raw", "xhttp"], True), (None, True)):
            with self.subTest(networks=networks):
                configuration.write_text(json.dumps({"inbounds": [
                    {"streamSettings": {"network": network}} for network in networks
                ]}) if networks is not None else "invalid-test-document", encoding="utf-8")
                child = mock.Mock()
                child.poll.return_value = 1
                with mock.patch.object(worker, "CONFIG", configuration), \
                        mock.patch.object(worker, "process", None), \
                        mock.patch.object(worker.subprocess, "Popen", return_value=child) as start:
                    self.assertFalse(worker.start_child({}))
                options = start.call_args.kwargs
                if suppressed:
                    self.assertEqual(options, {"stdout": worker.subprocess.DEVNULL,
                                               "stderr": worker.subprocess.DEVNULL})
                else:
                    self.assertEqual(options, {})

    def test_v2_collects_own_namespace_and_excludes_loopback(self):
        for path, value in {
            "proc/self/cgroup": "0::/own\n",
            "cgroup/cpu.max": "100000 100000", "cgroup/memory.current": "999999",
            "cgroup/own/cpu.max": "50000 100000", "cgroup/own/memory.current": "1024",
            "cgroup/own/memory.max": "8192",
            "cgroup/own/cpu.stat": "usage_usec 123456\nnr_throttled 3\nthrottled_usec 5000",
            "proc/net/dev": "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n"
                            "lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0\n"
                            "eth0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n"
                            "eth1: 300 1 0 0 0 0 0 0 400 1 0 0 0 0 0 0\n",
            "proc/net/snmp": "Ip: Forwarding DefaultTTL\nIp: 1 64\n"
                             "Tcp: RtoAlgorithm OutSegs RetransSegs InErrs\nTcp: 1 1000 4 0\n",
        }.items():
            self.write(path, value)
        result = worker.collect_metrics(True, self.proc, self.cgroup)
        self.assertEqual(result["version"], 1)
        self.assertTrue(result["running"])
        self.assertRegex(result["sampledAt"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        self.assertEqual(result["cgroup"], {"memoryBytes": 1024, "memoryLimitBytes": 8192,
                         "cpuQuotaCores": 0.5, "cpuUsageUsec": 123456,
                         "cpuThrottledUsec": 5000, "cpuThrottledPeriods": 3})
        self.assertEqual(result["network"], {"rxBytes": 400, "txBytes": 600})
        self.assertEqual(result["tcp"], {"outSegments": 1000, "retransmittedSegments": 4})
        serialized = json.dumps(result, allow_nan=False)
        self.assertNotIn(str(self.root), serialized)
        self.assertNotIn("eth0", serialized)

    def test_v1_uses_own_nested_group_and_converts_ns(self):
        for path, value in {
            "proc/self/cgroup": "2:memory:/own\n3:cpu,cpuacct:/own\n",
            "cgroup/memory/memory.usage_in_bytes": "999999",
            "cgroup/memory/own/memory.usage_in_bytes": "1024",
            "cgroup/memory/own/memory.limit_in_bytes": "9223372036854771712",
            "cgroup/cpu,cpuacct/cpu.cfs_period_us": "100000",
            "cgroup/cpu,cpuacct/own/cpu.cfs_period_us": "100000",
            "cgroup/cpu,cpuacct/own/cpu.cfs_quota_us": "200000",
            "cgroup/cpu,cpuacct/own/cpuacct.usage": "4567890",
            "cgroup/cpu,cpuacct/own/cpu.stat": "nr_throttled 2\nthrottled_time 7654321",
        }.items():
            self.write(path, value)
        self.assertEqual(worker.cgroup_metrics(self.proc, self.cgroup), {
            "memoryBytes": 1024, "memoryLimitBytes": None, "cpuQuotaCores": 2,
            "cpuUsageUsec": 4567, "cpuThrottledUsec": 7654, "cpuThrottledPeriods": 2})

    def test_missing_counters_are_unknown_and_zero_is_known(self):
        result = worker.collect_metrics(False, self.proc, self.cgroup)
        self.assertFalse(result["running"])
        self.assertTrue(all(value is None for value in result["cgroup"].values()))
        self.assertIsNone(result["network"])
        self.assertIsNone(result["tcp"])
        self.write("proc/self/cgroup", "0::/\n")
        self.write("cgroup/cpu.max", "max 100000")
        self.write("cgroup/memory.current", "0")
        self.write("cgroup/memory.max", "max")
        self.write("cgroup/cpu.stat", "usage_usec 0\nnr_throttled 0\nthrottled_usec 0")
        result = worker.cgroup_metrics(self.proc, self.cgroup)
        self.assertEqual(result["memoryBytes"], 0)
        self.assertEqual(result["cpuUsageUsec"], 0)
        self.assertIsNone(result["memoryLimitBytes"])
        self.assertIsNone(result["cpuQuotaCores"])

    def test_parent_cgroup_never_replaces_missing_own_group(self):
        for path, value in {
            "cgroup/cpu.max": "100000 100000", "cgroup/memory.current": "999999",
            "cgroup/cpu.stat": "usage_usec 7777\n",
            "cgroup/memory/memory.usage_in_bytes": "888888",
            "cgroup/cpu/cpu.cfs_period_us": "100000",
            "cgroup/cpu/cpu.cfs_quota_us": "50000",
        }.items():
            self.write(path, value)
        for group in ("0::/missing\n", "2:memory:/missing\n3:cpu,cpuacct:/missing\n", "0::/../../host\n", ""):
            with self.subTest(group=group):
                self.write("proc/self/cgroup", group)
                self.assertTrue(all(value is None for value in
                                    worker.cgroup_metrics(self.proc, self.cgroup).values()))

    def test_invalid_network_data_does_not_become_zero(self):
        for text in ("eth0: 1 2\n", "eth0: -1 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0\n",
                     "eth0: invalid 1 0 0 0 0 0 0 2 1 0 0 0 0 0 0\n"):
            with self.subTest(text=text):
                self.write("proc/net/dev", text)
                self.assertIsNone(worker.network_metrics(self.proc))
        self.write("proc/net/snmp", "Tcp: OutSegs RetransSegs\nTcp: 5\n")
        self.assertIsNone(worker.tcp_metrics(self.proc))
        self.write("proc/net/snmp", "Tcp: OutSegs RetransSegs\nTcp: 5 -1\n")
        self.assertIsNone(worker.tcp_metrics(self.proc))

    def test_proc_reads_and_numeric_values_are_bounded(self):
        self.write("proc/too-large", "x" * 65537)
        self.assertEqual(worker.read_metric(self.proc / "too-large"), "")
        for value in ("-1", "+1", "1.5", "18446744073709551616", "١", None, 10):
            with self.subTest(value=value):
                self.assertIsNone(worker.metric_uint(value))
        for value in ("NaN", "inf", "-1", None):
            with self.subTest(value=value):
                self.assertIsNone(worker.metric_float(value))

    def test_status_semantics_and_atomic_private_metrics(self):
        status, metrics = self.root / "xray.status.json", self.root / "xray.metrics.json"
        state = {"mtime_ns": 1234567890, "sha256": "a" * 64}
        with mock.patch.object(worker, "STATUS", status):
            worker.write_status(state, True)
        self.assertEqual(json.loads(status.read_text()), dict(state, running=True, error=""))
        worker.write_document(metrics, {"version": 1, "running": False})
        self.assertEqual(json.loads(metrics.read_text()), {"version": 1, "running": False})
        self.assertEqual(metrics.stat().st_mode & 0o777, 0o600)
        self.assertEqual(status.stat().st_mode & 0o777, 0o600)
        self.assertEqual(sorted(path.name for path in self.root.iterdir()),
                         ["xray.metrics.json", "xray.status.json"])
        with mock.patch.object(worker.os, "replace", side_effect=OSError("test write error")):
            with self.assertRaises(OSError):
                worker.write_document(metrics, {"version": 2})
        self.assertEqual(json.loads(metrics.read_text()), {"version": 1, "running": False})
        self.assertFalse(list(self.root.glob(".xray.metrics-*")))

    def test_telemetry_write_failure_does_not_restart_running_vpn(self):
        active = mock.Mock()
        active.poll.return_value = None
        sleeps = []
        def finish_after_one_loop(delay):
            sleeps.append(delay)
            worker.running = False
        previous_running, previous_process = worker.running, worker.process
        try:
            worker.running, worker.process = True, active
            with mock.patch.object(worker.signal, "signal"), \
                 mock.patch.object(worker, "snapshot", return_value=None), \
                 mock.patch.object(worker, "write_status"), \
                 mock.patch.object(worker, "write_document", side_effect=OSError("test telemetry error")), \
                 mock.patch.object(worker, "collect_metrics", return_value={}), \
                 mock.patch.object(worker, "start_child") as start, \
                 mock.patch.object(worker, "stop_child") as stop, \
                 mock.patch.object(worker.time, "sleep", side_effect=finish_after_one_loop), \
                 mock.patch("builtins.print"):
                worker.main()
            start.assert_not_called()
            self.assertEqual(stop.call_count, 1)
            self.assertEqual(sleeps, [0.25])
        finally:
            worker.running, worker.process = previous_running, previous_process


if __name__ == "__main__":
    unittest.main()
