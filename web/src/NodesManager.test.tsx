import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NodesManager } from "./NodesManager";
import type { Node } from "./api";

const node: Node = { id: "node-1", kind: "remote", name: "US VPS", host: "vpn.example", port: 8443,
  server_name: "target.example", public_key: "public", short_id: "abcd", desired_revision: 2, applied_revision: 1,
  connection_mode: "direct", xhttp: { port: 8444, public_key: "public-xhttp", short_id: "efab", path: "/test", mode: "auto" } };
const bridge = { host: "bridge.example", tcp_port: 443, xhttp_port: 8444 };
let cache: QueryClient;
beforeEach(() => {
  cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); };
});
afterEach(() => { cleanup(); cache.clear(); vi.unstubAllGlobals(); });
function mount(nodes = [node]) {
  return render(<QueryClientProvider client={cache}><NodesManager token="session" nodes={nodes} /></QueryClientProvider>);
}

it("removes purchased-proxy controls and does not load stored SOCKS credentials", () => {
  const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock);
  mount();
  expect(screen.queryByRole("button", { name: "Добавить прокси" })).toBeNull();
  expect(fetchMock).not.toHaveBeenCalled();
  expect(screen.getByRole("option", { name: "Через VPS-мост" })).toBeTruthy();
});

it("saves a bridge only after an explicit submit and keeps the saved route until confirmation", async () => {
  const fetchMock = vi.fn(async (_path: string, _options: RequestInit = {}) => Response.json({ node: { ...node, connection_mode: "bridge", bridge } }));
  vi.stubGlobal("fetch", fetchMock); mount();
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "bridge" } });
  expect((screen.getByRole("button", { name: "Сохранить способ подключения" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Адрес VPS-моста"), { target: { value: " bridge.example " } });
  expect(fetchMock).not.toHaveBeenCalled();
  expect(screen.getByText("Устройство → VPS → интернет")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("status");
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/nodes/node-1/connection");
  expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ mode: "bridge", bridge });
});

it("rejects overlapping TCP and XHTTP listener ports before submission", () => {
  const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock); mount();
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "bridge" } });
  fireEvent.change(screen.getByLabelText("Адрес VPS-моста"), { target: { value: bridge.host } });
  fireEvent.change(screen.getByLabelText("Порт XHTTP на мосту"), { target: { value: "443" } });
  expect(screen.getByText(/нужны разные порты/)).toBeTruthy();
  fireEvent.submit(screen.getByRole("form", { name: "Подключение к US VPS" }));
  expect(fetchMock).not.toHaveBeenCalled();
});

it("does not silently select direct after migration of an old SOCKS assignment", async () => {
  const fetchMock = vi.fn(async (_path: string, _options: RequestInit = {}) => Response.json({ node }));
  vi.stubGlobal("fetch", fetchMock); mount([{ ...node, connection_mode: "unconfigured" }]);
  expect((screen.getByLabelText("Способ подключения") as HTMLSelectElement).value).toBe("unconfigured");
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "direct" } });
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("status");
  expect(JSON.parse(fetchMock.mock.calls[0][1]!.body as string)).toEqual({ mode: "direct", bridge: null });
});

it("retains the saved bridge route if saving direct fails", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ error: "unavailable" }, { status: 503 })));
  mount([{ ...node, connection_mode: "bridge", bridge }]);
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "direct" } });
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByText("Устройство → bridge.example → VPS → интернет")).toBeTruthy();
});

it("does not claim a remote configuration is applied before its acknowledgement", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ status: "pending", desired_revision: 2 })));
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Применить" }));
  await screen.findByText(/Дождитесь подтверждения/);
  expect(screen.getByText("Ожидает применения")).toBeTruthy();
});

it("exports only saved bridge settings and leaves dirty edits unapplied", async () => {
  const fetchMock = vi.fn(async (_path: string) => Response.json({ version: 1, listeners: [] }));
  vi.stubGlobal("fetch", fetchMock);
  const create = vi.fn(() => "blob:bridge-test");
  const originalCreate = URL.createObjectURL;
  URL.createObjectURL = create;
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  try {
    mount([{ ...node, connection_mode: "bridge", bridge }]);
    fireEvent.change(screen.getByLabelText("Адрес VPS-моста"), { target: { value: "different.example" } });
    expect((screen.getByRole("button", { name: "Скачать настройку моста" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Адрес VPS-моста"), { target: { value: bridge.host } });
    fireEvent.click(screen.getByRole("button", { name: "Скачать настройку моста" }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/nodes/node-1/bridge-config");
    expect(create).toHaveBeenCalledOnce();
  } finally { URL.createObjectURL = originalCreate; click.mockRestore(); }
});
