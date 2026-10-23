import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ServerDiagnostics, serverDiagnosticReport } from "./ServerDiagnostics";
import type { CheckResult } from "./CheckResults";
import { runPerformance, savePerformanceHistory, type PathMeasurement, type PerformanceRun } from "./performance";

vi.mock("./performance", async (importOriginal) => ({
  ...await importOriginal<typeof import("./performance")>(),
  runPerformance: vi.fn(),
}));

const measuredPath = (): PathMeasurement => ({
  latency: { medianMs: 170, p95Ms: 210, jitterMs: 8, attempts: 8, successes: 8, errorCount: 0, error: null },
  download: { mbps: 15, bytes: 1024, durationMs: 1000, error: null },
  upload: { mbps: 3, bytes: 512, durationMs: 1000, error: null },
});
const performanceResult = (): PerformanceRun => ({
  checkedAt: Date.parse("2026-10-23T10:00:00Z"), mode: "current",
  panel: measuredPath(), internet: measuredPath(), serverBefore: null, serverAfter: null, serverError: null,
});
const health: { checks: CheckResult[] } = { checks: [{ id: "traffic", title: "VLESS-трафик", status: "ok", detail: "VLESS работает, выход — 203.0.113.7.", ip: "203.0.113.7" }] };
const clients: QueryClient[] = [];
let fetchMock: ReturnType<typeof vi.fn>;

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  const component = <QueryClientProvider client={client}><ServerDiagnostics token="admin-test-session" /></QueryClientProvider>;
  return { client, component, view: render(component) };
}

