import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearPerformanceHistory,
  latencyFromSamples,
  loadPerformanceHistory,
  runPerformance,
  savePerformanceHistory,
  type PerformanceRun,
  type ServerSnapshot,
} from "./performance";

const MIB = 1024 * 1024;
const snapshot: ServerSnapshot = {
  sampledAt: "2026-10-22T10:00:00Z",
  runtime: { os: "linux", arch: "amd64", goVersion: "go1.26", cpuCount: 2, goroutines: 4 },
  system: { scope: "vm", uptimeSeconds: 100, loadAverage: [0.1, 0.1, 0.1], memoryTotalBytes: 1024, memoryAvailableBytes: 512, cpu: { totalTicks: 1000, stealTicks: 0 } },
  api: { scope: "api", memoryBytes: 100, memoryLimitBytes: null, cpuQuotaCores: null, cpuUsageUsec: 100, cpuThrottledUsec: 0, cpuThrottledPeriods: 0 },
  vpn: { status: "unavailable", detail: "no sample", scope: "vpn", running: null, cgroup: null, network: null, tcp: null },
};

const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
let clock = 0;
let fetchMock: ReturnType<typeof vi.fn>;

function normalFetch(input: RequestInfo | URL, options?: RequestInit): Response {
  const url = new URL(String(input), "https://gateway.example");
  clock += 20;
  if (url.pathname.endsWith("/server")) return json(snapshot);
  if (url.pathname.endsWith("/ping")) return json({ ok: true });
  if (options?.method === "POST") {
    const bytes = (options.body as ArrayBuffer).byteLength;
    return url.hostname === "speed.cloudflare.com" ? json({ serverTime: 0 }) : json({ bytes });
  }
  return new Response(new Uint8Array(Number(url.searchParams.get("bytes"))));
}

beforeEach(() => {
  localStorage.clear();
  clock = 0;
  vi.spyOn(performance, "now").mockImplementation(() => clock);
  fetchMock = vi.fn().mockImplementation(async (input, options) => normalFetch(input, options));
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("bounded performance checks", () => {
  it("calculates HTTP jitter only between consecutive successful samples", () => {
    expect(latencyFromSamples([10, 20, null, 100, 140])).toEqual({
      medianMs: 60, p95Ms: 140, jitterMs: 25,
      attempts: 5, successes: 4, errorCount: 1, error: null,
    });
    expect(latencyFromSamples([null, null])).toMatchObject({ medianMs: null, p95Ms: null, jitterMs: null, errorCount: 2 });
    expect(latencyFromSamples([0])).toMatchObject({ medianMs: 0, jitterMs: null });
  });

  it("keeps panel credentials on same origin and caps adaptive payloads", async () => {
    const progress: string[] = [];
    const run = await runPerformance("private-session", "wifi-vpn", new AbortController().signal, (value) => progress.push(value));
    expect(run.mode).toBe("wifi-vpn");
    expect(run.panel.latency).toMatchObject({ attempts: 8, successes: 8, medianMs: 20, jitterMs: 0, errorCount: 0 });
    expect(run.internet.latency.successes).toBe(8);
    expect(run.panel.download.bytes).toBe(4 * MIB);
    expect(run.panel.upload.bytes).toBe(MIB);
    expect(run.internet.download.bytes).toBe(21 * MIB);
    expect(run.internet.upload.bytes).toBe(5.25 * MIB);
    expect(run.serverBefore).toEqual(snapshot);
    expect(run.serverAfter).toEqual(snapshot);
    expect(run.panel.download.mbps).toBeCloseTo(4 * MIB * 8 / 20_000);
    expect(progress.length).toBe(8);
    for (const [input, options] of fetchMock.mock.calls as [string, RequestInit][]) {
      if (input.startsWith("https://")) {
        expect(new URL(input).origin).toBe("https://speed.cloudflare.com");
        expect(options.credentials).toBe("omit");
        expect(options.referrerPolicy).toBe("no-referrer");
        expect(options.headers).toBeUndefined();
        expect(JSON.stringify(options)).not.toContain("private-session");
      } else {
        expect(input.startsWith("/api/v1/checks/performance/")).toBe(true);
        expect(options.credentials).toBe("same-origin");
        expect(new Headers(options.headers).get("Authorization")).toBe("Bearer private-session");
      }
    }
  });

  it("stops increasing payloads once a useful sample lasts one second", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).startsWith("https://speed.cloudflare.com") && (String(input).includes("bytes=1048576") || options?.method === "POST")) clock += 1000;
      return result;
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.internet.download.bytes).toBe(MIB);
    expect(run.internet.upload.bytes).toBe(MIB / 4);
    expect(run.internet.download.durationMs).toBe(1020);
  });

  it("preserves working measurements when one external transfer fails", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).startsWith("https://speed.cloudflare.com/__down?bytes=4194304")) return new Response("unavailable", { status: 503 });
      return normalFetch(input, options);
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.download.error).toBeNull();
    expect(run.internet.download.mbps).not.toBeNull();
    expect(run.internet.download.bytes).toBe(MIB);
    expect(run.internet.download.error).toContain("503");
    expect(run.internet.upload.error).toBeNull();
  });

  it("rejects oversized and incomplete data instead of reporting false speed", async () => {
    const cancel = vi.fn();
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).includes("/performance/download")) {
        return new Response(new ReadableStream({ cancel }), { headers: { "Content-Length": String(4 * MIB + 1) } });
      }
      if (String(input).startsWith("https://speed.cloudflare.com/__down?bytes=1048576")) return new Response(new Uint8Array(5));
      return normalFetch(input, options);
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.download.mbps).toBeNull();
    expect(run.panel.download.error).toContain("больше данных");
    expect(cancel).toHaveBeenCalledOnce();
    expect(run.internet.download.mbps).toBeNull();
    expect(run.internet.download.error).toContain("неполные данные");
    expect(run.internet.download.bytes).toBe(5);
  });

  it("cancels a hanging response body and does not continue after user cancellation", async () => {
    const controller = new AbortController();
    const cancel = vi.fn();
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).includes("/performance/download")) {
        const response = new Response(new ReadableStream({ cancel }));
        setTimeout(() => controller.abort(), 0);
        return response;
      }
      return normalFetch(input, options);
    });
    await expect(runPerformance("token", "current", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(cancel).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/performance/upload"))).toBe(false);
    expect(loadPerformanceHistory()).toEqual([]);
  });

  it("keeps panel speed and stops probing an unreachable external service early", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).startsWith("https://speed.cloudflare.com")) throw new TypeError("Blocked");
      return normalFetch(input, options);
    });
    const result = await runPerformance("token", "current", new AbortController().signal);
    expect(result.panel.download.mbps).not.toBeNull();
    expect(result.panel.upload.mbps).not.toBeNull();
    expect(result.internet.latency).toMatchObject({ attempts: 1, successes: 0, errorCount: 1 });
    expect(result.internet.download.mbps).toBeNull();
    expect(result.internet.upload.mbps).toBeNull();
    expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("https://speed.cloudflare.com"))).toHaveLength(2);
    expect(result.serverAfter).toEqual(snapshot);
  });

  it("does not report a failed warmup as failure of all successful measured probes", async () => {
    let firstPing = true;
    fetchMock.mockImplementation(async (input, options) => {
      if (firstPing && String(input).includes("/performance/ping")) {
        firstPing = false;
        throw new TypeError("A stale connection was closed");
      }
      return normalFetch(input, options);
    });
    const result = await runPerformance("token", "current", new AbortController().signal);
    expect(result.panel.latency).toMatchObject({ successes: 8, errorCount: 0, error: null });
  });

  it("returns partial results when the overall time budget expires", async () => {
    vi.useFakeTimers();
    const realTimeout = AbortSignal.timeout.bind(AbortSignal);
    const total = new AbortController();
    vi.spyOn(AbortSignal, "timeout").mockImplementation((ms) => ms === 45_000 ? total.signal : realTimeout(ms));
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).includes("/performance/download")) queueMicrotask(() => total.abort(new DOMException("Time expired", "TimeoutError")));
      return result;
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.latency.successes).toBe(8);
    expect(run.internet.upload.mbps).toBeNull();
    expect(run.internet.upload.error).toContain("Лимит времени");
    expect(run.serverAfter).toBeNull();
    vi.useRealTimers();
  });
});

