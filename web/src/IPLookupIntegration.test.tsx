import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Checks } from "./Checks";
import { ServerDiagnostics } from "./ServerDiagnostics";
import { runPerformance, type PathMeasurement, type PerformanceRun } from "./performance";
import type { CheckResult } from "./CheckResults";

vi.mock("./performance", async (importOriginal) => ({
  ...await importOriginal<typeof import("./performance")>(),
  runPerformance: vi.fn(),
}));
vi.mock("./deviceDiagnostics", () => ({
  collectDeviceDiagnostics: () => ({ collectedAt: Date.now(), facts: [], notes: [] }),
}));

const browserIP = "8.8.8.8";
const browserIPv6 = "2606:4700:4700::1111";
const gatewayIP = "162.35.172.6";
const serverExitIP = "1.1.1.1";
const ownZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
const clients: QueryClient[] = [];
let visibleIPv4: string;
let visibleIPv6: string | null;
let healthChecks: CheckResult[];
let lookupStatus: "available" | "not_configured";
let lookupFailure: boolean;
let lookupBlocked: boolean;
let lookupSignal: AbortSignal | undefined;
let fetchMock: ReturnType<typeof vi.fn<(path: string, options: RequestInit) => Promise<Response>>>;

function measuredPath(): PathMeasurement {
  return {
    latency: { medianMs: 100, p95Ms: 140, jitterMs: 6, attempts: 8, successes: 8, errorCount: 0, error: null },
    download: { mbps: 12, bytes: 1024, durationMs: 1000, error: null },
    upload: { mbps: 4, bytes: 512, durationMs: 1000, error: null },
  };
}

function performanceResult(): PerformanceRun {
  return {
    checkedAt: Date.now(), mode: "current", panel: measuredPath(), internet: measuredPath(),
    serverBefore: null, serverAfter: null, serverError: null,
  };
}

function setup(section: "privacy" | "server" = "privacy") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  const component = <QueryClientProvider client={client}>
    {section === "privacy" ? <Checks token="diagnostic-session" nodeHost={gatewayIP} /> : <ServerDiagnostics token="diagnostic-session" />}
  </QueryClientProvider>;
  return { client, component, view: render(component) };
}

function lookupCalls() {
  return fetchMock.mock.calls.filter(([path]) => path === "/api/v1/ip-lookup");
}

function requestedIPs() {
  return lookupCalls().map(([, options]) => (JSON.parse(options.body as string) as { ip: string }).ip);
}

async function startPrivacyCheck() {
  await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(browserIP));
  fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
}

