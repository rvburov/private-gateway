import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { AdvancedDiagnostics, diagnosticReport } from "./AdvancedDiagnostics";
import type { CheckRun } from "./Checks";
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

function result(before: ServerSnapshot, after: ServerSnapshot): CheckRun {
  return {
    checkedAt: Date.parse("2026-10-23T10:00:10Z"),
    browser: [{ id: "exit", title: "Выход", status: "ok", detail: "Адрес 203.0.113.7." }],
    browserFacts: { ipv4: { ip: "203.0.113.7", asn: null, isp: null, city: null, countryCode: null, timeZone: null }, ipv6: null, webrtc: null, dns: [], timeZone: "Europe/Moscow" },
    device: { collectedAt: Date.now(), facts: [{ id: "ua", label: "User-Agent", value: "Test browser" }], notes: ["Сведения сообщает браузер."] },
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

  it("exports selected diagnostic fields without unrelated session or profile data", () => {
    const first = snapshot("2026-10-23T10:00:00Z", "2026-10-23T10:00:00Z", 0);
    const run = { ...result(first, first), token: "session-secret", profile: "vless://profile-secret", subscription: "sub-secret" };
    const report = diagnosticReport(run, "https://gateway.example:8443");
    const serialized = JSON.stringify(report);
    expect(serialized).toContain("203.0.113.7");
    expect(serialized).toContain("Test browser");
    expect(serialized).not.toMatch(/session-secret|profile-secret|sub-secret/);
    expect(report.panelOrigin).toBe("https://gateway.example:8443");
  });

  it("starts an explicit local download and lets Safari open the blob before revoking it", () => {
    vi.useFakeTimers();
    const create = vi.fn(() => "blob:diagnostic-report");
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL { static createObjectURL = create; static revokeObjectURL = revoke; });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.isConnected).toBe(true);
      expect(this.download).toMatch(/private-gateway-diagnostics.*\.json$/);
    });
    const first = snapshot("2026-10-23T10:00:00Z", "2026-10-23T10:00:00Z", 0);
    render(<AdvancedDiagnostics result={result(first, first)} busy={false} />);
    expect(create).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Скачать отчёт JSON" }));
    expect(create).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    expect(revoke).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1000);
    expect(revoke).toHaveBeenCalledWith("blob:diagnostic-report");
    expect(document.querySelector("a[download]")).toBeNull();
  });
});