beforeEach(() => {
  localStorage.clear();
  vi.mocked(runPerformance).mockReset().mockImplementation(async (_token, mode) => ({ ...performanceResult(), mode }));
  fetchMock = vi.fn().mockImplementation(async () => Response.json(health));
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(async () => {
  cleanup();
  for (const client of clients.splice(0)) {
    await client.cancelQueries();
    client.clear();
  }
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("server diagnostics", () => {
  it("runs only on demand, preserves history and caches the server result independently", async () => {
    savePerformanceHistory({ ...performanceResult(), checkedAt: performanceResult().checkedAt - 60_000 });
    const { client, component, view } = setup();
    const privacyResult = { browser: [], checkedAt: 123 };
    client.setQueryData(["privacy-checks"], privacyResult);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(runPerformance).not.toHaveBeenCalled();
    expect(screen.getByRole("table").querySelectorAll("tbody tr")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Как подключены сейчас"), { target: { value: "mobile-vpn" } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/checks");
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get("Authorization")).toBe("Bearer admin-test-session");
    expect(runPerformance).toHaveBeenCalledWith("admin-test-session", "mobile-vpn", expect.any(AbortSignal), expect.any(Function));
    expect(screen.queryByRole("heading", { name: "Что видят сайты" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Отчёт о сервере" })).toBeTruthy();
    expect(client.getQueryData(["privacy-checks"])).toEqual(privacyResult);
    const saved = localStorage.getItem("private-gateway-performance-v1");
    expect(saved).toContain("mobile-vpn");
    expect(JSON.parse(saved || "[]")).toHaveLength(2);
    expect(saved).not.toMatch(/admin-test-session|203\.0\.113\.7/);
    view.unmount();
    render(component);
    expect(screen.getByRole("button", { name: "Проверить сервер снова" })).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(runPerformance).toHaveBeenCalledTimes(1);
  });

  it("waits for the health check before measuring and stops without saving a partial run", async () => {
    let healthSignal: AbortSignal | undefined;
    fetchMock.mockImplementation((_path, options: RequestInit) => {
      healthSignal = options.signal as AbortSignal;
      return new Promise((_resolve, reject) => healthSignal?.addEventListener("abort", () => reject(healthSignal?.reason), { once: true }));
    });
    const { client } = setup();
    const privacyResult = { browser: [], checkedAt: 123 };
    client.setQueryData(["privacy-checks"], privacyResult);
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Остановить" });
    expect(runPerformance).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка сервера остановлена.");
    expect(healthSignal?.aborted).toBe(true);
    expect(localStorage.getItem("private-gateway-performance-v1")).toBeNull();
    expect(runPerformance).not.toHaveBeenCalled();
    expect(client.getQueryData(["privacy-checks"])).toEqual(privacyResult);
  });

  it("shows completed stages and waits for the measurement result before reaching 100 percent", async () => {
    let reportProgress!: NonNullable<Parameters<typeof runPerformance>[3]>;
    let finishMeasurement!: (result: PerformanceRun) => void;
    vi.mocked(runPerformance).mockImplementation((_token, _mode, _signal, onProgress) => {
      reportProgress = onProgress!;
      onProgress?.("Получение от сервера", 35);
      return new Promise<PerformanceRun>((resolve) => { finishMeasurement = resolve; });
    });
    setup();
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    const progress = await screen.findByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement;
    await waitFor(() => expect(progress.value).toBe(48));
    expect(progress.max).toBe(100);
    expect(screen.getByRole("button", { name: "Остановить" })).toBeTruthy();
    act(() => reportProgress("Замеры завершены", 100));
    expect(progress.value).toBe(99);
    await act(async () => { finishMeasurement(performanceResult()); });
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    expect(progress.value).toBe(100);
  });

  it("preserves stopped progress with cached results and resets a new health check to zero", async () => {
    setup();
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    expect((screen.getByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement).value).toBe(100);
    const completedHistory = localStorage.getItem("private-gateway-performance-v1");
    vi.mocked(runPerformance).mockImplementation((_token, _mode, signal, onProgress) => {
      onProgress?.("Получение от сервера", 35);
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
    });
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер снова" }));
    await waitFor(() => expect((screen.getByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement).value).toBe(48));
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка сервера остановлена.");
    expect((screen.getByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement).value).toBe(48);
    expect(localStorage.getItem("private-gateway-performance-v1")).toBe(completedHistory);
    let healthSignal: AbortSignal | undefined;
    fetchMock.mockImplementation((_path, options: RequestInit) => {
      healthSignal = options.signal as AbortSignal;
      return new Promise((_resolve, reject) => healthSignal?.addEventListener("abort", () => reject(healthSignal?.reason), { once: true }));
    });
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер снова" }));
    await screen.findByRole("button", { name: "Остановить" });
    expect((screen.getByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement).value).toBe(0);
    expect(runPerformance).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка сервера остановлена.");
    expect(healthSignal?.aborted).toBe(true);
    expect((screen.getByRole("progressbar", { name: "Ход проверки сервера" }) as HTMLProgressElement).value).toBe(0);
  });

  it("cancels an in-progress speed measurement when leaving the server section", async () => {
    let measurementSignal: AbortSignal | undefined;
    vi.mocked(runPerformance).mockImplementation((_token, _mode, signal) => {
      measurementSignal = signal;
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
    });
    const { view, client } = setup();
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await waitFor(() => expect(runPerformance).toHaveBeenCalledOnce());
    view.unmount();
    expect(measurementSignal?.aborted).toBe(true);
    expect(client.getQueryData(["server-checks"])).toBeUndefined();
    expect(localStorage.getItem("private-gateway-performance-v1")).toBeNull();
  });

  it("keeps measurements available when the server health check is unavailable", async () => {
    fetchMock.mockResolvedValue(Response.json({ error: "Серверная проверка недоступна." }, { status: 503 }));
    setup();
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    expect(screen.getByText("Серверная проверка недоступна.")).toBeTruthy();
    expect(screen.getByRole("region", { name: "Канал до панели" }).querySelector("dd")?.textContent).toBe("15 Мбит/с");
    expect(localStorage.getItem("private-gateway-performance-v1")).not.toBeNull();
  });

  it("keeps the server health result when the speed measurement fails", async () => {
    vi.mocked(runPerformance).mockRejectedValue(new Error("Замер скорости недоступен."));
    setup();
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    expect(within(screen.getByRole("region", { name: "Работа сервера" })).getByText("VLESS-трафик")).toBeTruthy();
    expect(screen.getByText("Замер скорости недоступен.")).toBeTruthy();
    const summary = screen.getByRole("region", { name: "Проверка сервера" }).querySelector(".check-summary");
    expect(summary?.classList.contains("warning-notice")).toBe(true);
    expect(summary?.classList.contains("success-notice")).toBe(false);
    expect(localStorage.getItem("private-gateway-performance-v1")).toBeNull();
  });

  it("exports server data without browser, device, session or profile fields", () => {
    const run = {
      server: health.checks, serverError: null, performance: performanceResult(), performanceError: null, checkedAt: Date.now(),
      browserFacts: { ip: "client-private-ip" }, device: { userAgent: "private-device-fingerprint" },
      token: "session-secret", profile: "vless://profile-secret", subscription: "subscription-secret",
    };
    const report = serverDiagnosticReport(run, "https://gateway.example:8443");
    expect(report.format).toBe("private-gateway-server-diagnostics");
    expect(report.panelOrigin).toBe("https://gateway.example:8443");
    expect(JSON.stringify(report)).toContain("203.0.113.7");
    expect(JSON.stringify(report)).not.toMatch(/client-private-ip|private-device-fingerprint|session-secret|profile-secret|subscription-secret|browserFacts|device/);
  });
});
