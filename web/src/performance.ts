import { request } from "./api";

export type ConnectionMode = "current" | "wifi-direct" | "wifi-vpn" | "mobile-direct" | "mobile-vpn";

export const connectionModes: { value: ConnectionMode; label: string }[] = [
  { value: "current", label: "Текущее подключение" },
  { value: "wifi-direct", label: "Wi-Fi без VPN" },
  { value: "wifi-vpn", label: "Wi-Fi с VPN" },
  { value: "mobile-direct", label: "Мобильная сеть без VPN" },
  { value: "mobile-vpn", label: "Мобильная сеть с VPN" },
];

export interface LatencyMeasurement {
  medianMs: number | null;
  p95Ms: number | null;
  jitterMs: number | null;
  attempts: number;
  successes: number;
  errorCount: number;
  error: string | null;
}

export interface TransferMeasurement {
  mbps: number | null;
  bytes: number;
  durationMs: number | null;
  error: string | null;
}

export interface PathMeasurement {
  latency: LatencyMeasurement;
  download: TransferMeasurement;
  upload: TransferMeasurement;
}

export interface CgroupSnapshot {
  memoryBytes: number | null;
  memoryLimitBytes: number | null;
  cpuQuotaCores: number | null;
  cpuUsageUsec: number | null;
  cpuThrottledUsec: number | null;
  cpuThrottledPeriods: number | null;
}

export interface ServerSnapshot {
  sampledAt: string;
  runtime: {
    os: string;
    arch: string;
    goVersion: string;
    cpuCount: number;
    goroutines: number;
    version?: string;
    revision?: string;
  };
  system: {
    scope: string;
    uptimeSeconds: number | null;
    loadAverage: number[] | null;
    memoryTotalBytes: number | null;
    memoryAvailableBytes: number | null;
    cpu: { totalTicks: number; stealTicks: number } | null;
  };
  api: CgroupSnapshot & { scope: string };
  vpn: {
    status: "ok" | "unavailable" | "stale" | "invalid";
    detail: string;
    scope: string;
    sampledAt?: string;
    running: boolean | null;
    cgroup: CgroupSnapshot | null;
    network: { rxBytes: number; txBytes: number } | null;
    tcp: { outSegments: number; retransmittedSegments: number } | null;
  };
}

export interface PerformanceRun {
  checkedAt: number;
  mode: ConnectionMode;
  panel: PathMeasurement;
  internet: PathMeasurement;
  serverBefore: ServerSnapshot | null;
  serverAfter: ServerSnapshot | null;
  serverError: string | null;
}

export interface PathSummary {
  latencyMs: number | null;
  jitterMs: number | null;
  errorCount: number;
  downloadMbps: number | null;
  uploadMbps: number | null;
}

export interface PerformanceHistoryEntry {
  checkedAt: number;
  mode: ConnectionMode;
  panel: PathSummary;
  internet: PathSummary;
}

const MIB = 1024 * 1024;
const HISTORY_KEY = "private-gateway-performance-v1";
const HISTORY_LIMIT = 12;
const CLOUDFLARE = "https://speed.cloudflare.com";
const TOTAL_TIMEOUT_MS = 45_000;
const REQUEST_TIMEOUT_MS = 10_000;
const MIN_SAMPLE_MS = 1_000;
const LATENCY_ATTEMPTS = 8;
const FINISHED = "Лимит времени проверки исчерпан; остальные замеры не выполнены.";
let nonce = 0;

function aborted(signal: AbortSignal): void {
  if (signal.aborted) throw signal.reason ?? new DOMException("Проверка остановлена.", "AbortError");
}

// Ограничиваем также чтение потока: зависшее тело ответа не оставляет проверку активной.
function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  aborted(signal);
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(signal.reason ?? new DOMException("Проверка остановлена.", "AbortError"));
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(resolve, reject).finally(() => signal.removeEventListener("abort", onAbort));
  });
}

function operationSignal(signal: AbortSignal): AbortSignal {
  return AbortSignal.any([signal, AbortSignal.timeout(REQUEST_TIMEOUT_MS)]);
}

function measurementError(error: unknown): string {
  if (error instanceof Error && error.name === "TimeoutError") return "Ответ не получен за 10 секунд.";
  if (error instanceof Error && error.name === "AbortError") return FINISHED;
  if (error instanceof Error && error.name === "TypeError") return "Служба недоступна или браузер запретил запрос.";
  return error instanceof Error ? error.message : "Не удалось выполнить замер.";
}

