package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func diagnosticsFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestServerDiagnosticsScopesAndCgroupV2(t *testing.T) {
	root := t.TempDir()
	paths := diagnosticsPaths{filepath.Join(root, "proc"), filepath.Join(root, "cgroup")}
	for path, value := range map[string]string{
		"proc/uptime": "123.5 300", "proc/loadavg": "0.20 0.50 0.75 1/100 42",
		"proc/meminfo":     "MemTotal: 1000 kB\nMemAvailable: 200 kB\n",
		"proc/stat":        "cpu  100 10 20 500 5 2 3 60 90 5\ncpu0 50 5 10 250 2 1 2 30 45 2\n",
		"proc/self/cgroup": "0::/\n", "cgroup/cpu.max": "150000 100000\n",
		"cgroup/cpu.stat":       "usage_usec 1200000\nnr_throttled 2\nthrottled_usec 4000\n",
		"cgroup/memory.current": "4096", "cgroup/memory.max": "8192",
	} {
		diagnosticsFixture(t, root, path, value)
	}
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	diagnosticsFixture(t, root, "state/xray.metrics.json", `{"version":1,"sampledAt":"2026-10-22T09:59:58Z","running":true,"cgroup":{"memoryBytes":12,"memoryLimitBytes":null,"cpuQuotaCores":2,"cpuUsageUsec":1500,"cpuThrottledUsec":0,"cpuThrottledPeriods":0},"network":{"rxBytes":100,"txBytes":200},"tcp":{"outSegments":50,"retransmittedSegments":2}}`)
	result := collectServerDiagnosticsAt(context.Background(), filepath.Join(root, "state/xray.json"), paths, now)
	if result.SampledAt != "2026-10-22T10:00:00Z" || result.Runtime.OS == "" || result.Runtime.CPUCount < 1 {
		t.Fatalf("invalid runtime information: %+v", result.Runtime)
	}
	if result.System.UptimeSeconds == nil || *result.System.UptimeSeconds != 123.5 || len(result.System.LoadAverage) != 3 || result.System.LoadAverage[2] != 0.75 {
		t.Fatalf("invalid system metrics: %+v", result.System)
	}
	if result.System.MemoryTotalBytes == nil || *result.System.MemoryTotalBytes != 1024000 || *result.System.MemoryAvailableBytes != 204800 {
		t.Fatalf("invalid memory: %+v", result.System)
	}
	if result.System.CPU == nil || result.System.CPU.TotalTicks != 700 || result.System.CPU.StealTicks != 60 {
		t.Fatalf("guest accounting must not be counted twice: %+v", result.System.CPU)
	}
	if result.API.CPUQuotaCores == nil || *result.API.CPUQuotaCores != 1.5 || *result.API.CPUUsageUsec != 1200000 || *result.API.CPUThrottledUsec != 4000 || *result.API.MemoryLimitBytes != 8192 {
		t.Fatalf("invalid API cgroup metrics: %+v", result.API)
	}
	if result.VPN.Status != "ok" || result.VPN.Running == nil || !*result.VPN.Running || result.VPN.Network.RXBytes != 100 || result.VPN.TCP.RetransmittedSegments != 2 || *result.VPN.Cgroup.MemoryBytes != 12 {
		t.Fatalf("invalid worker metrics: %+v", result.VPN)
	}
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), root) || strings.Contains(string(data), "privateKey") || !strings.Contains(string(data), `"cpuQuotaCores":1.5`) {
		t.Fatalf("unsafe or malformed diagnostics JSON: %s (%v)", data, err)
	}
}

func TestServerDiagnosticsCgroupV1AndOwnNestedGroup(t *testing.T) {
	root := t.TempDir()
	paths := diagnosticsPaths{filepath.Join(root, "proc"), filepath.Join(root, "cgroup")}
	for path, value := range map[string]string{
		"proc/self/cgroup":                         "2:memory:/own\n3:cpu,cpuacct:/own\n",
		"cgroup/memory/memory.usage_in_bytes":      "999999",
		"cgroup/memory/own/memory.usage_in_bytes":  "1024",
		"cgroup/memory/own/memory.limit_in_bytes":  "9223372036854771712",
		"cgroup/cpu,cpuacct/cpu.cfs_period_us":     "100000",
		"cgroup/cpu,cpuacct/own/cpu.cfs_period_us": "100000",
		"cgroup/cpu,cpuacct/own/cpu.cfs_quota_us":  "50000",
		"cgroup/cpu,cpuacct/own/cpuacct.usage":     "12345678",
		"cgroup/cpu,cpuacct/own/cpu.stat":          "nr_throttled 3\nthrottled_time 2000000\n",
	} {
		diagnosticsFixture(t, root, path, value)
	}
	result := collectServerDiagnosticsAt(context.Background(), "", paths, time.Now())
	if result.API.MemoryBytes == nil || *result.API.MemoryBytes != 1024 || result.API.MemoryLimitBytes != nil || result.API.CPUQuotaCores == nil || *result.API.CPUQuotaCores != 0.5 || *result.API.CPUUsageUsec != 12345 || *result.API.CPUThrottledUsec != 2000 || *result.API.CPUThrottledPeriods != 3 {
		t.Fatalf("must use own v1 group and convert ns to us: %+v", result.API)
	}
	if result.System.MemoryTotalBytes != nil || result.System.UptimeSeconds != nil || result.System.CPU != nil || result.System.LoadAverage != nil || result.VPN.Status != "unavailable" {
		t.Fatalf("missing metrics must remain unknown: %+v", result)
	}
}

