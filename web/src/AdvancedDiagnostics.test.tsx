import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { AdvancedDiagnostics } from "./AdvancedDiagnostics";
import type { ServerCheckRun } from "./ServerDiagnostics";
import { savePerformanceHistory, type PathMeasurement, type PerformanceRun, type ServerSnapshot } from "./performance";

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

function currentRun(mode: PerformanceRun["mode"], checkedAt: number): PerformanceRun {
  const measuredPath = () => {
    const measured = path();
    measured.latency.attempts = 10;
    measured.latency.successes = 10;
    return measured;
  };
  return {
    checkedAt, mode, method: "browser-http-v2", panel: measuredPath(), internet: measuredPath(),
    serverBefore: null, serverAfter: null, serverError: null,
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
  it("labels new HTTP timings separately from legacy full-request timings", () => {
    const measured = currentRun("tcp", Date.parse("2026-10-31T10:00:00Z"));
    const check: ServerCheckRun = { checkedAt: measured.checkedAt, server: [], serverError: null, performance: measured, performanceError: null };
    const view = render(<AdvancedDiagnostics result={check} busy={false} />);
    expect(screen.getAllByText("Время до начала ответа HTTP, медиана")).toHaveLength(2);
    expect(screen.queryByText("Задержка HTTP, медиана")).toBeNull();
    view.rerender(<AdvancedDiagnostics result={{ ...check, performance: { ...measured, method: undefined } }} busy={false} />);
    expect(screen.getAllByText("Задержка HTTP, медиана")).toHaveLength(2);
    expect(screen.queryByText("Время до начала ответа HTTP, медиана")).toBeNull();
  });

  it("shows incomplete download bytes and keeps an unconfirmed upload out of sent totals", () => {
    const measured = currentRun("xhttp", Date.parse("2026-10-31T10:00:00Z"));
    measured.internet.download = {
      mbps: null, bytes: 238 * 1024, durationMs: null, error: "Ответ не получен за 10 секунд.",
      incomplete: { bytes: 238 * 1024, expectedBytes: 1024 * 1024, durationMs: 10_000 },
    };
    measured.internet.upload = {
      mbps: 3, bytes: 512, durationMs: 1000, error: "Ответ не получен за 10 секунд.",
      incomplete: { bytes: 0, expectedBytes: 256 * 1024, durationMs: 10_000 },
    };
    const check: ServerCheckRun = { checkedAt: measured.checkedAt, server: [], serverError: null, performance: measured, performanceError: null };
    render(<AdvancedDiagnostics result={check} busy={false} />);
    const internet = within(screen.getByRole("region", { name: "Интернет через текущее подключение" }));
    expect(internet.getByText("Получение").nextElementSibling?.textContent).toBe("Недоступно");
    expect(internet.getByText("Отправка").nextElementSibling?.textContent).toBe("3 Мбит/с");
    expect(internet.getByText(/Скачивание не завершено: получено 238 КБ из 1 МБ за 10 с/)).toBeTruthy();
    expect(internet.getByText(/Отправка не завершена: объём 256 КБ не подтверждён за 10 с/)).toBeTruthy();
    expect(internet.getByText(/отправка в завершённых пробах — 512 Б/)).toBeTruthy();
  });

  it("compares the latest manually labelled TCP and XHTTP measurements using the same method", () => {
    const first = currentRun("tcp", Date.parse("2026-10-31T10:00:00Z"));
    const xhttp = currentRun("xhttp", first.checkedAt + 60_000);
    xhttp.internet.download.mbps = 30;
    const latest = currentRun("tcp", first.checkedAt + 120_000);
    latest.internet.download.mbps = 22;
    for (const run of [first, latest, xhttp]) savePerformanceHistory(run);
    render(<AdvancedDiagnostics busy={false} />);
    const comparison = within(screen.getByRole("table", { name: "Последние замеры TCP и XHTTP" }));
    expect(comparison.getByRole("rowheader", { name: /TCP \+ Vision/ }).parentElement?.textContent).toContain("22 Мбит/с");
    expect(comparison.getByRole("rowheader", { name: /XHTTP/ }).parentElement?.textContent).toContain("30 Мбит/с");
    expect(comparison.getAllByRole("cell", { name: "12 мс" })).toHaveLength(2);
    expect(comparison.queryByRole("cell", { name: "15 Мбит/с" })).toBeNull();
    expect(screen.getByText(/Профиль выбран вручную/)).toBeTruthy();
    expect(screen.queryByText(/победитель|лучше|быстрее/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Очистить историю" }));
    expect(screen.queryByRole("table", { name: "Последние замеры TCP и XHTTP" })).toBeNull();
  });

  it("does not combine legacy HTTP durations with new response-start timings", () => {
    const tcp = currentRun("tcp", Date.parse("2026-10-31T10:00:00Z"));
    const legacy = { ...currentRun("xhttp", tcp.checkedAt + 60_000), method: undefined };
    savePerformanceHistory(tcp);
    savePerformanceHistory(legacy);
    render(<AdvancedDiagnostics busy={false} />);
    expect(screen.queryByRole("table", { name: "Последние замеры TCP и XHTTP" })).toBeNull();
    expect(screen.getByText("Полный HTTP-запрос")).toBeTruthy();
    expect(screen.getByText("До начала ответа HTTP")).toBeTruthy();
  });

  it("excludes errored and zero-valued metrics from the TCP and XHTTP comparison", () => {
    const tcp = currentRun("tcp", Date.parse("2026-10-31T10:00:00Z"));
    const xhttp = currentRun("xhttp", tcp.checkedAt + 60_000);
    tcp.internet.download.error = "Поздняя проба не завершена.";
    xhttp.internet.upload.mbps = 0;
    tcp.internet.latency.errorCount = 1;
    savePerformanceHistory(tcp);
    savePerformanceHistory(xhttp);
    render(<AdvancedDiagnostics busy={false} />);
    const comparison = within(screen.getByRole("table", { name: "Последние замеры TCP и XHTTP" }));
    expect(comparison.getAllByText("Недостаточно данных")).toHaveLength(6);
    expect(comparison.queryByText(/Мбит\/с|мс/)).toBeNull();
  });

  it.each([null, "Лимит времени проверки исчерпан; остальные замеры не выполнены."])("does not compare a partial three-probe HTTP series even without failed probes (%s)", (error) => {
    const tcp = currentRun("tcp", Date.parse("2026-10-31T10:00:00Z"));
    const xhttp = currentRun("xhttp", tcp.checkedAt + 60_000);
    tcp.internet.latency = { ...tcp.internet.latency, attempts: 3, successes: 3, errorCount: 0, error };
    savePerformanceHistory(tcp);
    savePerformanceHistory(xhttp);
    render(<AdvancedDiagnostics busy={false} />);
    const comparison = within(screen.getByRole("table", { name: "Последние замеры TCP и XHTTP" }));
    expect(comparison.getAllByText("Недостаточно данных")).toHaveLength(2);
    expect(comparison.queryByRole("cell", { name: "12 мс" })).toBeNull();
    expect(comparison.getAllByRole("cell", { name: "15 Мбит/с" })).toHaveLength(2);
    expect(comparison.getAllByRole("cell", { name: "3 Мбит/с" })).toHaveLength(2);
  });

  it("keeps the complete history in one semantic table with labelled mobile values", () => {
    const before = snapshot("2026-10-23T10:00:00Z", "2026-10-23T10:00:00Z", 1_000_000);
    const after = snapshot("2026-10-23T10:00:10Z", "2026-10-23T10:00:10Z", 2_000_000);
    const check = result(before, after);
    savePerformanceHistory(check.performance!);
    render(<AdvancedDiagnostics result={check} busy={false} />);
    const tables = screen.getAllByRole("table");
    expect(tables).toHaveLength(1);
    const entry = tables[0].querySelector("tbody tr") as HTMLElement;
    const cells = within(entry).getAllByRole("cell");
    expect(cells.map((cell) => cell.getAttribute("data-label"))).toEqual([
      "Получение", "Отправка", "Задержка интернета", "Задержка панели", "Ошибки HTTP в интернете",
    ]);
    expect(cells.map((cell) => cell.querySelector(".diagnostics-cell-value")?.textContent)).toEqual([
      "15 Мбит/с", "3 Мбит/с", "12 мс", "12 мс", "0",
    ]);
    expect(within(entry).getByRole("rowheader").textContent).toContain("Wi-Fi с VPN");
    fireEvent.click(screen.getByRole("button", { name: "Очистить историю" }));
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByText("Пока нет сохранённых замеров.")).toBeTruthy();
  });

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
