package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Счётчики относятся к доступным из контейнера пространствам имён. Они не дают
// доступа к физическому хосту и не доказывают ограничение скорости провайдером.
type resourceDiagnostics struct {
	MemoryBytes         *uint64  `json:"memoryBytes"`
	MemoryLimitBytes    *uint64  `json:"memoryLimitBytes"`
	CPUQuotaCores       *float64 `json:"cpuQuotaCores"`
	CPUUsageUsec        *uint64  `json:"cpuUsageUsec"`
	CPUThrottledUsec    *uint64  `json:"cpuThrottledUsec"`
	CPUThrottledPeriods *uint64  `json:"cpuThrottledPeriods"`
}

type cpuDiagnostics struct {
	TotalTicks uint64 `json:"totalTicks"`
	StealTicks uint64 `json:"stealTicks"`
}

type networkDiagnostics struct {
	RXBytes uint64 `json:"rxBytes"`
	TXBytes uint64 `json:"txBytes"`
}

type tcpDiagnostics struct {
	OutSegments           uint64 `json:"outSegments"`
	RetransmittedSegments uint64 `json:"retransmittedSegments"`
}

type vpnDiagnostics struct {
	Status    string               `json:"status"`
	Detail    string               `json:"detail"`
	Scope     string               `json:"scope"`
	SampledAt string               `json:"sampledAt,omitempty"`
	Running   *bool                `json:"running"`
	Cgroup    *resourceDiagnostics `json:"cgroup"`
	Network   *networkDiagnostics  `json:"network"`
	TCP       *tcpDiagnostics      `json:"tcp"`
}

type serverDiagnostics struct {
	SampledAt string `json:"sampledAt"`
	Runtime   struct {
		OS         string `json:"os"`
		Arch       string `json:"arch"`
		GoVersion  string `json:"goVersion"`
		CPUCount   int    `json:"cpuCount"`
		Goroutines int    `json:"goroutines"`
		Version    string `json:"version,omitempty"`
		Revision   string `json:"revision,omitempty"`
	} `json:"runtime"`
	System struct {
		Scope                string          `json:"scope"`
		UptimeSeconds        *float64        `json:"uptimeSeconds"`
		LoadAverage          []float64       `json:"loadAverage"`
		MemoryTotalBytes     *uint64         `json:"memoryTotalBytes"`
		MemoryAvailableBytes *uint64         `json:"memoryAvailableBytes"`
		CPU                  *cpuDiagnostics `json:"cpu"`
	} `json:"system"`
	API struct {
		Scope string `json:"scope"`
		resourceDiagnostics
	} `json:"api"`
	VPN vpnDiagnostics `json:"vpn"`
}

type diagnosticsPaths struct{ proc, cgroup string }

func collectServerDiagnostics(ctx context.Context, configPath string) serverDiagnostics {
	return collectServerDiagnosticsAt(ctx, configPath, diagnosticsPaths{"/proc", "/sys/fs/cgroup"}, time.Now().UTC())
}

func collectServerDiagnosticsAt(ctx context.Context, configPath string, paths diagnosticsPaths, now time.Time) serverDiagnostics {
	var result serverDiagnostics
	result.SampledAt = now.Format(time.RFC3339Nano)
	result.Runtime.OS, result.Runtime.Arch, result.Runtime.GoVersion = runtime.GOOS, runtime.GOARCH, runtime.Version()
	result.Runtime.CPUCount, result.Runtime.Goroutines = runtime.NumCPU(), runtime.NumGoroutine()
	if build, ok := debug.ReadBuildInfo(); ok {
		if build.Main.Version != "(devel)" {
			result.Runtime.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				result.Runtime.Revision = setting.Value
			}
		}
	}
	result.System.Scope = "Система или виртуальная машина, видимая API через /proc; это не отдельные лимиты VPN и не физический хост."
	result.API.Scope = "Группа ресурсов API (cgroup); может включать другие процессы этой группы."
	read := func(path string) string { return diagnosticsRead(ctx, path) }
	if fields := strings.Fields(read(filepath.Join(paths.proc, "uptime"))); len(fields) > 0 {
		result.System.UptimeSeconds = diagnosticsFloat(fields[0])
	}
	if fields := strings.Fields(read(filepath.Join(paths.proc, "loadavg"))); len(fields) >= 3 {
		values := make([]float64, 0, 3)
		for _, value := range fields[:3] {
			if parsed := diagnosticsFloat(value); parsed != nil {
				values = append(values, *parsed)
			}
		}
		if len(values) == 3 {
			result.System.LoadAverage = values
		}
	}
	for line := range strings.SplitSeq(read(filepath.Join(paths.proc, "meminfo")), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		value := diagnosticsUint(fields[1])
		if value == nil || *value > math.MaxUint64/1024 {
			continue
		}
		bytes := *value * 1024
		switch fields[0] {
		case "MemTotal:":
			result.System.MemoryTotalBytes = &bytes
		case "MemAvailable:":
			result.System.MemoryAvailableBytes = &bytes
		}
	}
	result.System.CPU = diagnosticsCPU(read(filepath.Join(paths.proc, "stat")))
	result.API.resourceDiagnostics = diagnosticsCgroup(read, paths)
	result.VPN = readVPNDiagnostics(ctx, configPath, now)
	return result
}

