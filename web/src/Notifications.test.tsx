import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NotificationBell, Notifications } from "./Notifications";
import { browserPushState, disableDevicePush, enableDevicePush, inspectDevicePush, type DevicePush } from "./push";
import type { GatewayNotification, GatewayPushSubscription, NotificationPreferences } from "./notifications";

vi.mock("./push", () => ({
  browserPushState: vi.fn(), inspectDevicePush: vi.fn(), enableDevicePush: vi.fn(), disableDevicePush: vi.fn(),
}));

const current: GatewayPushSubscription = { id: "00000000-0000-4000-8000-000000000001", endpoint_hash: "0102030405060708", device: "Windows · Chrome", created_at: "2026-10-23T10:00:00Z", last_sent_at: null };
const clients: QueryClient[] = [];
let fetchMock: ReturnType<typeof vi.fn>;
let device: DevicePush;
let items: GatewayNotification[];
let preferences: NotificationPreferences;

function setup(children = <Notifications token="session-for-test" />) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}

beforeEach(() => {
  device = { state: "unsupported", hash: null, subscription: null };
  items = [{ id: "notification-1", category: "configuration", title: "Конфигурация применена", body: "Xray использует новые настройки.", created_at: "2026-10-23T10:00:00Z", read_at: null }];
  preferences = { server: true, devices: true, configuration: true };
  vi.mocked(browserPushState).mockReset().mockImplementation(() => device.state === "on" ? "off" : device.state);
  vi.mocked(inspectDevicePush).mockReset().mockImplementation(async () => device);
  vi.mocked(enableDevicePush).mockReset().mockResolvedValue(current);
  vi.mocked(disableDevicePush).mockReset().mockResolvedValue(undefined);
  fetchMock = vi.fn().mockImplementation(async (path: string, options: RequestInit = {}) => {
    if (path === "/api/v1/notifications") return Response.json({ items, unread: items.filter((item) => !item.read_at).length });
    if (path === "/api/v1/notifications/preferences") {
      if (options.method === "PUT") preferences = JSON.parse(options.body as string);
      return Response.json(preferences);
    }
    if (path === "/api/v1/notifications/push-subscriptions") return Response.json({ items: [current] });
    if (path === "/api/v1/notifications/read") {
      const { ids } = JSON.parse(options.body as string) as { ids?: string[] };
      items = items.map((item) => !ids || ids.includes(item.id) ? { ...item, read_at: "2026-10-23T10:05:00Z" } : item);
      return Response.json({ ok: true });
    }
    if (path === "/api/v1/notifications/test") return Response.json({ ok: true });
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(async () => {
  cleanup();
  for (const client of clients.splice(0)) { await client.cancelQueries(); client.clear(); }
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("notification center", () => {
  it("updates the bell and read status without asking for notification permission", async () => {
    const open = vi.fn();
    setup(<><NotificationBell token="session-for-test" onOpen={open} /><Notifications token="session-for-test" /></>);
    const bell = await screen.findByRole("button", { name: "Уведомления: непрочитанных 1" });
    fireEvent.click(bell);
    expect(open).toHaveBeenCalledOnce();
    expect(await screen.findByText("Xray использует новые настройки.")).toBeTruthy();
    expect(screen.getByText("Этот браузер не поддерживает push-уведомления.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Включить уведомления" })).toBeNull();
    expect(enableDevicePush).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Прочитать все" }));
    await screen.findByRole("button", { name: "Уведомления" });
    expect(screen.queryByRole("button", { name: "Прочитано: Конфигурация применена" })).toBeNull();
    expect(fetchMock.mock.calls.some(([path, options]) => path === "/api/v1/notifications/read" && options.method === "POST")).toBe(true);
    for (const [, options] of fetchMock.mock.calls) expect(new Headers(options.headers).get("Authorization")).toBe("Bearer session-for-test");
  });

  it("saves category preferences explicitly and keeps explanatory text collapsed", async () => {
    setup();
    const settings = screen.getByRole("region", { name: "Настройки уведомлений" });
    const devices = await within(settings).findByRole("checkbox", { name: /Устройства и доступ/ });
    const save = within(settings).getByRole("button", { name: "Сохранить настройки" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(devices);
    expect((save as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(save);
    await within(settings).findByText("Настройки сохранены.");
    expect(preferences).toEqual({ server: true, devices: false, configuration: true });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByText(/Полное отключение VPS требует внешнего мониторинга/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "О push-уведомлениях" }));
    expect(screen.getByText(/Полное отключение VPS требует внешнего мониторинга/)).toBeTruthy();
  });

  it("starts enabling synchronously from the click and only tests this device on demand", async () => {
    device = { state: "off", hash: null, subscription: null };
    vi.mocked(enableDevicePush).mockImplementation(async () => {
      device = { state: "on", hash: current.endpoint_hash, subscription: { endpoint: "https://push.example.test/device" } as PushSubscription };
      return current;
    });
    setup();
    const enable = await screen.findByRole("button", { name: "Включить уведомления" });
    await waitFor(() => expect((enable as HTMLButtonElement).disabled).toBe(false));
    expect(enableDevicePush).not.toHaveBeenCalled();
    fireEvent.click(enable);
    expect(enableDevicePush).toHaveBeenCalledWith("session-for-test", [current]);
    const test = await screen.findByRole("button", { name: "Тестовое уведомление" });
    await waitFor(() => expect((test as HTMLButtonElement).disabled).toBe(false));
    expect(fetchMock.mock.calls.some(([path]) => path === "/api/v1/notifications/test")).toBe(false);
    fireEvent.click(test);
    await screen.findByText("Тестовое уведомление отправлено в push-службу. Доставку проверьте на устройстве.");
    const testRequest = fetchMock.mock.calls.find(([path]) => path === "/api/v1/notifications/test");
    expect(JSON.parse(testRequest![1].body)).toEqual({ subscription_id: current.id });
    await waitFor(() => expect((screen.getByRole("button", { name: "Выключить на этом устройстве" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Выключить на этом устройстве" }));
    await waitFor(() => expect(disableDevicePush).toHaveBeenCalledWith("session-for-test", current, device.subscription));
  });

  it("offers installation on iOS and never offers a push prompt after denial", async () => {
    device = { state: "ios", hash: null, subscription: null };
    const install = vi.fn();
    const view = setup(<Notifications token="session-for-test" onInstall={install} />);
    fireEvent.click(await screen.findByRole("button", { name: "Установить приложение" }));
    expect(install).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Включить уведомления" })).toBeNull();
    view.unmount();
    device = { state: "denied", hash: null, subscription: null };
    setup();
    await screen.findByText("Уведомления запрещены в настройках браузера.");
    expect(screen.queryByRole("button", { name: "Включить уведомления" })).toBeNull();
    expect(enableDevicePush).not.toHaveBeenCalled();
  });

  it("shows subscription errors instead of claiming that push is enabled", async () => {
    device = { state: "off", hash: null, subscription: null };
    vi.mocked(enableDevicePush).mockRejectedValue(new Error("Сервер не сохранил подписку."));
    setup();
    const enable = await screen.findByRole("button", { name: "Включить уведомления" });
    await waitFor(() => expect((enable as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(enable);
    await screen.findByRole("alert");
    expect(screen.getByText("Сервер не сохранил подписку.")).toBeTruthy();
    expect(screen.queryByText("Уведомления включены.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Тестовое уведомление" })).toBeNull();
  });

  it("explains the server test cooldown in Russian without claiming delivery", async () => {
    device = { state: "on", hash: current.endpoint_hash, subscription: { endpoint: "https://push.example.test/device" } as PushSubscription };
    const original = fetchMock.getMockImplementation() as (path: string, options?: RequestInit) => Promise<Response>;
    fetchMock.mockImplementation((path: string, options: RequestInit) => path === "/api/v1/notifications/test"
      ? Promise.resolve(Response.json({ error: "wait one minute before another test" }, { status: 429 }))
      : original(path, options));
    setup();
    const test = await screen.findByRole("button", { name: "Тестовое уведомление" });
    await waitFor(() => expect((test as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(test);
    expect(await screen.findByText("Повторите тест через минуту.")).toBeTruthy();
    expect(screen.queryByText(/Тестовое уведомление отправлено/)).toBeNull();
  });
});
