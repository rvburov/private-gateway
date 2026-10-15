import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { toDataURL } from "qrcode";
import App from "./App";

vi.mock("qrcode", () => ({
  toDataURL: vi.fn().mockResolvedValue("data:image/png;base64,cXJjb2Rl"),
}));

const profileURI =
  "vless://sensitive-device-credential@gateway.example:443?security=reality#Phone";
let revoked = false;
let revision = 1;
let profileRequests = 0;
let queryClient: QueryClient;
let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  revoked = false;
  revision = 1;
  profileRequests = 0;
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
    const token = (options.headers as Record<string, string>)?.Authorization;
    if (token !== "Bearer admin-token")
      return Response.json({ error: "unauthorized" }, { status: 401 });
    if (path === "/api/v1/status")
      return Response.json({
        database: "ok",
        node_id: "n1",
        devices: 1,
        active_devices: revoked ? 0 : 1,
        desired_revision: revision,
        applied_revision: 1,
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
        devices: [
          {
            id: "d1",
            name: "Phone",
            node_id: "n1",
            created_at: "2026-01-01T00:00:00Z",
            revoked_at: revoked ? "2026-01-02T00:00:00Z" : null,
          },
        ],
      });
    if (path === "/api/v1/browser")
      return Response.json({ running: false, url: "" });
    if (path === "/api/v1/devices/d1/profile") {
      profileRequests++;
      return Response.json({ uri: profileURI });
    }
    if (path === "/api/v1/devices/d1/revoke" && options.method === "POST") {
      revoked = true;
      revision = 2;
      return Response.json({ id: "d1", revoked_at: "2026-01-02T00:00:00Z" });
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
  fireEvent.change(screen.getByLabelText("Токен доступа"), {
    target: { value: "admin-token" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
  await screen.findByText("Ваш приватный шлюз");
  await screen.findByText("Phone");
}

describe("administrator panel", () => {
  it("keeps the panel locked when authentication fails", async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>,
    );
    fireEvent.change(screen.getByLabelText("Токен доступа"), {
      target: { value: "wrong-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Открыть панель" }));
    await screen.findByRole("alert");
    expect(screen.queryByText("Ваш приватный шлюз")).toBeNull();
    expect(screen.getByRole("alert").textContent).toContain(
      "Токен недействителен",
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  });

  it("keeps credentials out of persistent storage and clears query data on logout", async () => {
    const store = vi.spyOn(Storage.prototype, "setItem");
    await login();
    expect(store).not.toHaveBeenCalled();
    expect(queryClient.getQueryCache().getAll().length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Выйти из панели" }));
    const tokenInput = (await screen.findByLabelText(
      "Токен доступа",
    )) as HTMLInputElement;
    expect(tokenInput.value).toBe("");
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
    expect(screen.queryByDisplayValue(profileURI)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    await screen.findByDisplayValue(profileURI);
    expect(profileRequests).toBe(1);
    expect(screen.queryByRole("img", { name: "QR-код профиля подключения" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
    const qr = await screen.findByRole("img", { name: "QR-код профиля подключения" });
    expect(qr.getAttribute("src")).toBe("data:image/png;base64,cXJjb2Rl");
    expect(toDataURL).toHaveBeenCalledWith(profileURI, expect.any(Object));
    fireEvent.click(screen.getByRole("button", { name: "Скопировать URI" }));
    await screen.findByRole("button", { name: "Скопировано" });
    expect(clipboard.writeText).toHaveBeenCalledWith(profileURI);
    fireEvent.click(screen.getByRole("button", { name: "Скрыть профиль" }));
    await waitFor(() =>
      expect(screen.queryByDisplayValue(profileURI)).toBeNull(),
    );
    expect(screen.queryByRole("img", { name: "QR-код профиля подключения" })).toBeNull();
    expect(
      JSON.stringify(
        queryClient
          .getQueryCache()
          .getAll()
          .map((item) => item.state.data),
      ),
    ).not.toContain("sensitive-device-credential");
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Профиль" }));
    await screen.findByDisplayValue(profileURI);
    expect(profileRequests).toBe(2);
    fireEvent.click(screen.getByRole("button", { name: "Выйти из панели" }));
    await screen.findByText("Вход в панель");
    expect(screen.queryByDisplayValue(profileURI)).toBeNull();
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
});