// Размер каждого источника ограничен: повреждённый общий файл не раздувает ответ.
func diagnosticsRead(ctx context.Context, path string) string {
	if ctx.Err() != nil {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 || ctx.Err() != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func diagnosticsUint(value string) *uint64 {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func diagnosticsFloat(value string) *float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil
	}
	return &parsed
}

func diagnosticsCPU(data string) *cpuDiagnostics {
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] != "cpu" {
			continue
		}
		var result cpuDiagnostics
		// guest и guest_nice уже включены в user/nice; повторно их не считаем.
		for index, value := range fields[1:9] {
			parsed := diagnosticsUint(value)
			if parsed == nil || *parsed > math.MaxUint64-result.TotalTicks {
				return nil
			}
			result.TotalTicks += *parsed
			if index == 7 {
				result.StealTicks = *parsed
			}
		}
		return &result
	}
	return nil
}

func diagnosticsPairs(data string) map[string]string {
	result := make(map[string]string)
	for line := range strings.SplitSeq(data, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			result[fields[0]] = fields[1]
		}
	}
	return result
}

func diagnosticsCgroup(read func(string) string, paths diagnosticsPaths) resourceDiagnostics {
	var result resourceDiagnostics
	// В контейнере с приватным cgroup namespace собственный путь обычно равен /.
	// Если указан подкаталог, родительские счётчики вместо него не подставляем.
	groups := make(map[string]string)
	for line := range strings.SplitSeq(read(filepath.Join(paths.proc, "self/cgroup")), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || !strings.HasPrefix(parts[2], "/") || strings.Contains(parts[2], "..") {
			continue
		}
		for controller := range strings.SplitSeq(parts[1], ",") {
			groups[controller] = strings.TrimPrefix(parts[2], "/")
		}
	}
	if relative, unified := groups[""]; unified {
		root := filepath.Join(paths.cgroup, relative)
		max := read(filepath.Join(root, "cpu.max"))
		result.MemoryBytes = diagnosticsUint(read(filepath.Join(root, "memory.current")))
		result.MemoryLimitBytes = diagnosticsUint(read(filepath.Join(root, "memory.max")))
		if fields := strings.Fields(max); len(fields) == 2 {
			quota, period := diagnosticsFloat(fields[0]), diagnosticsFloat(fields[1])
			if quota != nil && period != nil && *quota > 0 && *period > 0 {
				cores := *quota / *period
				result.CPUQuotaCores = &cores
			}
		}
		cpu := diagnosticsPairs(read(filepath.Join(root, "cpu.stat")))
		result.CPUUsageUsec = diagnosticsUint(cpu["usage_usec"])
		result.CPUThrottledUsec = diagnosticsUint(cpu["throttled_usec"])
		result.CPUThrottledPeriods = diagnosticsUint(cpu["nr_throttled"])
		return result
	}
	// Старые установки cgroup v1: контроллеры могут быть смонтированы совместно.
	controllerRoot := func(controller, probe string) string {
		relative, known := groups[controller]
		if !known {
			return ""
		}
		for _, mount := range []string{controller, "cpu,cpuacct", "cpuacct,cpu"} {
			base := filepath.Join(paths.cgroup, mount, relative)
			if read(filepath.Join(base, probe)) != "" {
				return base
			}
		}
		return ""
	}
	controllerRead := func(root, name string) string {
		if root == "" {
			return ""
		}
		return read(filepath.Join(root, name))
	}
	memory := controllerRoot("memory", "memory.usage_in_bytes")
	result.MemoryBytes = diagnosticsUint(controllerRead(memory, "memory.usage_in_bytes"))
	result.MemoryLimitBytes = diagnosticsUint(controllerRead(memory, "memory.limit_in_bytes"))
	if result.MemoryLimitBytes != nil && *result.MemoryLimitBytes >= 1<<60 {
		result.MemoryLimitBytes = nil
	}
	cpu := controllerRoot("cpu", "cpu.cfs_period_us")
	quota, period := diagnosticsFloat(controllerRead(cpu, "cpu.cfs_quota_us")), diagnosticsFloat(controllerRead(cpu, "cpu.cfs_period_us"))
	if quota != nil && period != nil && *quota > 0 && *period > 0 {
		cores := *quota / *period
		result.CPUQuotaCores = &cores
	}
	stats := diagnosticsPairs(controllerRead(cpu, "cpu.stat"))
	result.CPUThrottledPeriods = diagnosticsUint(stats["nr_throttled"])
	if nanos := diagnosticsUint(stats["throttled_time"]); nanos != nil {
		usec := *nanos / 1000
		result.CPUThrottledUsec = &usec
	}
	accounting := controllerRoot("cpuacct", "cpuacct.usage")
	if nanos := diagnosticsUint(controllerRead(accounting, "cpuacct.usage")); nanos != nil {
		usec := *nanos / 1000
		result.CPUUsageUsec = &usec
	}
	return result
}

