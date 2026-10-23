import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { toDataURL } from "qrcode";
import App from "./App";
import * as deviceDiagnostics from "./deviceDiagnostics";

vi.mock("qrcode", () => ({
  toDataURL: vi.fn().mockResolvedValue("data:image/png;base64,cXJjb2Rl"),
}));

const profileURI =
  "vless://sensitive-device-credential@gateway.example:443?security=reality#Phone";
const profileJSON = '{"outbounds":[{"type":"vless","uuid":"sensitive-device-credential"}]}';
const profileYAML = 'proxies:\n  - type: vless\n    uuid: sensitive-device-credential\n';
let subscriptionPath = "/sub/sensitive-subscription-token-1";
let rotations = 0;
const subscriptionLink = () => `${window.location.origin}${subscriptionPath}`;
let revoked = false;
let deleted = false;
let revision = 1;
let appliedRevision = 1;
let interruptApply = false;
let profileRequests = 0;
let queryClient: QueryClient;
let fetchMock: ReturnType<typeof vi.fn>;
let adminSetup = true;
let browserRunning = false;
let rememberedSession = false;
let performanceBlocked = false;
let privacyBlocked = false;
let performanceUnavailable = false;
// Что отвечает ipleak.net, внешний сервис раздела «Проверка».
let visibleIP = "203.0.113.7";
let visibleIPv6: string | null = null;
let dnsResolvers = ["162.158.1.10"];
const ownZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
const networks: Record<string, { as_number: number; isp_name: string; country_code: string | null; time_zone: string | null }> = {
  "203.0.113.7": { as_number: 14061, isp_name: "DigitalOcean, LLC", country_code: "NL", time_zone: ownZone },
  "198.51.100.20": { as_number: 9009, isp_name: "M247 Europe SRL", country_code: "DE", time_zone: ownZone },
  "2001:db8::20": { as_number: 8359, isp_name: "MTS PJSC", country_code: "RU", time_zone: "Europe/Moscow" },
  "162.158.1.10": { as_number: 13335, isp_name: "Cloudflare, Inc.", country_code: null, time_zone: null },
  "192.0.2.53": { as_number: 8359, isp_name: "MTS PJSC", country_code: "RU", time_zone: "Europe/Moscow" },
};

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  window.localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  revoked = false;
  deleted = false;
  revision = 1;
  appliedRevision = 1;
  interruptApply = false;
  profileRequests = 0;
  subscriptionPath = "/sub/sensitive-subscription-token-1";
  rotations = 0;
  adminSetup = true;
  browserRunning = false;
  rememberedSession = false;
  performanceBlocked = false;
  privacyBlocked = false;
  performanceUnavailable = false;
  visibleIP = "203.0.113.7";
  visibleIPv6 = null;
  dnsResolvers = ["162.158.1.10"];
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  // В jsdom нет настоящих модальных окон: для тестов взаимодействия повторяем их открытие и закрытие.
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
  fetchMock = vi.fn(async (path: string, options: RequestInit = {}) => {
    if (path === "/api/v1/notifications") return Response.json({ items: [], unread: 0 });
    if (path === "/api/v1/notifications/preferences") return Response.json({ server: true, devices: true, configuration: true });
    if (path === "/api/v1/notifications/push-subscriptions") return Response.json({ items: [] });
    const about = (ip: string) => Response.json({ ip, ...networks[ip] });
    if (path === "/api/v1/auth/restore")
      return rememberedSession ? Response.json({ session: "admin-token" }) : Response.json({ error: "unauthorized" }, { status: 401 });
    if (path === "https://ipv4.ipleak.net/json/") {
      if (privacyBlocked) return new Promise<Response>((_resolve, reject) => {
        options.signal?.addEventListener("abort", () => reject(options.signal?.reason), { once: true });
      });
      return about(visibleIP);
    }
    if (path === "https://ipv6.ipleak.net/json/") {
      if (!visibleIPv6) throw new TypeError("Failed to fetch");
      return about(visibleIPv6);
    }
    if (/^https:\/\/[0-9a-f]{40}-\d\.ipleak\.net\/dnsdetection\/$/.test(path))
      return Response.json({ session: "test", ip: Object.fromEntries(dnsResolvers.map((ip) => [ip, 1])) });
    if (path.startsWith("https://ipleak.net/json/")) return about(path.slice("https://ipleak.net/json/".length));
    if (path.startsWith("https://speed.cloudflare.com/")) {
      if (performanceUnavailable) throw new TypeError("Failed to fetch");
      const url = new URL(path);
      return new Response(new Uint8Array(url.pathname === "/__down" ? Number(url.searchParams.get("bytes")) : 0));
    }
    if (path === "/api/v1/auth/login") {
      const input = JSON.parse(options.body as string) as { username: string; password: string };
      return input.username === "admin" && input.password === "long-password"
        ? Response.json({ status: adminSetup ? "totp_required" : "totp_setup_required", pending: "pending-token" })
        : Response.json({ error: "unauthorized" }, { status: 401 });
    }
    if (path === "/api/v1/auth/2fa/setup") return Response.json({ secret: "TESTONLY1234567890", otpauth_uri: "otpauth://totp/Private%20Gateway:admin?secret=TESTONLY1234567890" });
    if (path === "/api/v1/auth/2fa" || path === "/api/v1/auth/2fa/setup/confirm") {
      const input = JSON.parse(options.body as string) as { code: string; remember: boolean };
      if (input.code === "123456" && input.remember) rememberedSession = true;
      return options.headers && (options.headers as Record<string, string>).Authorization === "Bearer pending-token" && input.code === "123456"
        ? Response.json({ session: "admin-token" })
        : Response.json({ error: "unauthorized" }, { status: 401 });
    }
    if (path === "/api/v1/auth/logout") {
      rememberedSession = false;
      return Response.json({ status: "logged out" });
    }
    const token = (options.headers as Record<string, string>)?.Authorization;
    if (token !== "Bearer admin-token")
      return Response.json({ error: "unauthorized" }, { status: 401 });
    if (path.startsWith("/api/v1/checks/performance/")) {
      if (performanceUnavailable) return Response.json({ error: "unavailable" }, { status: 503 });
      const url = new URL(path, window.location.origin);
      if (url.pathname.endsWith("/ping")) {
        if (performanceBlocked) return new Promise<Response>((_resolve, reject) => {
          options.signal?.addEventListener("abort", () => reject(options.signal?.reason), { once: true });
        });
        return Response.json({ ok: true });
      }
      if (url.pathname.endsWith("/download")) return new Response(new Uint8Array(Number(url.searchParams.get("bytes"))));
      if (url.pathname.endsWith("/upload")) return Response.json({ bytes: (options.body as ArrayBuffer).byteLength });
      if (url.pathname.endsWith("/server")) return Response.json({
        sampledAt: new Date().toISOString(),
        runtime: { os: "linux", arch: "amd64", goVersion: "go-test", cpuCount: 2, goroutines: 5 },
        system: { scope: "Видимая виртуальная машина.", uptimeSeconds: 3600, loadAverage: [0.1, 0.2, 0.3], memoryTotalBytes: 1024 ** 3, memoryAvailableBytes: 512 * 1024 ** 2, cpu: { totalTicks: 200, stealTicks: 10 } },
        api: { scope: "Контейнер API.", memoryBytes: 1024, memoryLimitBytes: null, cpuQuotaCores: null, cpuUsageUsec: null, cpuThrottledUsec: null, cpuThrottledPeriods: null },
        vpn: { status: "unavailable", detail: "Показатели VPN недоступны.", scope: "Контейнер VPN.", running: null, cgroup: null, network: null, tcp: null },
      });
    }
    if (path === "/api/v1/status")
      return Response.json({
        database: "ok",
        node_id: "n1",
        devices: deleted ? 0 : 1,
        active_devices: revoked ? 0 : 1,
        desired_revision: revision,
        applied_revision: appliedRevision,
        xray_configured: true,
      });
    if (path === "/api/v1/nodes")
      return Response.json({
        nodes: [
          {
            id: "n1",
            name: "Main",
            host: "203.0.113.7",
            port: 443,
            server_name: "example.com",
            public_key: "public",
            short_id: "abcd",
          },
        ],
      });
    if (path === "/api/v1/devices")
      return Response.json({
        devices: deleted ? [] : [
          {
            id: "d1",
            name: "Phone",
            node_id: "n1",
            created_at: "2026-01-01T00:00:00Z",
            revoked_at: revoked ? "2026-01-02T00:00:00Z" : null,
          },
        ],
      });
    if (path === "/api/v1/devices/traffic")
      return Response.json({ devices: { d1: { uplink_bytes: 2048, downlink_bytes: 1048576 } }, period: "since_xray_restart" });
    if (path === "/api/v1/checks")
      return Response.json({
        checks: [
          { id: "database", title: "База данных", status: "ok", detail: "PostgreSQL отвечает." },
          {
            id: "traffic",
            title: "VLESS-трафик через сервер",
            status: "ok",
            detail: "Настоящий VLESS-трафик проходит через сервер; сайты видят IP 203.0.113.7 (NL) — адрес сервера.",
            ip: "203.0.113.7",
          },
        ],
      });
    if (path === "/api/v1/browser")
      return Response.json({ running: browserRunning, url: browserRunning ? "https://gateway.example/browser/vnc.html" : "" });
    if (path === "/api/v1/devices/d1/profile") {
      profileRequests++;
      return Response.json({ uri: profileURI, sing_box_json: profileJSON, mihomo_yaml: profileYAML, subscription_path: subscriptionPath });
    }
    if (path === "/api/v1/devices/d1/subscription" && options.method === "POST") {
      rotations++;
      subscriptionPath = `/sub/sensitive-subscription-token-${rotations + 1}`;
      return Response.json({ subscription_path: subscriptionPath });
    }
    if (path === "/api/v1/devices/d1/revoke" && options.method === "POST") {
      revoked = true;
      revision = 2;
      return Response.json({ id: "d1", revoked_at: "2026-01-02T00:00:00Z" });
    }
    if (path === "/api/v1/devices/d1" && options.method === "DELETE") {
      if (!revoked || appliedRevision !== revision)
        return Response.json({ error: "configuration pending" }, { status: 409 });
      deleted = true;
      return Response.json({ status: "deleted" });
    }
    if (path === "/api/v1/config/apply" && options.method === "POST") {
      appliedRevision = revision;
      if (interruptApply) throw new TypeError("VPN disconnected");
      return Response.json({ status: "applied" });
    }
    throw new Error(
      `Unexpected API request: ${options.method || "GET"} ${path}`,
    );
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(async () => {
  cleanup();
  await queryClient.cancelQueries();
  queryClient.clear();
  vi.unstubAllGlobals();
});

async function login() {
  render(
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>,
  );
  fireEvent.change(await screen.findByLabelText("Логин"), { target: { value: "admin" } });
  fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "long-password" } });
  fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
  fireEvent.change(await screen.findByLabelText("Код аутентификации"), { target: { value: "123456" } });
  fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
  await screen.findByText("Ваш приватный шлюз");
  await screen.findByText("Phone");
}