func TestServerDiagnosticsUnlimitedAndInvalidMetrics(t *testing.T) {
	root := t.TempDir()
	paths := diagnosticsPaths{filepath.Join(root, "proc"), filepath.Join(root, "cgroup")}
	for path, value := range map[string]string{
		"proc/uptime": "NaN 1", "proc/loadavg": "1 Inf 2 0/1 1",
		"proc/meminfo": "MemTotal: 18446744073709551615 kB\nMemAvailable: invalid kB",
		"proc/stat":    "cpu 1 2 3 4 5 6 7 NaN", "proc/self/cgroup": "0::/own\n",
		"cgroup/cpu.max": "100000 100000", "cgroup/memory.current": "9999",
		"cgroup/own/cpu.max": "max 100000", "cgroup/own/memory.current": "0",
		"cgroup/own/memory.max": "max", "cgroup/own/cpu.stat": "usage_usec 0\nthrottled_usec invalid\n",
	} {
		diagnosticsFixture(t, root, path, value)
	}
	result := collectServerDiagnosticsAt(context.Background(), "", paths, time.Now())
	if result.API.MemoryBytes == nil || *result.API.MemoryBytes != 0 || result.API.MemoryLimitBytes != nil || result.API.CPUQuotaCores != nil || result.API.CPUUsageUsec == nil || *result.API.CPUUsageUsec != 0 || result.API.CPUThrottledUsec != nil {
		t.Fatalf("zero is known; max and malformed values are unknown: %+v", result.API)
	}
	if result.System.MemoryTotalBytes != nil || result.System.MemoryAvailableBytes != nil || result.System.UptimeSeconds != nil || result.System.LoadAverage != nil || result.System.CPU != nil {
		t.Fatalf("invalid proc values must not become numbers: %+v", result.System)
	}
}

func TestVPNDiagnosticsFreshnessAndInvalidFiles(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct{ name, data, status string }{
		{"stale", `{"version":1,"sampledAt":"2026-10-22T09:59:44Z","running":true,"network":{"rxBytes":123,"txBytes":456}}`, "stale"},
		{"future", `{"version":1,"sampledAt":"2026-10-22T10:00:06Z","running":true}`, "invalid"},
		{"version", `{"version":2,"sampledAt":"2026-10-22T10:00:00Z","running":true}`, "invalid"},
		{"missing_running", `{"version":1,"sampledAt":"2026-10-22T10:00:00Z"}`, "invalid"},
		{"broken", `{`, "invalid"},
		{"negative_counter", `{"version":1,"sampledAt":"2026-10-22T10:00:00Z","running":true,"network":{"rxBytes":-1,"txBytes":5}}`, "invalid"},
		{"bad_quota", `{"version":1,"sampledAt":"2026-10-22T10:00:00Z","running":true,"cgroup":{"cpuQuotaCores":0}}`, "invalid"},
		{"too_large", strings.Repeat(" ", 64*1024+1), "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			diagnosticsFixture(t, root, "xray.metrics.json", test.data)
			result := readVPNDiagnostics(context.Background(), filepath.Join(root, "xray.json"), now)
			if result.Status != test.status || result.Running != nil || result.Network != nil || result.Cgroup != nil || result.TCP != nil {
				t.Fatalf("untrusted or stale metrics must not be presented as current: %+v", result)
			}
		})
	}
}

func TestVPNDiagnosticsMissingFieldsStayUnknown(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	diagnosticsFixture(t, root, "xray.metrics.json", `{"version":1,"sampledAt":"2026-10-22T10:00:00Z","running":false,"network":{},"tcp":{"outSegments":0},"cgroup":{}}`)
	result := readVPNDiagnostics(context.Background(), filepath.Join(root, "xray.json"), now)
	if result.Status != "ok" || result.Running == nil || *result.Running || result.Network != nil || result.TCP != nil || result.Cgroup.MemoryBytes != nil {
		t.Fatalf("omitted counters must not turn into measured zeros: %+v", result)
	}
}

func TestServerDiagnosticsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := collectServerDiagnosticsAt(ctx, "/nonexistent/xray.json", diagnosticsPaths{"/proc", "/sys/fs/cgroup"}, time.Now())
	if result.System.CPU != nil || result.System.MemoryTotalBytes != nil || result.API.CPUUsageUsec != nil || result.VPN.Status != "unavailable" {
		t.Fatalf("cancelled request must not continue sampling: %+v", result)
	}
}

func TestServerDiagnosticsNeverSubstitutesParentCgroup(t *testing.T) {
	root := t.TempDir()
	paths := diagnosticsPaths{filepath.Join(root, "proc"), filepath.Join(root, "cgroup")}
	for path, value := range map[string]string{
		"cgroup/cpu.max": "100000 100000", "cgroup/memory.current": "999999",
		"cgroup/cpu.stat":                     "usage_usec 7777\n",
		"cgroup/memory/memory.usage_in_bytes": "888888",
		"cgroup/cpu/cpu.cfs_period_us":        "100000", "cgroup/cpu/cpu.cfs_quota_us": "50000",
	} {
		diagnosticsFixture(t, root, path, value)
	}
	for _, group := range []string{"0::/missing\n", "2:memory:/missing\n3:cpu,cpuacct:/missing\n", "0::/../../host\n", ""} {
		diagnosticsFixture(t, root, "proc/self/cgroup", group)
		result := collectServerDiagnosticsAt(context.Background(), "", paths, time.Now())
		if result.API.MemoryBytes != nil || result.API.CPUUsageUsec != nil || result.API.CPUQuotaCores != nil {
			t.Fatalf("unavailable own group %q must not use parent's counters: %+v", group, result.API)
		}
	}
}