func readVPNDiagnostics(ctx context.Context, configPath string, now time.Time) vpnDiagnostics {
	result := vpnDiagnostics{
		Status: "unavailable", Detail: "Телеметрия VPN в этом запуске недоступна.",
		Scope: "Контейнер Xray: его cgroup и сетевое пространство имён; счётчики всех его соединений, кроме loopback.",
	}
	if configPath == "" || ctx.Err() != nil {
		return result
	}
	data := diagnosticsRead(ctx, filepath.Join(filepath.Dir(configPath), "xray.metrics.json"))
	if data == "" {
		return result
	}
	var snapshot struct {
		Version   int                  `json:"version"`
		SampledAt string               `json:"sampledAt"`
		Running   *bool                `json:"running"`
		Cgroup    *resourceDiagnostics `json:"cgroup"`
		Network   *struct {
			RXBytes *uint64 `json:"rxBytes"`
			TXBytes *uint64 `json:"txBytes"`
		} `json:"network"`
		TCP *struct {
			OutSegments           *uint64 `json:"outSegments"`
			RetransmittedSegments *uint64 `json:"retransmittedSegments"`
		} `json:"tcp"`
	}
	if json.Unmarshal([]byte(data), &snapshot) != nil || snapshot.Version != 1 || snapshot.Running == nil {
		result.Status, result.Detail = "invalid", "Снимок телеметрии VPN повреждён или имеет неизвестный формат."
		return result
	}
	sampled, err := time.Parse(time.RFC3339Nano, snapshot.SampledAt)
	if err != nil || sampled.After(now.Add(5*time.Second)) {
		result.Status, result.Detail = "invalid", "В снимке телеметрии VPN недопустимое время."
		return result
	}
	result.SampledAt = snapshot.SampledAt
	if now.Sub(sampled) > 15*time.Second {
		result.Status, result.Detail = "stale", "Снимок VPN старше 15 секунд; текущая нагрузка неизвестна."
		return result
	}
	if snapshot.Cgroup != nil && snapshot.Cgroup.CPUQuotaCores != nil &&
		(*snapshot.Cgroup.CPUQuotaCores <= 0 || math.IsNaN(*snapshot.Cgroup.CPUQuotaCores) || math.IsInf(*snapshot.Cgroup.CPUQuotaCores, 0)) {
		result.Status, result.Detail = "invalid", "В снимке телеметрии VPN недопустимый лимит CPU."
		return result
	}
	result.Status, result.Detail = "ok", "Свежий снимок VPN; изменение счётчиков между двумя замерами показывает нагрузку за этот интервал."
	result.Running, result.Cgroup = snapshot.Running, snapshot.Cgroup
	if snapshot.Network != nil && snapshot.Network.RXBytes != nil && snapshot.Network.TXBytes != nil {
		result.Network = &networkDiagnostics{RXBytes: *snapshot.Network.RXBytes, TXBytes: *snapshot.Network.TXBytes}
	}
	if snapshot.TCP != nil && snapshot.TCP.OutSegments != nil && snapshot.TCP.RetransmittedSegments != nil {
		result.TCP = &tcpDiagnostics{OutSegments: *snapshot.TCP.OutSegments, RetransmittedSegments: *snapshot.TCP.RetransmittedSegments}
	}
	return result
}
