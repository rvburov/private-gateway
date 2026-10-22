import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { AdvancedDiagnostics } from "./AdvancedDiagnostics";
import type { ServerCheckRun } from "./ServerDiagnostics";
import type { PathMeasurement, ServerSnapshot } from "./performance";

function snapshot(at: string, vpnAt: string, usage: number): ServerSnapshot {
  const cgroup = { memoryBytes: 1024, memoryLimitBytes: 2048, cpuQuotaCores: 1, cpuUsageUsec: usage, cpuThrottledUsec: usage / 10, cpuThrottledPeriods: 1 };
  return {
    sampledAt: at,
    runtime: { os: "linux", arch: "amd64", goVersion: "go-test", cpuCount: 2, goroutines: 2 },
    system: { scope: "Видимая VM.", uptimeSeconds: 1000, loadAverage: [0.1, 0.2, 0.3], memoryTotalBytes: 4096, memoryAvailableBytes: 2048, cpu: null },
    api: { ...cgroup, scope: "API." },
    vpn: { status: "ok", detail: "Свежий снимок.", scope: "VPN.", sampledAt: vpnAt, running: true, cgroup, network: { rxBytes: usage, txBytes: usage * 2 }, tcp: { outSegments: usage, retransmittedSegments: usage / 1000 } },
  };
}

const path = (): PathMeasurement => ({
  latency: { medianMs: 12, p95Ms: 20, jitterMs: 3, attempts: 8, successes: 8, errorCount: 0, error: null },
  download: { mbps: 15, bytes: 1024, durationMs: 1000, error: null },
  upload: { mbps: 3, bytes: 512, durationMs: 1000, error: null },
});

function result(before: ServerSnapshot, after: ServerSnapshot): ServerCheckRun {
  return {
    checkedAt: Date.parse("2026-10-23T10:00:10Z"),
    server: [], serverError: null, performanceError: null,
    performance: { checkedAt: Date.parse("2026-10-23T10:00:10Z"), mode: "wifi-vpn", panel: path(), internet: path(), serverBefore: before, serverAfter: after, serverError: null },
  };
}

function vpnGroup() {
  return within(screen.getByRole("heading", { name: "Контейнер VPN" }).nextElementSibling as HTMLElement);
}

function valueFor(label: string) {
  return vpnGroup().getByText(label).parentElement?.querySelector("dd")?.textContent;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  window.localStorage.clear();
});

describe("extended diagnostics", () => {
  it("does not show zero CPU or traffic for repeated worker snapshots", () => {
    const first = snapshot("2026-10-23T10:00:00Z", "2026-10-23T10:00:00Z", 1_000_000);
    const last = snapshot("2026-10-23T10:00:10Z", "2026-10-23T10:00:00Z", 1_000_000);
    render(<AdvancedDiagnostics result={result(first, last)} busy={false} />);
    expect(valueFor("CPU за время замера")).toBe("Недоступно");
    expect(screen.getByText("Получено / отправлено VPN за замер").parentElement?.querySelector("dd")?.textContent).toBe("Недоступно / Недоступно");
    expect(screen.getByText("Повторные отправки TCP / исходящие сегменты").parentElement?.querySelector("dd")?.textContent).toBe("Недоступно");
  });

  it("calculates VPN CPU against the worker interval and discards reset counters", () => {
    const first = snapshot("2026-10-23T10:00:00Z", "2026-10-23T10:00:00Z", 1_000_000);
    const last = snapshot("2026-10-23T10:00:10Z", "2026-10-23T10:00:02Z", 2_000_000);
    const rendered = render(<AdvancedDiagnostics result={result(first, last)} busy={false} />);
    expect(valueFor("CPU за время замера")).toBe("50 % лимита");
    const reset = snapshot("2026-10-23T10:00:10Z", "2026-10-23T10:00:02Z", 100);
    rendered.rerender(<AdvancedDiagnostics result={result(first, reset)} busy={false} />);
    expect(valueFor("CPU за время замера")).toBe("Недоступно");
    expect(screen.getByText("Повторные отправки TCP / исходящие сегменты").parentElement?.querySelector("dd")?.textContent).toBe("Недоступно");
  });

});