beforeEach(() => {
  localStorage.clear();
  visibleIPv4 = browserIP;
  visibleIPv6 = null;
  lookupStatus = "available";
  lookupFailure = false;
  lookupBlocked = false;
  lookupSignal = undefined;
  healthChecks = [{ id: "traffic", title: "VLESS-трафик", status: "ok", detail: "Трафик сервера проходит.", ip: serverExitIP }];
  vi.mocked(runPerformance).mockReset().mockImplementation(async (_token, mode) => ({ ...performanceResult(), mode }));
  fetchMock = vi.fn(async (path: string, options: RequestInit = {}) => {
    const network = (ip: string) => Response.json({ ip, as_number: 15169, isp_name: "Проверочная сеть", country_code: "US", city_name: "Город", time_zone: ownZone });
    if (path === "https://ipv4.ipleak.net/json/") return network(visibleIPv4);
    if (path === "https://ipv6.ipleak.net/json/") {
      if (!visibleIPv6) throw new TypeError("Failed to fetch");
      return network(visibleIPv6);
    }
    if (/^https:\/\/[0-9a-f]{40}-\d\.ipleak\.net\/dnsdetection\/$/.test(path))
      return Response.json({ ip: { "9.9.9.9": 1 } });
    if (path.startsWith("https://ipleak.net/json/")) return network(path.slice("https://ipleak.net/json/".length));
    if (path === "/api/v1/checks") return Response.json({ checks: healthChecks });
    if (path === "/api/v1/ip-lookup") {
      const { ip } = JSON.parse(options.body as string) as { ip: string };
      if (lookupBlocked) {
        lookupSignal = options.signal as AbortSignal;
        return new Promise<Response>((_resolve, reject) => {
          lookupSignal?.addEventListener("abort", () => reject(lookupSignal?.reason), { once: true });
        });
      }
      if (lookupFailure) return Response.json({ error: "Служба сведений об IP недоступна." }, { status: 503 });
      return Response.json({
        ip, source: "IP2Location.io", status: lookupStatus,
        checkedAt: lookupStatus === "available" ? "2026-10-23T10:00:00Z" : null,
        detail: lookupStatus === "available" ? "Сведения из базы IP2Location.io." : "API-ключ IP2Location.io не настроен.",
        data: lookupStatus === "available" ? {
          ip, isp: "Сеть из IP2Location", country_code: "US", country_name: "United States of America",
          fraud_score: 0, is_proxy: false, proxy: { is_vpn: false, is_data_center: true },
        } : null,
      });
    }
    throw new Error(`Неожиданный запрос: ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(async () => {
  cleanup();
  for (const client of clients.splice(0)) {
    await client.cancelQueries();
    client.clear();
  }
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("IP lookup in diagnostic sections", () => {
  it.each(["privacy", "server"] as const)("exports one UTF-8 report after a manual-only %s lookup without inventing main diagnostics", async (scope) => {
    const create = vi.fn((_blob: Blob | MediaSource) => "blob:manual-report");
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = vi.fn();
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const { client } = setup(scope);
    client.setQueryData(["ip-lookup-manual", scope === "privacy" ? "server" : "privacy"], {
      ip: "9.9.9.9", source: "IP2Location.io", status: "available", checkedAt: null,
      detail: "other-section-secret", data: { ip: "9.9.9.9" },
    });
    expect(screen.queryByRole("button", { name: "Скачать отчёт" })).toBeNull();
    fireEvent.change(screen.getByLabelText("IP-адрес"), { target: { value: serverExitIP } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    const download = await screen.findByRole("button", { name: "Скачать отчёт" });
    await waitFor(() => expect((download as HTMLButtonElement).disabled).toBe(false));
    expect(screen.getAllByRole("button", { name: "Скачать отчёт" })).toHaveLength(1);
    expect(requestedIPs()).toEqual([serverExitIP]);
    expect(fetchMock).toHaveBeenCalledTimes(scope === "privacy" ? 5 : 1);
    expect(runPerformance).not.toHaveBeenCalled();
    vi.useFakeTimers();
    fireEvent.click(download);
    expect(click).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(1_000);
    vi.useRealTimers();
    const blob = create.mock.calls[0][0] as Blob;
    expect(blob.type).toBe("text/plain;charset=utf-8");
    const bytes = await new Promise<Uint8Array>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
      reader.onerror = () => reject(reader.error);
      reader.readAsArrayBuffer(blob);
    });
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    const text = new TextDecoder().decode(bytes);
    expect(text).toContain(scope === "privacy" ? "Основная проверка безопасности не запускалась." : "Основная проверка сервера не запускалась.");
    expect(text).toContain(`IP-адрес: ${serverExitIP}`);
    expect(text).toContain("Ручная проверка другого IP");
    expect(text).not.toMatch(/8\.8\.8\.8|162\.35\.172\.6|9\.9\.9\.9|other-section-secret|Задержка HTTP|Логические ядра/);
    if (scope === "privacy") {
      expect(client.getQueryData(["privacy-checks"])).toMatchObject({ targetIP: serverExitIP, ipLookup: { ip: serverExitIP } });
    } else {
      expect(client.getQueryData(["server-checks"])).toBeUndefined();
    }
  });

  it("enriches the measured browser exit on demand with authorization and reuses its section cache", async () => {
    const { client, component, view } = setup();
    await startPrivacyCheck();
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs()).toEqual([browserIP]);
    expect(requestedIPs()).not.toContain(gatewayIP);
    const [, options] = lookupCalls()[0];
    expect(options.method).toBe("POST");
    expect(new Headers(options.headers).get("Authorization")).toBe("Bearer diagnostic-session");
    const result = client.getQueryData<{ ipLookups: Array<{ ip: string }> }>(["privacy-checks"]);
    expect(result?.ipLookups.map(({ ip }) => ip)).toEqual([browserIP]);
    expect(screen.getByRole("heading", { name: "24 сайта для проверки" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Данные об IP и репутации" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Текущее подключение и браузер" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "IP2Location" }).getAttribute("href")).toBe("https://www.ip2location.com/demo");
    view.unmount();
    render(component);
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(lookupCalls()).toHaveLength(1);
  });

  it("enriches each measured IPv4 and IPv6 once without submitting DNS resolver addresses", async () => {
    visibleIPv6 = browserIPv6;
    const { client } = setup();
    await startPrivacyCheck();
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs().sort()).toEqual([browserIP, browserIPv6].sort());
    expect(requestedIPs()).not.toContain("9.9.9.9");
    expect(client.getQueryData<{ ipLookups: unknown[] }>(["privacy-checks"])?.ipLookups).toHaveLength(2);
  });

  it("recognizes equivalent IPv6 text as the current browser exit", async () => {
    visibleIPv6 = browserIPv6;
    const { client } = setup();
    await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(browserIP));
    const expanded = "2606:4700:4700:0000:0000:0000:0000:1111";
    fireEvent.change(screen.getByLabelText("IP-адрес"), { target: { value: expanded } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    const result = client.getQueryData<{ targetIP: string; browserFacts: unknown; ipLookups: Array<{ ip: string }> }>(["privacy-checks"]);
    expect(result?.targetIP).toBe(expanded);
    expect(result?.browserFacts).toBeTruthy();
    expect(result?.ipLookups.map(({ ip }) => ip).sort()).toEqual([browserIP, browserIPv6].sort());
    expect(screen.queryByText(/По чужому IP эта проверка недоступна/)).toBeNull();
  });

  it("keeps absent API configuration separate from the completed network leak checks", async () => {
    lookupStatus = "not_configured";
    const { client } = setup();
    await startPrivacyCheck();
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(screen.getByText("API-ключ IP2Location.io не настроен.")).toBeTruthy();
    const networkChecks = within(screen.getByRole("heading", { name: "Что видят сайты" }).closest("section")!);
    expect(networkChecks.getByText("WebRTC")).toBeTruthy();
    expect(networkChecks.getByText("DNS")).toBeTruthy();
    expect(networkChecks.queryByText("Проблема")).toBeNull();
    const result = client.getQueryData<{ ipLookups: Array<{ status: string; data: unknown }> }>(["privacy-checks"]);
    expect(result?.ipLookups[0]).toMatchObject({ status: "not_configured", data: null });
    expect(screen.getAllByRole("link").some((link) => link.getAttribute("href") === "https://www.ip2location.com/demo")).toBe(true);
  });

  it("looks up only the actual successful VLESS exit and preserves server measurements if the lookup fails", async () => {
    lookupFailure = true;
    const { client } = setup("server");
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Проверить подключение" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs()).toEqual([serverExitIP]);
    expect(runPerformance).toHaveBeenCalledOnce();
    const result = client.getQueryData<{ server: CheckResult[]; performance: PerformanceRun; serverIPLookup: { status: string } }>(["server-checks"]);
    expect(result?.server[0].ip).toBe(serverExitIP);
    expect(result?.performance.internet.download.mbps).toBe(12);
    expect(result?.serverIPLookup.status).toBe("unavailable");
    expect(screen.getByRole("heading", { name: "IP выхода сервера" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Что видят сайты" })).toBeNull();
  });

  it("does not infer a server exit from an unsuccessful traffic check", async () => {
    healthChecks = [{ id: "traffic", title: "VLESS-трафик", status: "fail", detail: "Выход не измерен.", ip: serverExitIP }];
    const { client } = setup("server");
    fireEvent.click(screen.getByRole("button", { name: "Проверить подключение" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(lookupCalls()).toHaveLength(0);
    expect(runPerformance).toHaveBeenCalledOnce();
    expect(client.getQueryData<{ serverIPLookup: unknown }>(["server-checks"])?.serverIPLookup).toBeNull();
  });

  it("enriches a measured foreign VLESS exit even when the health check warns about the different address", async () => {
    healthChecks = [{ id: "traffic", title: "VLESS-трафик", status: "warn", detail: "Фактический выход отличается от адреса узла.", ip: serverExitIP }];
    const { client } = setup("server");
    fireEvent.click(screen.getByRole("button", { name: "Проверить подключение" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs()).toEqual([serverExitIP]);
    expect(requestedIPs()).not.toContain(gatewayIP);
    expect(client.getQueryData<{ serverIPLookup: { ip: string } }>(["server-checks"])?.serverIPLookup.ip).toBe(serverExitIP);
    expect(runPerformance).toHaveBeenCalledOnce();
  });

  it("does not request metadata for an absent or non-public VLESS exit", async () => {
    healthChecks = [{ id: "traffic", title: "VLESS-трафик", status: "warn", detail: "Ответ не получен." }];
    setup("server");
    fireEvent.click(screen.getByRole("button", { name: "Проверить подключение" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(lookupCalls()).toHaveLength(0);
    healthChecks = [{ id: "traffic", title: "VLESS-трафик", status: "ok", detail: "Некорректный адрес выхода.", ip: "127.0.0.1" }];
    fireEvent.click(screen.getByRole("button", { name: "Проверить снова" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(lookupCalls()).toHaveLength(0);
  });

  it("aborts an in-flight enrichment without publishing a completed privacy result", async () => {
    lookupBlocked = true;
    const { client } = setup();
    await startPrivacyCheck();
    await waitFor(() => expect(lookupSignal).toBeDefined());
    const progress = screen.getByRole("progressbar", { name: "Ход проверки безопасности" }) as HTMLProgressElement;
    expect(progress.value).toBeLessThan(100);
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка безопасности остановлена.");
    expect(lookupSignal?.aborted).toBe(true);
    expect(progress.value).toBeLessThan(100);
    expect(client.getQueryData(["privacy-checks"])).toBeUndefined();
  });

  it("validates the unified target before requesting and replaces browser facts with an address-only result", async () => {
    const { client } = setup();
    await startPrivacyCheck();
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(screen.getByRole("button", { name: "Скачать отчёт" })).toBeTruthy();
    const input = screen.getByLabelText("IP-адрес");
    for (const invalid of ["", "127.0.0.1", "https://example.com", "999.1.1.1", "8.8.8.8:1080", "2001:db8::1"]) {
      fireEvent.change(input, { target: { value: invalid } });
      fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
      await screen.findByRole("alert");
      expect(requestedIPs()).toEqual([browserIP]);
      expect(screen.queryByRole("heading", { name: "IP и репутация" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Скачать отчёт" })).toBeNull();
    }
    const dnsCalls = () => fetchMock.mock.calls.filter(([path]) => String(path).includes("dnsdetection"));
    expect(dnsCalls()).toHaveLength(10);
    fireEvent.change(input, { target: { value: ` ${serverExitIP} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs()).toEqual([browserIP, serverExitIP]);
    expect(client.getQueryData(["privacy-checks"])).toMatchObject({ targetIP: serverExitIP, ipLookup: { ip: serverExitIP } });
    expect(client.getQueryData(["privacy-checks"])).not.toHaveProperty("browserFacts");
    expect(client.getQueryData(["ip-lookup-manual", "privacy"])).toBeUndefined();
    expect(dnsCalls()).toHaveLength(10);
    const browserChecks = within(screen.getByRole("heading", { name: "Что видят сайты" }).closest("section")!);
    expect(browserChecks.getAllByText("Пропущено")).toHaveLength(5);
    expect(browserChecks.queryByText(/Сайты видят/)).toBeNull();
    expect(screen.queryByText("Текущий выход браузера по IPv4.")).toBeNull();
    expect(screen.getByText(`Проверяемый IP: ${serverExitIP}. Сведения относятся только к этому адресу.`)).toBeTruthy();
  });

  it("prefills the current browser IP without running DNS, STUN or metadata lookups", async () => {
    const rtc = vi.fn();
    vi.stubGlobal("RTCPeerConnection", rtc);
    setup();
    await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(browserIP));
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      "https://ipv4.ipleak.net/json/", "https://ipv6.ipleak.net/json/",
    ]);
    expect(rtc).not.toHaveBeenCalled();
    expect(lookupCalls()).toHaveLength(0);
    for (const [, options] of fetchMock.mock.calls) {
      expect(options.credentials).toBe("omit");
      expect(new Headers(options.headers).get("Authorization")).toBeNull();
    }
    expect(screen.getAllByLabelText("IP-адрес")).toHaveLength(1);
    expect(screen.queryByRole("region", { name: "Проверить другой IP" })).toBeNull();
  });

  it.each(["automatic", "my-ip"])("does not overwrite manually entered text after late %s detection", async (mode) => {
    const respond = fetchMock.getMockImplementation()!;
    let resolveDetection!: (response: Response) => void;
    let hold = mode === "automatic";
    fetchMock.mockImplementation((path: string, options: RequestInit = {}) => {
      if (hold && path === "https://ipv4.ipleak.net/json/") return new Promise<Response>((resolve) => { resolveDetection = resolve; });
      return respond(path, options);
    });
    setup();
    if (mode === "my-ip") {
      await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(browserIP));
      hold = true;
      fireEvent.click(screen.getByRole("button", { name: "Мой IP" }));
    }
    const input = screen.getByLabelText("IP-адрес") as HTMLInputElement;
    fireEvent.change(input, { target: { value: serverExitIP } });
    resolveDetection(Response.json({ ip: browserIP }));
    await waitFor(() => expect(screen.queryByText("Определяем текущий IP браузера…")).toBeNull());
    expect(input.value).toBe(serverExitIP);
    expect(lookupCalls()).toHaveLength(0);
  });

  it("rejects an automatically selected IP if the route changed before starting the check", async () => {
    const { client } = setup();
    await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(browserIP));
    visibleIPv4 = serverExitIP;
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await screen.findByText(/IP подключения изменился\. Адрес/);
    expect(requestedIPs()).toEqual([]);
    expect(client.getQueryData(["privacy-checks"])).toBeUndefined();
    expect(screen.queryByRole("button", { name: "Скачать отчёт" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Мой IP" }));
    await waitFor(() => expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).value).toBe(serverExitIP));
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect(requestedIPs()).toEqual([serverExitIP]);
  });

  it("does not publish metadata or browser facts if the exit changes during the check", async () => {
    const respond = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (path: string, options: RequestInit = {}) => {
      const response = await respond(path, options);
      if (path === "/api/v1/ip-lookup") visibleIPv4 = serverExitIP;
      return response;
    });
    const { client } = setup();
    await startPrivacyCheck();
    await screen.findByText(/IP подключения изменился во время проверки/);
    expect(requestedIPs()).toEqual([browserIP]);
    expect(client.getQueryData(["privacy-checks"])).toBeUndefined();
    expect(screen.queryByRole("heading", { name: "IP и репутация" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Скачать отчёт" })).toBeNull();
    expect(screen.queryByText(/Сайты видят/)).toBeNull();
  });

  it("does not publish a cancelled late lookup over a new target", async () => {
    const respond = fetchMock.getMockImplementation()!;
    let resolveOld!: (response: Response) => void;
    let oldSignal: AbortSignal | undefined;
    fetchMock.mockImplementation((path: string, options: RequestInit = {}) => {
      if (path === "/api/v1/ip-lookup" && JSON.parse(options.body as string).ip === serverExitIP) {
        oldSignal = options.signal as AbortSignal;
        return new Promise<Response>((resolve) => { resolveOld = resolve; });
      }
      return respond(path, options);
    });
    const { client } = setup();
    fireEvent.change(screen.getByLabelText("IP-адрес"), { target: { value: serverExitIP } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await waitFor(() => expect(oldSignal).toBeDefined());
    expect((screen.getByLabelText("IP-адрес") as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка безопасности остановлена.");
    expect(oldSignal?.aborted).toBe(true);
    fireEvent.change(screen.getByLabelText("IP-адрес"), { target: { value: "9.9.9.9" } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить IP" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    await act(async () => {
      resolveOld(Response.json({ ip: serverExitIP, source: "IP2Location.io", status: "available", checkedAt: null,
        detail: "old-result-must-not-appear", data: { ip: serverExitIP } }));
    });
    await waitFor(() => expect(client.getQueryData(["privacy-checks"])).toMatchObject({ targetIP: "9.9.9.9", ipLookup: { ip: "9.9.9.9" } }));
    expect(screen.queryByText("old-result-must-not-appear")).toBeNull();
    expect(screen.getByText("Проверяемый IP: 9.9.9.9. Сведения относятся только к этому адресу.")).toBeTruthy();
  });
});