function init(signal: AbortSignal, token?: string): RequestInit {
  return {
    signal,
    cache: "no-store",
    credentials: token === undefined ? "omit" : "same-origin",
    referrerPolicy: "no-referrer",
    redirect: "error",
    ...(token === undefined ? {} : { headers: { Authorization: `Bearer ${token}` } }),
  };
}

async function responseFor(url: string, options: RequestInit, signal: AbortSignal): Promise<Response> {
  const pending = fetch(url, options).then((response) => {
    if (signal.aborted) {
      void response.body?.cancel().catch(() => undefined);
      aborted(signal);
    }
    return response;
  });
  const response = await abortable(pending, signal);
  if (signal.aborted) {
    void response.body?.cancel().catch(() => undefined);
    aborted(signal);
  }
  if (!response.ok) {
    void response.body?.cancel().catch(() => undefined);
    throw new Error(response.status === 429
      ? "Достигнут лимит измерений сервера. Повторите проверку через минуту."
      : response.status === 409
        ? "Сервер уже выполняет другой замер. Повторите проверку позже."
        : `Служба измерений вернула HTTP ${response.status}.`);
  }
  return response;
}

async function readBytes(response: Response, cap: number, signal: AbortSignal, received?: (count: number) => void): Promise<number> {
  const length = response.headers.get("content-length");
  if (length !== null && (!/^\d+$/.test(length) || Number(length) > cap)) {
    void response.body?.cancel().catch(() => undefined);
    throw new Error("Служба вернула больше данных, чем запрошено.");
  }
  if (!response.body) throw new Error("Браузер не предоставил поток данных для замера.");
  const reader = response.body.getReader();
  const cancel = () => { void reader.cancel().catch(() => undefined); };
  signal.addEventListener("abort", cancel, { once: true });
  let total = 0;
  try {
    while (true) {
      aborted(signal);
      const chunk = await abortable(reader.read(), signal);
      aborted(signal);
      if (chunk.done) return total;
      total += chunk.value.byteLength;
      received?.(chunk.value.byteLength);
      if (total > cap) throw new Error("Служба вернула больше данных, чем запрошено.");
    }
  } finally {
    signal.removeEventListener("abort", cancel);
    void reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}

function nextURL(url: string): string {
  // Случайный идентификатор устройства не нужен: счётчик исключает повтор ответа из кеша.
  return `${url}${url.includes("?") ? "&" : "?"}r=${Date.now()}-${++nonce}`;
}

export function latencyFromSamples(samples: (number | null)[]): LatencyMeasurement {
  const valid = samples.filter((value): value is number => value !== null && Number.isFinite(value) && value >= 0);
  const sorted = [...valid].sort((a, b) => a - b);
  const differences: number[] = [];
  // Не соединяем соседние успешные пробы через неудачную: её задержка неизвестна.
  for (let index = 1; index < samples.length; index++) {
    const previous = samples[index - 1];
    const current = samples[index];
    if (previous !== null && current !== null && Number.isFinite(previous) && Number.isFinite(current) && previous >= 0 && current >= 0)
      differences.push(Math.abs(current - previous));
  }
  const middle = Math.floor(sorted.length / 2);
  return {
    medianMs: sorted.length ? (sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2) : null,
    p95Ms: sorted.length ? sorted[Math.ceil(sorted.length * 0.95) - 1] : null,
    jitterMs: differences.length ? differences.reduce((sum, value) => sum + value, 0) / differences.length : null,
    attempts: samples.length,
    successes: valid.length,
    errorCount: samples.length - valid.length,
    error: null,
  };
}

async function measureLatency(url: string, signal: AbortSignal, parent: AbortSignal, token?: string): Promise<LatencyMeasurement> {
  const samples: (number | null)[] = [];
  let error: string | null = null;
  let consecutiveFailures = 0;
  // Первая проба прогревает TLS/соединение и не входит в устойчивую задержку HTTP.
  for (let index = -1; index < LATENCY_ATTEMPTS; index++) {
    aborted(parent);
    if (signal.aborted) { error = FINISHED; break; }
    const one = operationSignal(signal);
    try {
      const start = performance.now();
      const response = await responseFor(nextURL(url), init(one, token), one);
      await readBytes(response, 4096, one);
      const duration = performance.now() - start;
      if (index >= 0) samples.push(duration);
      consecutiveFailures = 0;
    } catch (failure) {
      aborted(parent);
      error = signal.aborted ? FINISHED : measurementError(failure);
      if (index >= 0) samples.push(null);
      // Два тайм-аута подряд уже показывают недоступность этой службы; не ждём восемь.
      if (++consecutiveFailures >= 2) break;
    }
  }
  const measurement = latencyFromSamples(samples);
  return { ...measurement, error: measurement.errorCount || !measurement.attempts || signal.aborted ? error : null };
}

const emptyTransfer = (error: string | null = FINISHED): TransferMeasurement => ({ mbps: null, bytes: 0, durationMs: null, error });

function randomPayload(bytes: number): ArrayBuffer {
  const payload = new Uint8Array(bytes);
  for (let offset = 0; offset < bytes; offset += 65536)
    crypto.getRandomValues(payload.subarray(offset, Math.min(bytes, offset + 65536)));
  return payload.buffer;
}

async function measureTransfer(
  url: string,
  sizes: number[],
  upload: boolean,
  signal: AbortSignal,
  parent: AbortSignal,
  token?: string,
): Promise<TransferMeasurement> {
  let result = emptyTransfer();
  let transferred = 0;
  for (const size of sizes) {
    aborted(parent);
    if (signal.aborted) return { ...result, bytes: transferred, error: FINISHED };
    const one = operationSignal(signal);
    try {
      const options = init(one, token);
      if (upload) {
        options.method = "POST";
        options.body = randomPayload(size);
        // Внешней службе отправляется только двоичное тело без заголовков панели.
        if (token !== undefined) options.headers = { ...options.headers, "Content-Type": "application/octet-stream" };
      }
      const target = nextURL(upload ? url : `${url}?bytes=${size}`);
      const start = performance.now();
      const response = await responseFor(target, options, one);
      if (upload) {
        if (token !== undefined) {
          const acknowledgement = await abortable(response.json() as Promise<{ bytes?: unknown }>, one);
          if (acknowledgement.bytes !== size) throw new Error("Сервер подтвердил другой объём загрузки.");
        } else {
          // Ответ Cloudflare содержит серверные времена, а не повтор отправленного тела.
          await readBytes(response, 16 * 1024, one);
        }
        transferred += size;
      } else {
        const received = await readBytes(response, size, one, (count) => { transferred += count; });
        if (received !== size) throw new Error("Служба вернула неполные данные; скорость не определена.");
      }
      const duration = performance.now() - start;
      if (!Number.isFinite(duration) || duration <= 0) throw new Error("Замер оказался слишком коротким для расчёта скорости.");
      const mbps = size * 8 / (duration * 1000);
      if (!Number.isFinite(mbps)) throw new Error("Не удалось рассчитать скорость.");
      result = { mbps, bytes: transferred, durationMs: duration, error: null };
      if (duration >= MIN_SAMPLE_MS) return result;
    } catch (failure) {
      aborted(parent);
      return { ...result, bytes: transferred, error: signal.aborted ? FINISHED : measurementError(failure) };
    }
  }
  return result;
}

export async function runPerformance(
  token: string,
  mode: ConnectionMode,
  signal: AbortSignal,
  onProgress?: (phase: string, percent: number) => void,
): Promise<PerformanceRun> {
  aborted(signal);
  const deadline = AbortSignal.any([signal, AbortSignal.timeout(TOTAL_TIMEOUT_MS)]);
  let serverError: string | null = null;
  // Процент отражает завершённые этапы, включая недоступные замеры, а не прошедшее время.
  function progress(phase: string, percent: number): void {
    aborted(signal);
    onProgress?.(phase, percent);
  }
  async function snapshot(): Promise<ServerSnapshot | null> {
    if (deadline.aborted) { serverError = FINISHED; return null; }
    try {
      return await abortable(request<ServerSnapshot>(token, "/checks/performance/server", { signal: operationSignal(deadline) }), deadline);
    } catch (failure) {
      aborted(signal);
      serverError = deadline.aborted ? FINISHED : measurementError(failure);
      return null;
    }
  }
  progress("Состояние сервера", 0);
  const serverBefore = await snapshot();
  progress("Задержка до панели", 5);
  const panelLatency = await measureLatency("/api/v1/checks/performance/ping", deadline, signal, token);
  const unavailable = (latency: LatencyMeasurement) => emptyTransfer(latency.error ?? "Служба измерений не ответила на пробы задержки.");
  progress("Получение от сервера", 20);
  const panelDownload = panelLatency.successes
    ? await measureTransfer("/api/v1/checks/performance/download", [4 * MIB], false, deadline, signal, token)
    : unavailable(panelLatency);
  progress("Отправка на сервер", 35);
  const panelUpload = panelLatency.successes
    ? await measureTransfer("/api/v1/checks/performance/upload", [MIB], true, deadline, signal, token)
    : unavailable(panelLatency);
  progress("Задержка интернета", 50);
  const internetLatency = await measureLatency(`${CLOUDFLARE}/__down?bytes=0`, deadline, signal);
  progress("Получение из интернета", 65);
  const internetDownload = internetLatency.successes
    ? await measureTransfer(`${CLOUDFLARE}/__down`, [MIB, 4 * MIB, 16 * MIB], false, deadline, signal)
    : unavailable(internetLatency);
  progress("Отправка в интернет", 80);
  const internetUpload = internetLatency.successes
    ? await measureTransfer(`${CLOUDFLARE}/__up`, [MIB / 4, MIB, 4 * MIB], true, deadline, signal)
    : unavailable(internetLatency);
  progress("Итоговое состояние сервера", 95);
  const serverAfter = await snapshot();
  progress("Замеры завершены", 100);
  return {
    checkedAt: Date.now(), mode,
    panel: { latency: panelLatency, download: panelDownload, upload: panelUpload },
    internet: { latency: internetLatency, download: internetDownload, upload: internetUpload },
    serverBefore, serverAfter, serverError,
  };
}

function numeric(value: unknown, limit = 1e12): number | null {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 && value <= limit ? value : null;
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function parseSummary(value: unknown): PathSummary | null {
  if (!record(value)) return null;
  const keys = ["latencyMs", "jitterMs", "downloadMbps", "uploadMbps"] as const;
  for (const key of keys) if (value[key] !== null && numeric(value[key]) === null) return null;
  const errors = numeric(value.errorCount, LATENCY_ATTEMPTS);
  if (errors === null || !Number.isInteger(errors)) return null;
  return {
    latencyMs: value.latencyMs as number | null, jitterMs: value.jitterMs as number | null,
    downloadMbps: value.downloadMbps as number | null, uploadMbps: value.uploadMbps as number | null,
    errorCount: errors,
  };
}

function parseEntry(value: unknown): PerformanceHistoryEntry | null {
  if (!record(value) || !connectionModes.some(({ value: mode }) => mode === value.mode)) return null;
  const checkedAt = numeric(value.checkedAt, 8.64e15);
  const panel = parseSummary(value.panel);
  const internet = parseSummary(value.internet);
  if (checkedAt === null || checkedAt < 1 || !panel || !internet) return null;
  // Проекция перечисленных полей исключает из истории адреса, отпечатки и сеансы.
  return { checkedAt, mode: value.mode as ConnectionMode, panel, internet };
}

export function loadPerformanceHistory(): PerformanceHistoryEntry[] {
  try {
    const serialized = localStorage.getItem(HISTORY_KEY);
    if (!serialized || serialized.length > 64 * 1024) return [];
    const values: unknown = JSON.parse(serialized);
    if (!Array.isArray(values)) return [];
    return values.slice(0, HISTORY_LIMIT).map(parseEntry).filter((entry): entry is PerformanceHistoryEntry => entry !== null);
  } catch {
    // В приватном окне или при запрете хранилища сама диагностика остаётся доступной.
    return [];
  }
}

function pathSummary(value: PathMeasurement): PathSummary {
  return {
    latencyMs: value.latency.medianMs, jitterMs: value.latency.jitterMs, errorCount: value.latency.errorCount,
    downloadMbps: value.download.mbps, uploadMbps: value.upload.mbps,
  };
}

export function savePerformanceHistory(run: PerformanceRun): PerformanceHistoryEntry[] {
  const parsed = parseEntry({ checkedAt: run.checkedAt, mode: run.mode, panel: pathSummary(run.panel), internet: pathSummary(run.internet) });
  const history = loadPerformanceHistory();
  if (!parsed) return history;
  const entries = [parsed, ...history.filter((entry) => entry.checkedAt !== run.checkedAt)].slice(0, HISTORY_LIMIT);
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(entries));
  } catch {
    // Ограничение квоты не превращаем в ошибку завершённого замера.
  }
  return entries;
}

export function clearPerformanceHistory(): void {
  try { localStorage.removeItem(HISTORY_KEY); } catch { /* Хранилище может быть запрещено браузером. */ }
}