describe("administrator panel", () => {
  it("offers installation before login without starting a permission request", async () => {
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    await screen.findByLabelText("Логин");
    fireEvent.click(screen.getByRole("button", { name: "Установить приложение" }));
    const install = await screen.findByRole("dialog", { name: "Установить приложение" });
    expect(within(install).getByRole("heading", { name: "Private Gateway" })).toBeTruthy();
    expect(within(install).queryByRole("button", { name: "Включить уведомления" })).toBeNull();
    fireEvent.click(within(install).getByText("Закрыть", { selector: "button" }));
    expect(screen.queryByRole("dialog", { name: "Установить приложение" })).toBeNull();
    expect(fetchMock.mock.calls.some(([path]) => path.includes("/notifications"))).toBe(false);
  });

  it("opens installation and notifications from the phone More menu and the header bell", async () => {
    await login();
    const tabs = screen.getByRole("navigation", { name: "Нижнее меню" });
    fireEvent.click(within(tabs).getByRole("button", { name: "Ещё" }));
    fireEvent.click(within(screen.getByRole("dialog", { name: "Ещё" })).getByRole("button", { name: "Установить приложение" }));
    expect(screen.queryByRole("dialog", { name: "Ещё" })).toBeNull();
    const install = await screen.findByRole("dialog", { name: "Установить приложение" });
    fireEvent.click(within(install).getByText("Закрыть", { selector: "button" }));
    fireEvent.click(screen.getByRole("banner").querySelector(".notifications-bell") as HTMLButtonElement);
    expect(await screen.findByRole("region", { name: "Центр уведомлений" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Уведомления на этом устройстве" })).toBeTruthy();
    expect(window.location.search).toBe("?page=notifications");
    expect(await screen.findByRole("checkbox", { name: /Состояние сервера/ })).toBeTruthy();
    fireEvent.click(within(tabs).getByRole("button", { name: "Обзор" }));
    expect(window.location.search).toBe("");
  });

  it("retains a notification destination clicked while the login page is open", async () => {
    const worker = new EventTarget();
    vi.stubGlobal("navigator", { userAgent: navigator.userAgent, maxTouchPoints: navigator.maxTouchPoints, serviceWorker: worker });
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    await screen.findByLabelText("Логин");
    worker.dispatchEvent(new MessageEvent("message", { data: { type: "gateway-notification-open" } }));
    expect(window.location.search).toBe("?page=notifications");
    fireEvent.change(screen.getByLabelText("Логин"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    fireEvent.change(await screen.findByLabelText("Код аутентификации"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
    expect(await screen.findByRole("region", { name: "Центр уведомлений" })).toBeTruthy();
  });

  it("switches between the system, light and dark themes and remembers only the choice", async () => {
    await login();
    const themes = screen.getByRole("group", { name: "Тема оформления" });
    expect(within(themes).getByRole("button", { name: "Авто" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(within(themes).getByRole("button", { name: "Тёмная" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(window.localStorage.getItem("private-gateway-theme")).toBe("dark");
    fireEvent.click(within(themes).getByRole("button", { name: "Светлая" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    fireEvent.click(within(themes).getByRole("button", { name: "Авто" }));
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(window.localStorage.getItem("private-gateway-theme")).toBeNull();
  });

  it("navigates with the phone tabs and the More sheet", async () => {
    await login();
    const tabs = screen.getByRole("navigation", { name: "Нижнее меню" });
    fireEvent.click(within(tabs).getByRole("button", { name: "Устройства" }));
    expect(await screen.findByText("Профили устройств")).toBeTruthy();
    expect(within(tabs).getByRole("button", { name: "Устройства" }).getAttribute("aria-current")).toBe("page");
    fireEvent.click(within(tabs).getByRole("button", { name: "Ещё" }));
    const sheet = screen.getByRole("dialog", { name: "Ещё" });
    fireEvent.click(within(sheet).getByRole("button", { name: "Сервер" }));
    expect(screen.queryByRole("dialog", { name: "Ещё" })).toBeNull();
    expect(await screen.findByText("Сервер подключения")).toBeTruthy();
    expect(screen.getByText("203.0.113.7:443")).toBeTruthy();
    fireEvent.click(within(tabs).getByRole("button", { name: "Ещё" }));
    fireEvent.click(within(screen.getByRole("dialog", { name: "Ещё" })).getByRole("button", { name: "Журнал действий" }));
    expect(await screen.findByText("История действий")).toBeTruthy();
    fireEvent.click(within(tabs).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("heading", { name: "Проверить безопасность" })).toBeTruthy();
    fireEvent.click(within(tabs).getByRole("button", { name: "Ещё" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Ещё" })).toBeNull();
    fireEvent.click(within(tabs).getByRole("button", { name: "Добавить" }));
    expect(await screen.findByRole("dialog", { name: "Добавить устройство" })).toBeTruthy();
  });

  it("runs privacy checks without requesting server health, speed or history", async () => {
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("heading", { name: "Проверить безопасность" })).toBeTruthy();
    const asked = () => fetchMock.mock.calls.map(([path]) => String(path));
    expect(asked().filter((path) => path.startsWith("https://") || path.startsWith("/api/v1/checks"))).toEqual([]);
    expect(screen.getByRole("link", { name: "ipleak.net" }).getAttribute("href")).toBe("https://ipleak.net/");
    expect(screen.queryByLabelText("Как подключены сейчас")).toBeNull();
    expect(screen.queryByRole("region", { name: "Скорость и задержка" })).toBeNull();
    expect(screen.queryByRole("region", { name: "История замеров" })).toBeNull();
    expect(screen.queryByText(/Браузер обращается к ipleak\.net/)).toBeNull();
    const help = screen.getByRole("button", { name: "О проверке безопасности" });
    fireEvent.click(help);
    expect(within(screen.getByRole("note", { name: "О проверке безопасности" })).getByText(/Браузер обращается к ipleak\.net/)).toBeTruthy();
    fireEvent.click(help);
    expect(screen.queryByText(/Браузер обращается к ipleak\.net/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Проверить безопасность" }));
    expect(await screen.findByText(/По выполненным проверкам проблем не найдено/)).toBeTruthy();
    expect(screen.getByText(/Сайты видят 203\.0\.113\.7 — ваш шлюз \(DigitalOcean, LLC, Нидерланды\)/)).toBeTruthy();
    expect(screen.getByText(/Ваши DNS-запросы видит Cloudflare, Inc\. — публичный DNS/)).toBeTruthy();
    expect(screen.getByText(/WebRTC в этом браузере выключен/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Скачать отчёт" })).toBeTruthy();
    expect(screen.queryByText("VLESS-трафик через сервер")).toBeNull();
    expect(asked().filter((path) => path.startsWith("/api/v1/checks") || path.startsWith("https://speed.cloudflare.com/"))).toEqual([]);
    expect(window.localStorage.getItem("private-gateway-performance-v1")).toBeNull();
    const outsideCalls = fetchMock.mock.calls.filter(([path]) => String(path).startsWith("https://"));
    expect(outsideCalls.some(([path]) => /^https:\/\/[0-9a-f]{40}-\d\.ipleak\.net\/dnsdetection\/$/.test(String(path)))).toBe(true);
    for (const [, options] of outsideCalls) {
      expect(options).toMatchObject({ credentials: "omit", referrerPolicy: "no-referrer" });
      expect(new Headers(options?.headers).has("Authorization")).toBe(false);
    }

    // Возврат из другого раздела не запускает проверку повторно.
    const requests = asked().length;
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер" })).toBeTruthy();
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("button", { name: "Проверить снова" })).toBeTruthy();
    expect(screen.getByText(/Сайты видят 203\.0\.113\.7 — ваш шлюз/)).toBeTruthy();
    expect(asked().length).toBe(requests);
  });

  it("runs server diagnostics separately and retains both cached results", async () => {
    const collect = vi.spyOn(deviceDiagnostics, "collectDeviceDiagnostics");
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер" })).toBeTruthy();
    expect(fetchMock.mock.calls.some(([path]) => String(path).startsWith("/api/v1/checks") || String(path).startsWith("https://"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Как подключены сейчас"), { target: { value: "wifi-vpn" } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер снова" })).toBeTruthy();
    expect(screen.getByText("VLESS-трафик через сервер")).toBeTruthy();
    expect(screen.getByRole("region", { name: "Канал до панели" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Интернет через текущее подключение" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Скачать отчёт" })).toBeTruthy();
    expect(collect).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("ipleak.net"))).toBe(false);
    expect(screen.queryByText("Что видят сайты")).toBeNull();
    expect(screen.queryByText("Что ещё видит любой сайт")).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/checks", expect.objectContaining({ headers: expect.objectContaining({ Authorization: "Bearer admin-token" }) }));
    const stored = window.localStorage.getItem("private-gateway-performance-v1");
    expect(JSON.parse(stored || "[]")[0].mode).toBe("wifi-vpn");
    expect(stored).not.toMatch(/admin-token|203\.0\.113|User-Agent|secret|profile/i);
    for (const [path, options] of fetchMock.mock.calls)
      if (String(path).startsWith("https://speed.cloudflare.com/")) {
        expect(options).toMatchObject({ credentials: "omit", referrerPolicy: "no-referrer" });
        expect(new Headers(options?.headers).has("Authorization")).toBe(false);
      }

    const serverRequests = fetchMock.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/checks")).length;
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("button", { name: "Проверить безопасность" })).toBeTruthy();
    expect(screen.queryByText("VLESS-трафик через сервер")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Проверить безопасность" }));
    expect(await screen.findByRole("button", { name: "Проверить снова" })).toBeTruthy();
    expect(fetchMock.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/checks")).length).toBe(serverRequests);
    expect(window.localStorage.getItem("private-gateway-performance-v1")).toBe(stored);
    expect(collect).toHaveBeenCalledOnce();
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер снова" })).toBeTruthy();
    expect(screen.getByText("VLESS-трафик через сервер")).toBeTruthy();
    expect(fetchMock.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/checks")).length).toBe(serverRequests);
  });

  it("stops a server measurement without losing privacy results or writing partial history", async () => {
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить безопасность" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    const privateRequests = fetchMock.mock.calls.filter(([path]) => String(path).includes("ipleak.net")).length;
    performanceBlocked = true;
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить сервер" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/checks/performance/ping"))).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    expect(await screen.findByText(/Проверка сервера остановлена/)).toBeTruthy();
    expect(window.localStorage.getItem("private-gateway-performance-v1")).toBeNull();
    await waitFor(() => expect(screen.getByRole("button", { name: "Проверить сервер" })).toBeTruthy());
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/checks/performance/download"))).toBe(false);
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("button", { name: "Проверить снова" })).toBeTruthy();
    expect(screen.getByText(/Сайты видят 203\.0\.113\.7 — ваш шлюз/)).toBeTruthy();
    expect(fetchMock.mock.calls.filter(([path]) => String(path).includes("ipleak.net")).length).toBe(privateRequests);
  });

  it("stops privacy checks without changing an existing speed history", async () => {
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить сервер" }));
    await screen.findByRole("button", { name: "Проверить сервер снова" });
    const stored = window.localStorage.getItem("private-gateway-performance-v1");
    privacyBlocked = true;
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить безопасность" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path]) => path === "https://ipv4.ipleak.net/json/")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    expect(await screen.findByText("Проверка безопасности остановлена.")).toBeTruthy();
    expect(window.localStorage.getItem("private-gateway-performance-v1")).toBe(stored);
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер снова" })).toBeTruthy();
  });

  it("shows completed privacy stages, preserves partial progress on cancellation and resets it on restart", async () => {
    vi.stubGlobal("RTCPeerConnection", undefined);
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(screen.queryByRole("progressbar", { name: "Ход проверки безопасности" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Проверить безопасность" }));
    const progress = () => screen.getByRole("progressbar", { name: "Ход проверки безопасности" }) as HTMLProgressElement;
    await waitFor(() => expect(progress().value).toBe(100));
    expect(progress().max).toBe(100);

    // Ответы проб удерживаем отдельно: процент меняется после ответа, а не по таймеру.
    const pending: Array<{ path: string; resolve: (response: Response) => void }> = [];
    const respond = fetchMock.getMockImplementation() as (path: string, options?: RequestInit) => Promise<Response>;
    fetchMock.mockImplementation((path: string, options: RequestInit = {}) => {
      if (path !== "https://ipv4.ipleak.net/json/" && path !== "https://ipv6.ipleak.net/json/" &&
          !/^https:\/\/[0-9a-f]{40}-\d\.ipleak\.net\/dnsdetection\/$/.test(path)) return respond(path, options);
      return new Promise<Response>((resolve, reject) => {
        pending.push({ path, resolve });
        options.signal?.addEventListener("abort", () => reject(options.signal?.reason), { once: true });
      });
    });
    fireEvent.click(screen.getByRole("button", { name: "Проверить снова" }));
    await waitFor(() => expect(pending).toHaveLength(12));
    // Собраны сведения об устройстве и завершена проверка недоступного WebRTC: 2 из 7 этапов.
    await waitFor(() => expect(progress().value).toBe(28));
    pending.find((request) => request.path === "https://ipv4.ipleak.net/json/")!.resolve(
      Response.json({ ip: visibleIP, ...networks[visibleIP] }),
    );
    await waitFor(() => expect(progress().value).toBe(42));
    fireEvent.click(screen.getByRole("button", { name: "Остановить" }));
    await screen.findByText("Проверка безопасности остановлена.");
    expect(progress().value).toBe(42);
    // Сохранённый результат не превращает остановленную повторную проверку в 100%.
    expect(screen.getByText(/Сайты видят 203\.0\.113\.7 — ваш шлюз/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Проверить снова" }));
    await waitFor(() => expect(pending).toHaveLength(24));
    await waitFor(() => expect(progress().value).toBe(28));
    for (const request of pending.slice(12)) {
      request.resolve(Response.json(request.path === "https://ipv4.ipleak.net/json/"
        ? { ip: visibleIP, ...networks[visibleIP] }
        : request.path === "https://ipv6.ipleak.net/json/"
          ? {}
          : { session: "test", ip: Object.fromEntries(dnsResolvers.map((ip) => [ip, 1])) }));
    }
    await waitFor(() => expect(progress().value).toBe(100));
    expect(progress().getAttribute("aria-valuetext")).toContain("100%. Проверка завершена.");
  });

  it("does not present cached success as a completed new check after a device probe fails", async () => {
    await login();
    fireEvent.click(within(screen.getByRole("navigation", { name: "Главное меню" })).getByRole("button", { name: "Проверка" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить безопасность" }));
    await screen.findByRole("button", { name: "Проверить снова" });
    expect((screen.getByRole("progressbar", { name: "Ход проверки безопасности" }) as HTMLProgressElement).value).toBe(100);
    const collect = vi.spyOn(deviceDiagnostics, "collectDeviceDiagnostics").mockImplementationOnce(() => {
      throw new Error("Device diagnostic failed");
    });
    try {
      fireEvent.click(screen.getByRole("button", { name: "Проверить снова" }));
      expect((await screen.findByRole("alert")).textContent).toContain("Device diagnostic failed");
      expect(screen.getByText("Не удалось завершить проверку.")).toBeTruthy();
      expect((screen.getByRole("progressbar", { name: "Ход проверки безопасности" }) as HTMLProgressElement).value).toBe(0);
      const previous = screen.getByText(/Предыдущая проверка:/).closest(".check-summary");
      expect(previous?.classList.contains("success-notice")).toBe(false);
      expect(screen.getByText(/Сайты видят 203\.0\.113\.7 — ваш шлюз/)).toBeTruthy();
    } finally {
      collect.mockRestore();
    }
  });

  it("keeps server health when speed services fail and clears only measurement history", async () => {
    performanceUnavailable = true;
    await login();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Сервер" }));
    fireEvent.change(await screen.findByLabelText("Как подключены сейчас"), { target: { value: "wifi-vpn" } });
    fireEvent.click(screen.getByRole("button", { name: "Проверить сервер" }));
    expect(await screen.findByRole("button", { name: "Проверить сервер снова" })).toBeTruthy();
    expect(screen.getByText("VLESS-трафик через сервер")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Интернет через текущее подключение" })).getAllByText("Недоступно").length).toBeGreaterThan(0);
    expect(JSON.parse(window.localStorage.getItem("private-gateway-performance-v1") || "[]")[0].mode).toBe("wifi-vpn");
    fireEvent.click(screen.getByRole("button", { name: "Очистить историю" }));
    expect(window.localStorage.getItem("private-gateway-performance-v1")).toBeNull();
    expect(screen.getByText("VLESS-трафик через сервер")).toBeTruthy();
    fireEvent.click(within(menu).getByRole("button", { name: "Проверка" }));
    expect(await screen.findByRole("button", { name: "Проверить безопасность" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "История замеров" })).toBeNull();
  });

  it("names the provider an IPv6 address and DNS go to past another VPN", async () => {
    visibleIP = "198.51.100.20";
    visibleIPv6 = "2001:db8::20";
    dnsResolvers = ["192.0.2.53"];
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Проверить безопасность" }));
    fireEvent.click(await screen.findByRole("button", { name: "Проверить безопасность" }));
    expect(await screen.findByText(/Найдено проблем: 1, замечаний: 2\./)).toBeTruthy();
    // Другой VPN — замечание, а не проблема.
    expect(screen.getByText(/M247 Europe SRL, Германия\. Это не ваш шлюз/)).toBeTruthy();
    expect(screen.getByText(/IPv6-адрес 2001:db8::20 — MTS PJSC, Россия, а IPv4 идёт через M247 Europe SRL/)).toBeTruthy();
    expect(screen.getByText(/Среди DNS-серверов обнаружены MTS PJSC, Россия/)).toBeTruthy();
  });

  it("shows setup instructions for each system and opens them from a profile", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Инструкции" }));
    expect(await screen.findByText("Как подключить устройство")).toBeTruthy();
    const systems = screen.getByRole("group", { name: "Система устройства" });
    fireEvent.click(within(systems).getByRole("button", { name: "Android" }));
    expect(within(systems).getByRole("button", { name: "Android" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "v2rayNG → Инструкция" }));
    expect(screen.getByText(/«Постоянная VPN» и «Блокировать подключения без VPN»/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Все приложения для Android/ }));
    expect(screen.getByText(/ещё не проверяли/)).toBeTruthy();
    fireEvent.click(within(systems).getByRole("button", { name: "iPhone и iPad" }));
    expect(screen.getByText(/Проверено на iPhone с Hiddify по LTE/)).toBeTruthy();
    const menu = screen.getByRole("navigation", { name: "Главное меню" });
    fireEvent.click(within(menu).getByRole("button", { name: "Обзор" }));
    fireEvent.click(await screen.findByRole("button", { name: "Профиль" }));
    fireEvent.click(await screen.findByRole("button", { name: "Как добавить ссылку в приложение" }));
    expect(await screen.findByText("Как подключить устройство")).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: /Профиль:/ })).toBeNull();
  });

  it("shows separate Xray traffic counters for the device", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Все устройства" }));
    expect(await screen.findByText("2 КБ / 1 МБ")).toBeTruthy();
  });

  it("shows only the login form before authentication", async () => {
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    expect(await screen.findByLabelText("Логин")).toBeTruthy();
    expect(screen.getByLabelText("Пароль")).toBeTruthy();
    expect(screen.queryByLabelText("Токен доступа")).toBeNull();
    expect(screen.queryByText(/admin-credentials\.py/)).toBeNull();
    expect(screen.queryByText(/не настроена/)).toBeNull();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/restore", { credentials: "same-origin" });
  });

  it("requires password before the authenticator code when configured", async () => {
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    expect(await screen.findByLabelText("Логин")).toBeTruthy();
    expect(screen.queryByLabelText("Токен доступа")).toBeNull();
    fireEvent.change(screen.getByLabelText("Логин"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Неверный логин");
    expect(screen.queryByText("Ваш приватный шлюз")).toBeNull();
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    fireEvent.change(await screen.findByLabelText("Код аутентификации"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
    await screen.findByText("Ваш приватный шлюз");
    expect(screen.queryByLabelText("Код аутентификации")).toBeNull();
  });

  it("opens noVNC through the existing administrator session", async () => {
    browserRunning = true;
    await login();
    const link = await screen.findByRole("link", { name: "Открыть" });
    expect(link.getAttribute("href")).toBe("https://gateway.example/browser/vnc.html");
    expect(screen.getByText(/отдельный пароль не нужен/)).toBeTruthy();
  });

  it("enrolls the authenticator in the browser after password verification", async () => {
    adminSetup = false;
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    fireEvent.change(await screen.findByLabelText("Логин"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "long-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    expect(await screen.findByText("TESTONLY1234567890")).toBeTruthy();
    expect(await screen.findByRole("img", { name: "QR-код приложения-аутентификатора" })).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Код аутентификации"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Подтвердить и войти" }));
    await screen.findByText("Ваш приватный шлюз");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/2fa/setup/confirm", expect.objectContaining({ method: "POST" }));
  });

  it("keeps the panel locked when authentication fails", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.change(await screen.findByLabelText("Логин"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "wrong-password" } });
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    await screen.findByRole("alert");
    expect(screen.queryByText("Ваш приватный шлюз")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain("Неверный логин или пароль");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  });

  it("keeps credentials out of persistent storage and clears query data on logout", async () => {
    const store = vi.spyOn(Storage.prototype, "setItem");
    await login();
    expect(store).not.toHaveBeenCalled();
    expect(queryClient.getQueryCache().getAll().length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Выйти из панели" }));
    expect(await screen.findByLabelText("Логин")).toBeTruthy();
    expect((screen.getByLabelText("Пароль") as HTMLInputElement).value).toBe("");
    expect(screen.queryByLabelText("Токен доступа")).toBeNull();
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
    expect(screen.queryByText("Phone")).toBeNull();
    expect(store).not.toHaveBeenCalled();
  });

  it("restores a remembered login after remount and forgets it on logout", async () => {
    const store = vi.spyOn(Storage.prototype, "setItem");
    const view = render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    fireEvent.change(await screen.findByLabelText("Логин"), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText("Пароль"), { target: { value: "long-password" } });
    fireEvent.click(screen.getByLabelText("Запомнить вход на этом устройстве на 30 дней"));
    fireEvent.click(screen.getByRole("button", { name: "Продолжить" }));
    fireEvent.change(await screen.findByLabelText("Код аутентификации"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
    await screen.findByText("Ваш приватный шлюз");
    expect(rememberedSession).toBe(true);
    expect(store).not.toHaveBeenCalled();

    view.unmount();
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
    await screen.findByText("Ваш приватный шлюз");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/restore", { credentials: "same-origin" });
    fireEvent.click(screen.getByRole("button", { name: "Выйти из панели" }));
    await screen.findByLabelText("Логин");
    expect(rememberedSession).toBe(false);
    expect(store).not.toHaveBeenCalled();
    store.mockRestore();
  });

  it("fetches a sensitive profile only on demand and removes it when hidden", async () => {
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: clipboard,
    });
    await login();
    expect(profileRequests).toBe(0);
    expect(screen.queryByDisplayValue(subscriptionLink())).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    await screen.findByDisplayValue(subscriptionLink());
    expect(profileRequests).toBe(1);
    expect(screen.queryByRole("img", { name: "QR-код ссылки-подписки" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Показать QR-код" }));
    const qr = await screen.findByRole("img", { name: "QR-код ссылки-подписки" });
    expect(qr.getAttribute("src")).toBe("data:image/png;base64,cXJjb2Rl");
    expect(toDataURL).toHaveBeenCalledWith(subscriptionLink(), expect.any(Object));
    fireEvent.click(screen.getByRole("button", { name: "Скопировать ссылку" }));
    await screen.findByRole("button", { name: "Скопировано" });
    expect(clipboard.writeText).toHaveBeenCalledWith(subscriptionLink());
    fireEvent.click(screen.getByRole("button", { name: "Скрыть профиль" }));
    await waitFor(() =>
      expect(screen.queryByDisplayValue(subscriptionLink())).toBeNull(),
    );
    expect(screen.queryByRole("img", { name: "QR-код ссылки-подписки" })).toBeNull();
    const cached = JSON.stringify(
      queryClient
        .getQueryCache()
        .getAll()
        .map((item) => item.state.data),
    );
    expect(cached).not.toContain("sensitive-device-credential");
    expect(cached).not.toContain("sensitive-subscription-token");
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    await screen.findByDisplayValue(subscriptionLink());
    expect(profileRequests).toBe(2);
    fireEvent.click(screen.getByRole("button", { name: "Выйти из панели" }));
    await screen.findByText("Вход в панель");
    expect(screen.queryByDisplayValue(subscriptionLink())).toBeNull();
  });

  it("replaces the subscription link only after confirmation", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    const oldLink = subscriptionLink();
    await screen.findByDisplayValue(oldLink);
    // Текстовое поле появляется до showModal: ждём доступную кнопку открытого окна.
    fireEvent.click(await screen.findByRole("button", { name: "Новая ссылка" }));
    expect(screen.getByText(/Прежняя ссылка сразу перестанет работать/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Отмена" }));
    expect(rotations).toBe(0);
    fireEvent.click(screen.getByRole("button", { name: "Новая ссылка" }));
    fireEvent.click(screen.getByRole("button", { name: "Заменить ссылку" }));
    await screen.findByDisplayValue(subscriptionLink());
    expect(subscriptionLink()).not.toBe(oldLink);
    expect(rotations).toBe(1);
    expect(screen.getByText("Ссылка заменена: прежняя больше не работает.")).toBeTruthy();
    expect(screen.queryByDisplayValue(oldLink)).toBeNull();
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
  });

  it("copies a VLESS link or client configuration without a new secret request", async () => {
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    await screen.findByDisplayValue(subscriptionLink());
    fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "uri" } });
    expect(screen.getByDisplayValue(profileURI)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Новая ссылка" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Показать QR-код" }));
    const qr = await screen.findByRole("img", { name: "QR-код профиля подключения" });
    expect(qr.getAttribute("src")).toBe("data:image/png;base64,cXJjb2Rl");
    expect(toDataURL).toHaveBeenCalledWith(profileURI, expect.any(Object));
    fireEvent.click(screen.getByRole("button", { name: "Скопировать ссылку" }));
    await waitFor(() => expect(clipboard.writeText).toHaveBeenLastCalledWith(profileURI));
    fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "sing_box_json" } });
    expect(screen.queryByRole("img", { name: "QR-код профиля подключения" })).toBeNull();
    expect(screen.getByDisplayValue(profileJSON)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Скопировать конфигурацию" }));
    await waitFor(() => expect(clipboard.writeText).toHaveBeenLastCalledWith(profileJSON));
    fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "mihomo_yaml" } });
    expect((screen.getByLabelText("Конфигурация клиента") as HTMLTextAreaElement).value).toBe(profileYAML);
    fireEvent.click(screen.getByRole("button", { name: "Скопировать конфигурацию" }));
    await waitFor(() => expect(clipboard.writeText).toHaveBeenLastCalledWith(profileYAML));
    expect(profileRequests).toBe(1);
  });

  it("requires explicit revoke confirmation and shows the unapplied revision afterward", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Отозвать" }));
    await screen.findByRole("dialog", { name: "Отозвать доступ?" });
    expect(revoked).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Отозвать доступ" }));
    await screen.findByText(
      "Доступ отозван. Примените конфигурацию, чтобы обновить Xray.",
    );
    await screen.findByText("Есть изменения");
    expect(revoked).toBe(true);
    expect(screen.queryByRole("button", { name: "Профиль" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Отозвать" })).toBeNull();
    expect(
      fetchMock.mock.calls.some(
        ([path, options]) =>
          path === "/api/v1/devices/d1/revoke" && options.method === "POST",
      ),
    ).toBe(true);
  });

  it("does not reapply an unchanged revision after a lost VPN response", async () => {
    interruptApply = true;
    await login();
    const applyButton = screen.getByRole("button", { name: "Применить конфигурацию" }) as HTMLButtonElement;
    expect(applyButton.disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Отозвать" }));
    fireEvent.click(await screen.findByRole("button", { name: "Отозвать доступ" }));
    await waitFor(() => expect(applyButton.disabled).toBe(false));
    fireEvent.click(applyButton);
    await screen.findByText("2 / 2");
    await waitFor(() => expect(applyButton.disabled).toBe(true));
    expect(screen.queryByText(/Связь с сервером прервалась/)).toBeNull();
    expect(fetchMock.mock.calls.filter(([path]) => path === "/api/v1/config/apply")).toHaveLength(1);
  });

  it("removes a revoked device only after Xray applied the revocation", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Отозвать" }));
    fireEvent.click(await screen.findByRole("button", { name: "Отозвать доступ" }));
    const removeButton = await screen.findByRole("button", { name: "Удалить" }) as HTMLButtonElement;
    expect(removeButton.disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Применить конфигурацию" }));
    await waitFor(() => expect(removeButton.disabled).toBe(false));
    fireEvent.click(removeButton);
    expect(await screen.findByRole("dialog", { name: "Удалить устройство?" })).toBeTruthy();
    expect(deleted).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Удалить устройство" }));
    await screen.findByText("Отозванное устройство удалено из списка. События остались в журнале.");
    expect(deleted).toBe(true);
    expect(screen.queryByText("Phone")).toBeNull();
    expect(fetchMock.mock.calls.filter(([path, options]) => path === "/api/v1/devices/d1" && options.method === "DELETE")).toHaveLength(1);
  });
});
