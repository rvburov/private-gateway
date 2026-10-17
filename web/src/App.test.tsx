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

beforeEach(() => {
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
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  // jsdom lacks native modal behavior; keep the same open/close lifecycle for interaction tests.
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
  fetchMock = vi.fn(async (path: string, options: RequestInit = {}) => {
    if (path === "/api/v1/auth/login") {
      const input = JSON.parse(options.body as string) as { username: string; password: string };
      return input.username === "admin" && input.password === "long-password"
        ? Response.json({ status: adminSetup ? "totp_required" : "totp_setup_required", pending: "pending-token" })
        : Response.json({ error: "unauthorized" }, { status: 401 });
    }
    if (path === "/api/v1/auth/2fa/setup") return Response.json({ secret: "TESTONLY1234567890", otpauth_uri: "otpauth://totp/Private%20Gateway:admin?secret=TESTONLY1234567890" });
    if (path === "/api/v1/auth/2fa" || path === "/api/v1/auth/2fa/setup/confirm") {
      const input = JSON.parse(options.body as string) as { code: string };
      return options.headers && (options.headers as Record<string, string>).Authorization === "Bearer pending-token" && input.code === "123456"
        ? Response.json({ session: "admin-token" })
        : Response.json({ error: "unauthorized" }, { status: 401 });
    }
    if (path === "/api/v1/auth/logout") return Response.json({ status: "logged out" });
    const token = (options.headers as Record<string, string>)?.Authorization;
    if (token !== "Bearer admin-token")
      return Response.json({ error: "unauthorized" }, { status: 401 });
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
            host: "gateway.example",
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
    expect(screen.getByText("gateway.example:443")).toBeTruthy();
    fireEvent.click(within(tabs).getByRole("button", { name: "Ещё" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Ещё" })).toBeNull();
    fireEvent.click(within(tabs).getByRole("button", { name: "Добавить" }));
    expect(await screen.findByRole("dialog", { name: "Добавить устройство" })).toBeTruthy();
  });

  it("shows setup instructions for each system and opens them from a profile", async () => {
    await login();
    fireEvent.click(screen.getByRole("button", { name: "Инструкции" }));
    expect(await screen.findByText("Как подключить устройство")).toBeTruthy();
    const systems = screen.getByRole("group", { name: "Система устройства" });
    fireEvent.click(within(systems).getByRole("button", { name: "Android" }));
    expect(within(systems).getByRole("button", { name: "Android" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText(/«Постоянная VPN» и «Блокировать подключения без VPN»/)).toBeTruthy();
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
    expect(fetchMock).not.toHaveBeenCalled();
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
    expect(fetchMock).toHaveBeenCalledTimes(1);
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
    fireEvent.click(screen.getByRole("button", { name: "Новая ссылка" }));
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