describe("local numeric measurement history", () => {
  async function run(): Promise<PerformanceRun> {
    return runPerformance("secret-session", "wifi-vpn", new AbortController().signal);
  }

  it("stores only a bounded numeric summary without server, IP or browser data", async () => {
    const result = await run();
    for (let index = 0; index < 15; index++) savePerformanceHistory({ ...result, checkedAt: 1000 + index });
    const stored = loadPerformanceHistory();
    expect(stored).toHaveLength(12);
    expect(stored[0].checkedAt).toBe(1014);
    const serialized = localStorage.getItem("private-gateway-performance-v1")!;
    expect(serialized).not.toContain("secret-session");
    expect(serialized).not.toContain("sampledAt");
    expect(serialized).not.toContain("runtime");
    expect(Object.keys(stored[0])).toEqual(["checkedAt", "mode", "panel", "internet"]);
    clearPerformanceHistory();
    expect(loadPerformanceHistory()).toEqual([]);
  });

  it("rejects invalid values and strips unexpected fields from existing storage", async () => {
    const result = await run();
    const valid = savePerformanceHistory(result)[0];
    const invalid = { ...valid, panel: { ...valid.panel, downloadMbps: "wrong" } };
    localStorage.setItem("private-gateway-performance-v1", JSON.stringify([invalid, { ...valid, token: "old-secret", ip: "203.0.113.7" }]));
    expect(loadPerformanceHistory()).toEqual([valid]);
    localStorage.setItem("private-gateway-performance-v1", "{broken");
    expect(loadPerformanceHistory()).toEqual([]);
    localStorage.setItem("private-gateway-performance-v1", JSON.stringify([{ ...valid, mode: "autodetected-wifi" }]));
    expect(loadPerformanceHistory()).toEqual([]);
  });

  it("keeps results usable when browser storage is blocked or full", async () => {
    const result = await run();
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new DOMException("Denied", "SecurityError"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("Full", "QuotaExceededError"); });
    expect(loadPerformanceHistory()).toEqual([]);
    expect(savePerformanceHistory(result)).toHaveLength(1);
  });
});
