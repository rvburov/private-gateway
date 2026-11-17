import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NodesManager } from "./NodesManager";
import type { EntryProxy, Node } from "./api";

const node: Node = { id: "node-1", kind: "remote", name: "US VPS", host: "vpn.example", port: 8443,
  server_name: "target.example", public_key: "public", short_id: "abcd", desired_revision: 2, applied_revision: 1 };
const proxy: EntryProxy = { id: "entry-1", name: "Вход", host: "entry.example", port: 1080,
  auth_type: "password", enabled: true, supports_udp: false };
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

it("writes credentials without retaining them in query or mutation caches", async () => {
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "POST"
    ? Response.json(proxy) : Response.json({ entry_proxies: [] }));
  vi.stubGlobal("fetch", fetchMock);
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Добавить прокси" }));
  for (const [label, value] of [["Название", "Вход"], ["Адрес SOCKS5", "entry.example"], ["Логин SOCKS5", "secret-user"], ["Пароль SOCKS5", "secret-pass"]]) {
    fireEvent.change(screen.getByLabelText(label), { target: { value } });
  }
  fireEvent.click(screen.getByRole("button", { name: "Сохранить прокси" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  const post = fetchMock.mock.calls.find(([, options]) => options?.method === "POST")!;
  expect(JSON.parse(post[1]!.body as string)).toMatchObject({ username: "secret-user", password: "secret-pass", supports_udp: false });
  expect(JSON.stringify(cache.getQueryCache().getAll())).not.toContain("secret-pass");
  expect(cache.getMutationCache().getAll()).toHaveLength(0);
});

it("preserves credentials when editing only the address", async () => {
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "PATCH"
    ? Response.json(proxy) : Response.json({ entry_proxies: [proxy] }));
  vi.stubGlobal("fetch", fetchMock);
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Изменить" }));
  expect(screen.queryByLabelText("Пароль SOCKS5")).toBeNull();
  fireEvent.change(screen.getByLabelText("Адрес SOCKS5"), { target: { value: "new-entry.example" } });
  fireEvent.click(screen.getByRole("button", { name: "Сохранить прокси" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  const patch = fetchMock.mock.calls.find(([, options]) => options?.method === "PATCH")!;
  const body = JSON.parse(patch[1]!.body as string);
  expect(body.host).toBe("new-entry.example");
  expect(body).not.toHaveProperty("password"); expect(body).not.toHaveProperty("username");
});

it("keeps a disabled assigned proxy selected and prevents deleting it", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ entry_proxies: [{ ...proxy, enabled: false }] })));
  mount([{ ...node, entry_proxy_id: proxy.id }]);
  await screen.findByText(/Прокси выключен/);
  expect((screen.getByLabelText("Входной прокси") as HTMLSelectElement).value).toBe(proxy.id);
  expect((screen.getByRole("button", { name: "Удалить" }) as HTMLButtonElement).disabled).toBe(true);
});

it("does not report remote configuration applied before an agent acknowledgement", async () => {
  vi.stubGlobal("fetch", vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "POST"
    ? Response.json({ status: "pending", desired_revision: 2 }) : Response.json({ entry_proxies: [] })));
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Применить" }));
  await screen.findByText(/Дождитесь подтверждения/);
  expect(screen.getByText("Ожидает применения")).toBeTruthy();
});

it("selects the connection mode explicitly and saves the selected proxy only on submit", async () => {
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "PUT"
    ? Response.json({ node: { ...node, entry_proxy_id: proxy.id } }) : Response.json({ entry_proxies: [proxy] }));
  vi.stubGlobal("fetch", fetchMock);
  mount();
  expect((screen.getByLabelText("Способ подключения") as HTMLSelectElement).value).toBe("direct");
  expect(screen.queryByLabelText("Входной прокси")).toBeNull();
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "proxy" } });
  await waitFor(() => expect((screen.getByLabelText("Входной прокси") as HTMLSelectElement).disabled).toBe(false));
  expect((screen.getByRole("button", { name: "Сохранить способ подключения" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Входной прокси"), { target: { value: proxy.id } });
  expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "PUT")).toHaveLength(0);
  expect(screen.getByText("Устройство → VPS → интернет")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("status");
  const put = fetchMock.mock.calls.find(([, options]) => options?.method === "PUT")!;
  expect(put[0]).toBe("/api/v1/nodes/node-1/entry-proxy");
  expect(JSON.parse(put[1]!.body as string)).toEqual({ entry_proxy_id: proxy.id });
  expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "DELETE" || options?.method === "POST")).toHaveLength(0);
});

it("can explicitly switch to direct even when the proxy list cannot be loaded", async () => {
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "PUT"
    ? Response.json({ node: { ...node, entry_proxy_id: null } }) : Response.json({ error: "unavailable" }, { status: 503 }));
  vi.stubGlobal("fetch", fetchMock);
  mount([{ ...node, entry_proxy_id: proxy.id }]);
  await waitFor(() => expect(cache.getQueryState(["entry-proxies"])?.status).toBe("error"));
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "direct" } });
  expect((screen.getByRole("button", { name: "Сохранить способ подключения" }) as HTMLButtonElement).disabled).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("status");
  const writes = fetchMock.mock.calls.filter(([, options]) => options?.method === "PUT");
  expect(writes).toHaveLength(1);
  expect(JSON.parse(writes[0][1]!.body as string)).toEqual({ entry_proxy_id: null });
  expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "DELETE")).toHaveLength(0);
});

it("retains the saved proxy route when saving direct fails", async () => {
  vi.stubGlobal("fetch", vi.fn(async (_path: string, options: RequestInit = {}) => options.method === "PUT"
    ? Response.json({ error: "unavailable" }, { status: 503 }) : Response.json({ entry_proxies: [proxy] })));
  mount([{ ...node, entry_proxy_id: proxy.id }]);
  await screen.findByText("Устройство → Вход → VPS → интернет");
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "direct" } });
  fireEvent.click(screen.getByRole("button", { name: "Сохранить способ подключения" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("status")).toBeNull();
  expect(screen.getByText("Устройство → Вход → VPS → интернет")).toBeTruthy();
});

it("does not save a disabled proxy as the new connection mode", async () => {
  const fetchMock = vi.fn(async () => Response.json({ entry_proxies: [{ ...proxy, enabled: false }] }));
  vi.stubGlobal("fetch", fetchMock);
  mount();
  fireEvent.change(screen.getByLabelText("Способ подключения"), { target: { value: "proxy" } });
  await screen.findByText("Вход (выключен)");
  fireEvent.change(screen.getByLabelText("Входной прокси"), { target: { value: proxy.id } });
  expect((screen.getByRole("button", { name: "Сохранить способ подключения" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.submit(screen.getByRole("form", { name: "Подключение к US VPS" }));
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
