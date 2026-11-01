import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearPerformanceHistory,
  connectionModes,
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
  const url = new URL(String(input), "https://example.com");
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
    const progress: number[] = [];
    const run = await runPerformance("private-session", "wifi-vpn", new AbortController().signal, (_phase, percent) => progress.push(percent));
    expect(run.mode).toBe("wifi-vpn");
    expect(run.method).toBe("browser-http-v2");
    expect(run.panel.latency).toMatchObject({ attempts: 10, successes: 10, medianMs: 20, jitterMs: 0, errorCount: 0 });
    expect(run.internet.latency.successes).toBe(10);
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes("/performance/ping"))).toHaveLength(12);
    expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("https://speed.cloudflare.com/__down?bytes=0&"))).toHaveLength(12);
    expect(run.panel.download.bytes).toBe(5.3125 * MIB);
    expect(run.panel.upload.bytes).toBe(1.65625 * MIB);
    expect(run.internet.download.bytes).toBe(21 * MIB + 100_000);
    expect(run.internet.upload.bytes).toBe(5.25 * MIB + 100_000);
    expect(run.panel.download.bytes + run.panel.upload.bytes + run.internet.download.bytes + run.internet.upload.bytes).toBeLessThan(40_000_000);
    expect(run.serverBefore).toEqual(snapshot);
    expect(run.serverAfter).toEqual(snapshot);
    expect(run.panel.download.mbps).toBeCloseTo(4 * MIB * 8 / 20_000);
    expect(progress).toEqual([0, 5, 20, 35, 50, 65, 80, 95, 100]);
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

  it("excludes two warmups and measures response headers while still validating delayed bodies", async () => {
    const counts = { panel: 0, internet: 0 };
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      const url = String(input);
      const kind = url.includes("/performance/ping") ? "panel"
        : url.startsWith("https://speed.cloudflare.com/__down?bytes=0&") ? "internet" : null;
      if (!kind) return result;
      if (++counts[kind] <= 2) clock += 500;
      return new Response(new ReadableStream({
        start(controller) {
          setTimeout(() => {
            clock += 80;
            controller.enqueue(new Uint8Array([1]));
            controller.close();
          }, 0);
        },
      }));
    });
    const run = await runPerformance("token", "tcp", new AbortController().signal);
    expect(counts).toEqual({ panel: 12, internet: 12 });
    expect(run.panel.latency).toMatchObject({ attempts: 10, successes: 10, medianMs: 20, p95Ms: 20 });
    expect(run.internet.latency.medianMs).toBe(20);
  });

  it("does not count valid headers followed by a failed body as a successful latency probe", async () => {
    let pings = 0;
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).includes("/performance/ping") && ++pings === 3)
        return new Response(new Uint8Array(4097));
      return result;
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.latency).toMatchObject({ attempts: 10, successes: 9, errorCount: 1 });
    expect(run.panel.latency.error).toContain("больше данных");
  });

  it("measures a slow connection with small complete payloads before attempting larger bodies", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      const url = String(input);
      if (url.includes("/performance/download") || url.includes("/performance/upload")
          || url.includes("/__down?bytes=100000") || url.includes("/__up?")) clock += 1200;
      return result;
    });
    const run = await runPerformance("token", "xhttp", new AbortController().signal);
    expect(run.panel.download).toMatchObject({ bytes: 64 * 1024, durationMs: 1220, error: null });
    expect(run.panel.upload).toMatchObject({ bytes: 32 * 1024, durationMs: 1220, error: null });
    expect(run.internet.download).toMatchObject({ bytes: 100_000, durationMs: 1220, error: null });
    expect(run.internet.upload).toMatchObject({ bytes: 100_000, durationMs: 1220, error: null });
    expect(run.panel.download.mbps).toBeCloseTo(64 * 1024 * 8 / 1_220_000);
    expect(run.internet.download.mbps).toBeLessThan(0.84);
  });

  it.each([false, true])("records a partial download timeout without inventing speed; earlier sample=%s", async (completedFirst) => {
    const realTimeout = AbortSignal.timeout.bind(AbortSignal);
    let operation!: AbortController;
    vi.spyOn(AbortSignal, "timeout").mockImplementation((ms) => {
      if (ms !== 10_000) return realTimeout(ms);
      operation = new AbortController();
      return operation.signal;
    });
    const cancel = vi.fn();
    const failedSize = completedFirst ? 256 * 1024 : 64 * 1024;
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).startsWith(`/api/v1/checks/performance/download?bytes=${failedSize}&`)) {
        const deadline = operation;
        return new Response(new ReadableStream({
          start(controller) {
            controller.enqueue(new Uint8Array(24_000));
            setTimeout(() => {
              clock += 10_000;
              deadline.abort(new DOMException("Request deadline", "TimeoutError"));
            }, 0);
          },
          cancel,
        }));
      }
      return result;
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.download.incomplete).toEqual({ bytes: 24_000, expectedBytes: failedSize, durationMs: 10_020 });
    expect(run.panel.download.bytes).toBe((completedFirst ? 64 * 1024 : 0) + 24_000);
    expect(run.panel.download.error).toContain("Чтение ответа");
    expect(run.panel.download.mbps).toBe(completedFirst ? 64 * 1024 * 8 / 20_000 : null);
    expect(run.panel.download.durationMs).toBe(completedFirst ? 20 : null);
    expect(cancel).toHaveBeenCalledOnce();
    expect(run.panel.upload.error).toBeNull();
    expect(run.internet.download.error).toBeNull();
    expect(run.serverAfter).toEqual(snapshot);
  });

  it("distinguishes a headers timeout and an unconfirmed upload without inferring sent bytes", async () => {
    const realTimeout = AbortSignal.timeout.bind(AbortSignal);
    let operation!: AbortController;
    vi.spyOn(AbortSignal, "timeout").mockImplementation((ms) => {
      if (ms !== 10_000) return realTimeout(ms);
      operation = new AbortController();
      return operation.signal;
    });
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).includes("/performance/download") || String(input).includes("/performance/upload")) {
        const deadline = operation;
        queueMicrotask(() => {
          clock += 10_000;
          deadline.abort(new DOMException("Request deadline", "TimeoutError"));
        });
        return new Promise<Response>(() => {});
      }
      return normalFetch(input, options);
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.download.error).toContain("Заголовки ответа");
    expect(run.panel.upload.error).toContain("подтверждение");
    expect(run.panel.upload.incomplete).toEqual({ bytes: 0, expectedBytes: 32 * 1024, durationMs: 10_000 });
    expect(run.panel.upload.bytes).toBe(0);
    expect(run.panel.upload.mbps).toBeNull();
    expect(run.internet.upload.error).toBeNull();
  });

  it.each([
    { name: "invalid JSON", body: "private-response-content", message: "корректное подтверждение" },
    { name: "wrong byte count", body: JSON.stringify({ bytes: 1 }), message: "другой объём" },
    { name: "oversized acknowledgement", body: "x".repeat(4097), message: "больше данных" },
  ])("rejects $name instead of treating an upload response as confirmation", async ({ body, message }) => {
    fetchMock.mockImplementation(async (input, options) => String(input).includes("/performance/upload")
      ? new Response(body) : normalFetch(input, options));
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.panel.upload.mbps).toBeNull();
    expect(run.panel.upload.bytes).toBe(0);
    expect(run.panel.upload.error).toContain(message);
    expect(run.panel.upload.error).not.toContain("private-response-content");
    expect(run.internet.upload.error).toBeNull();
  });

  it.each([
    { name: "HTML with HTTP 200", body: "<html>private-response-content</html>" },
    { name: "JSON with no serverTime", body: JSON.stringify({ bytes: 100_000 }) },
    { name: "null JSON", body: "null" },
    { name: "array JSON", body: JSON.stringify([{ serverTime: 0 }]) },
    { name: "string serverTime", body: JSON.stringify({ serverTime: "0" }) },
    { name: "null serverTime", body: JSON.stringify({ serverTime: null }) },
    { name: "boolean serverTime", body: JSON.stringify({ serverTime: false }) },
    { name: "negative serverTime", body: JSON.stringify({ serverTime: -1 }) },
    { name: "infinite serverTime", body: '{"serverTime":1e400}' },
    { name: "oversized JSON", body: JSON.stringify({ serverTime: 0, padding: "x".repeat(16 * 1024) }) },
  ])("rejects Cloudflare $name instead of counting its upload as complete", async ({ body }) => {
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).startsWith("https://speed.cloudflare.com/__up?")) {
        clock += 20;
        return new Response(body, { status: 200 });
      }
      return normalFetch(input, options);
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.internet.upload).toMatchObject({ mbps: null, bytes: 0, incomplete: { bytes: 0, expectedBytes: 100_000 } });
    expect(run.internet.upload.error).not.toBeNull();
    expect(run.internet.upload.error).not.toContain("private-response-content");
    expect(run.panel.upload.error).toBeNull();
    expect(run.internet.download.error).toBeNull();
  });

  it("accepts Cloudflare serverTime zero without requiring a numeric byte acknowledgement", async () => {
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.internet.upload).toMatchObject({ error: null, bytes: 5.25 * MIB + 100_000, durationMs: 20 });
    expect(run.internet.upload.mbps).toBeGreaterThan(0);
  });

  it("reports completion only after the final server snapshot settles", async () => {
    let snapshots = 0;
    let finishSnapshot!: (response: Response) => void;
    let snapshotStarted!: () => void;
    const waitingForSnapshot = new Promise<void>((resolve) => { snapshotStarted = resolve; });
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).endsWith("/server") && ++snapshots === 2) {
        return new Promise<Response>((resolve) => {
          finishSnapshot = resolve;
          snapshotStarted();
        });
      }
      return normalFetch(input, options);
    });
    const progress: number[] = [];
    const pending = runPerformance("token", "current", new AbortController().signal, (_phase, percent) => progress.push(percent));
    await waitingForSnapshot;
    expect(progress.at(-1)).toBe(95);
    expect(progress).not.toContain(100);
    finishSnapshot(json(snapshot));
    const result = await pending;
    expect(result.serverAfter).toEqual(snapshot);
    expect(progress.at(-1)).toBe(100);
    expect(progress.filter((percent) => percent === 100)).toHaveLength(1);
  });

  it("does not report completion when cancelled during the final server snapshot", async () => {
    const controller = new AbortController();
    let snapshots = 0;
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).endsWith("/server") && ++snapshots === 2) {
        queueMicrotask(() => controller.abort());
        return new Promise<Response>(() => {});
      }
      return normalFetch(input, options);
    });
    const progress: number[] = [];
    await expect(runPerformance("token", "current", controller.signal, (_phase, percent) => progress.push(percent))).rejects.toMatchObject({ name: "AbortError" });
    expect(progress.at(-1)).toBe(95);
    expect(progress).not.toContain(100);
  });

  it("stops increasing payloads once a useful sample lasts one second", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).startsWith("https://speed.cloudflare.com") && (String(input).includes("bytes=100000") || options?.method === "POST")) clock += 1000;
      return result;
    });
    const run = await runPerformance("token", "current", new AbortController().signal);
    expect(run.internet.download.bytes).toBe(100_000);
    expect(run.internet.upload.bytes).toBe(100_000);
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
    expect(run.internet.download.bytes).toBe(MIB + 100_000);
    expect(run.internet.download.error).toContain("503");
    expect(run.internet.upload.error).toBeNull();
  });

  it("rejects oversized and incomplete data instead of reporting false speed", async () => {
    const cancel = vi.fn();
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).includes("/performance/download")) {
        return new Response(new ReadableStream({ cancel }), { headers: { "Content-Length": String(64 * 1024 + 1) } });
      }
      if (String(input).startsWith("https://speed.cloudflare.com/__down?bytes=100000")) return new Response(new Uint8Array(5));
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
    const progress: number[] = [];
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).includes("/performance/download")) {
        const response = new Response(new ReadableStream({ cancel }));
        setTimeout(() => controller.abort(), 0);
        return response;
      }
      return normalFetch(input, options);
    });
    await expect(runPerformance("token", "current", controller.signal, (_phase, percent) => progress.push(percent))).rejects.toMatchObject({ name: "AbortError" });
    expect(cancel).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/performance/upload"))).toBe(false);
    expect(progress).toEqual([0, 5, 20]);
    expect(loadPerformanceHistory()).toEqual([]);
  });

  it("keeps panel speed and stops probing an unreachable external service early", async () => {
    fetchMock.mockImplementation(async (input, options) => {
      if (String(input).startsWith("https://speed.cloudflare.com")) throw new TypeError("Blocked");
      return normalFetch(input, options);
    });
    const progress: number[] = [];
    const result = await runPerformance("token", "current", new AbortController().signal, (_phase, percent) => progress.push(percent));
    expect(result.panel.download.mbps).not.toBeNull();
    expect(result.panel.upload.mbps).not.toBeNull();
    expect(result.internet.latency).toMatchObject({ attempts: 0, successes: 0, errorCount: 0 });
    expect(result.internet.download.mbps).toBeNull();
    expect(result.internet.upload.mbps).toBeNull();
    expect(fetchMock.mock.calls.filter(([input]) => String(input).startsWith("https://speed.cloudflare.com"))).toHaveLength(2);
    expect(result.serverAfter).toEqual(snapshot);
    expect(progress).toEqual([0, 5, 20, 35, 50, 65, 80, 95, 100]);
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
    expect(result.panel.latency).toMatchObject({ successes: 10, errorCount: 0, error: null });
  });

  it("returns partial results when the overall time budget expires", async () => {
    vi.useFakeTimers();
    const realTimeout = AbortSignal.timeout.bind(AbortSignal);
    const total = new AbortController();
    vi.spyOn(AbortSignal, "timeout").mockImplementation((ms) => ms === 60_000 ? total.signal : realTimeout(ms));
    fetchMock.mockImplementation(async (input, options) => {
      const result = normalFetch(input, options);
      if (String(input).includes("/performance/download")) queueMicrotask(() => total.abort(new DOMException("Time expired", "TimeoutError")));
      return result;
    });
    const progress: number[] = [];
    const run = await runPerformance("token", "current", new AbortController().signal, (_phase, percent) => progress.push(percent));
    expect(run.panel.latency.successes).toBe(10);
    expect(run.internet.upload.mbps).toBeNull();
    expect(run.internet.upload.error).toContain("Лимит времени");
    expect(run.serverAfter).toBeNull();
    expect(progress).toEqual([0, 5, 20, 35, 50, 65, 80, 95, 100]);
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
    expect(Object.keys(stored[0])).toEqual(["checkedAt", "mode", "method", "panel", "internet"]);
    expect(stored[0].panel).toMatchObject({ latencyError: false, downloadError: false, uploadError: false });
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

  it("retains legacy history but rejects unknown methods and invalid transfer error flags", async () => {
    const result = await run();
    const current = savePerformanceHistory(result)[0];
    const { method: _method, ...legacy } = current;
    delete legacy.panel.latencyError;
    delete legacy.panel.downloadError;
    delete legacy.panel.uploadError;
    delete legacy.internet.downloadError;
    delete legacy.internet.uploadError;
    delete legacy.internet.latencyError;
    const invalidMethod = { ...current, method: "future-experiment" };
    const invalidFlag = { ...current, panel: { ...current.panel, downloadError: "false" } };
    const xhttp = { ...current, mode: "xhttp", method: "browser-http-v2", token: "private-token", incomplete: { address: "203.0.113.7" } };
    const tcp = { ...current, mode: "tcp" };
    localStorage.setItem("private-gateway-performance-v1", JSON.stringify([legacy, invalidMethod, invalidFlag, xhttp, tcp]));
    const stored = loadPerformanceHistory();
    expect(stored).toHaveLength(3);
    expect(stored[0].method).toBeUndefined();
    expect(stored[1]).toMatchObject({ mode: "xhttp", method: "browser-http-v2" });
    expect(stored[2].mode).toBe("tcp");
    expect(JSON.stringify(stored)).not.toContain("private-token");
    expect(JSON.stringify(stored)).not.toContain("203.0.113.7");
    expect(connectionModes).toEqual(expect.arrayContaining([
      { value: "tcp", label: "TCP + Vision" }, { value: "xhttp", label: "XHTTP" },
    ]));
  });

  it("keeps completed speed and its later error flag distinct in the numeric history", async () => {
    const result = await run();
    result.panel.download.error = "Поздняя проба не завершилась.";
    result.panel.download.incomplete = { bytes: 240, expectedBytes: 1024, durationMs: 10_000 };
    const stored = savePerformanceHistory(result)[0];
    expect(stored.panel.downloadMbps).toBe(result.panel.download.mbps);
    expect(stored.panel.downloadError).toBe(true);
    expect(stored.panel.uploadError).toBe(false);
    expect(JSON.stringify(stored)).not.toContain("incomplete");
    expect(JSON.stringify(stored)).not.toContain("Поздняя");
  });

  it("marks unfinished successful latency samples as unsuitable for comparison", async () => {
    const result = await run();
    result.panel.latency = { ...latencyFromSamples([10, 20, 30]), error: "Лимит времени проверки исчерпан." };
    const stored = savePerformanceHistory(result)[0];
    expect(stored.panel).toMatchObject({ latencyMs: 20, errorCount: 0, latencyError: true });
    expect(stored.internet.latencyError).toBe(false);
    const invalid = { ...stored, panel: { ...stored.panel, latencyError: "false" } };
    localStorage.setItem("private-gateway-performance-v1", JSON.stringify([invalid, stored]));
    expect(loadPerformanceHistory()).toEqual([stored]);
  });

  it("keeps results usable when browser storage is blocked or full", async () => {
    const result = await run();
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new DOMException("Denied", "SecurityError"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("Full", "QuotaExceededError"); });
    expect(loadPerformanceHistory()).toEqual([]);
    expect(savePerformanceHistory(result)).toHaveLength(1);
  });
});
