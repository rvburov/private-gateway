import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toDataURL } from "qrcode";
import { ProfileDialog, type RevealedProfile } from "./ProfileDialog";
import { browserURLForPanel, type Node } from "./api";

vi.mock("qrcode", () => ({ toDataURL: vi.fn().mockResolvedValue("data:image/png;base64,cXJjb2Rl") }));

const initial: RevealedProfile = {
  device: { id: "device-test", node_id: "node-test", name: "Test", preferred_exit: "server", created_at: "", revoked_at: null },
  exit: "server",
  port: 8443,
  uri: "vless://test-credential@vpn.example:8443",
  sing_box_json: '{"outbounds":[{"server_port":8443}]}',
  mihomo_yaml: "proxies:\n  - port: 8443",
  subscription_path: "/sub/test-subscription",
  expected_exit_ip: "8.8.8.8",
};

const expectedIPs = { expected_server_ip: "8.8.8.8", expected_proxy_ip: "1.1.1.1" };

const xhttpProfile: RevealedProfile = {
  ...initial,
  xhttp_port: 8444,
  xhttp_uri: "vless://11111111-2222-3333-4444-555555555555@198.51.100.10:8444?type=xhttp&security=reality&path=%2Ftest-xhttp-path-0001&mode=auto&sni=example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=0011223344556677#Test%20XHTTP",
};

function Dialog({ value = initial, proxyAvailable = false, node = expectedIPs }: {
  value?: RevealedProfile;
  proxyAvailable?: boolean;
  node?: Pick<Node, "expected_server_ip" | "expected_proxy_ip"> | null;
}) {
  const [profile, setProfile] = useState(value);
  return <ProfileDialog token="test-session" profile={profile} proxyAvailable={proxyAvailable} node={node ?? undefined} onChange={setProfile}
    onClose={() => {}} />;
}

function subscriptionURL(path = initial.subscription_path) {
  const url = new URL(path, window.location.origin);
  url.port = "8443";
  return url.toString();
}

beforeEach(() => {
  vi.clearAllMocks();
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); };
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("uses one port for the subscription, exported formats and both QR codes", async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog />);
  expect(screen.queryByRole("option", { name: /XHTTP/ })).toBeNull();
  expect(screen.queryByLabelText("Вход на сервер")).toBeNull();
  expect(screen.queryByText("Основной вход")).toBeNull();
  expect(screen.queryByText(/Резервный вход/)).toBeNull();
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(subscriptionURL());
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await waitFor(() => expect(toDataURL).toHaveBeenLastCalledWith(subscriptionURL(), expect.any(Object)));
  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "uri" } });
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(initial.uri);
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await waitFor(() => expect(toDataURL).toHaveBeenLastCalledWith(initial.uri, expect.any(Object)));
  for (const format of ["sing_box_json", "mihomo_yaml"] as const) {
    fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: format } });
    expect((screen.getByLabelText("Конфигурация клиента") as HTMLTextAreaElement).value)
      .toBe(initial[format]);
  }
  expect(fetchMock).not.toHaveBeenCalled();
});

it("copies and renders QR for the selected transport while retaining one subscription", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  const fetchMock = vi.fn();
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog value={xhttpProfile} />);
  const details = within(screen.getByRole("region", { name: "Параметры подключения" }));
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
  expect(details.getByText("XHTTP: 8444; TCP: 8443")).toBeTruthy();

  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "xhttp_uri" } });
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(xhttpProfile.xhttp_uri);
  expect(details.getByText("8444")).toBeTruthy();
  expect(details.getByText("REALITY, без Vision")).toBeTruthy();
  expect(details.getByText("Не измерено")).toBeTruthy();
  expect(details.getByText(/Скорость требует замера/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Скачать файл" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await waitFor(() => expect(toDataURL).toHaveBeenLastCalledWith(xhttpProfile.xhttp_uri, expect.any(Object)));
  fireEvent.click(screen.getByRole("button", { name: "Скопировать ссылку" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(xhttpProfile.xhttp_uri));

  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "uri" } });
  expect(screen.queryByRole("img", { name: "QR-код профиля подключения" })).toBeNull();
  expect(details.getByText("8443")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Скопировать ссылку" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(initial.uri));
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await waitFor(() => expect(toDataURL).toHaveBeenLastCalledWith(initial.uri, expect.any(Object)));

  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "subscription" } });
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
  expect(fetchMock).not.toHaveBeenCalled();
});

it("retains TCP configuration files when an XHTTP link is available", () => {
  render(<Dialog value={xhttpProfile} />);
  for (const format of ["sing_box_json", "mihomo_yaml"] as const) {
    fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: format } });
    expect((screen.getByLabelText("Конфигурация клиента") as HTMLTextAreaElement).value).toBe(initial[format]);
    const details = within(screen.getByRole("region", { name: "Параметры подключения" }));
    expect(details.getByText("TCP")).toBeTruthy();
    expect(details.getByText("REALITY + Vision")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Скачать файл" })).toBeTruthy();
  }
});

it("falls back to TCP if an updated profile no longer exposes XHTTP", () => {
  const props = { token: "test-session", proxyAvailable: false, onChange: () => {}, onClose: () => {} };
  const { rerender } = render(<ProfileDialog {...props} profile={xhttpProfile} />);
  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "xhttp_uri" } });
  rerender(<ProfileDialog {...props} profile={initial} />);
  expect((screen.getByLabelText("Формат профиля") as HTMLSelectElement).value).toBe("uri");
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(initial.uri);
  expect(screen.queryByRole("option", { name: /XHTTP/ })).toBeNull();
});

it("switches between separate server and proxy profiles without changing the format list", async () => {
  const proxyPath = "/sub/proxy/test-subscription";
  const proxyURI = "vless://proxy-test-credential@vpn.example:8443";
  const fetchMock = vi.fn(async (path: string, options: RequestInit = {}) => {
    if (path === "/api/v1/devices/device-test" && options.method === "PATCH")
      return Response.json({ ...initial.device, preferred_exit: JSON.parse(options.body as string).preferred_exit });
    if (path.endsWith("/profile?exit=proxy"))
      return Response.json({ ...xhttpProfile, uri: proxyURI, subscription_path: proxyPath, expected_exit_ip: "1.1.1.1" });
    if (path.endsWith("/profile?exit=server"))
      return Response.json(initial);
    throw new Error("Unexpected request");
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog value={xhttpProfile} proxyAvailable />);
  expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("server");
  const format = screen.getByLabelText("Формат профиля") as HTMLSelectElement;
  expect(format.options).toHaveLength(5);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await waitFor(() => expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("proxy"));
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL(proxyPath));
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/devices/device-test/profile?exit=proxy");
  expect(format.options).toHaveLength(5);
  fireEvent.change(format, { target: { value: "uri" } });
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(proxyURI);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "server" } });
  await waitFor(() => expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("server"));
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(initial.uri);
  expect(fetchMock.mock.calls[2][0]).toBe("/api/v1/devices/device-test/profile?exit=server");
  expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "PATCH").map(([, options]) => JSON.parse(options!.body as string)))
    .toEqual([{ preferred_exit: "proxy" }, { preferred_exit: "server" }]);
});

it("keeps the server profile available when the proxy is not configured", () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog />);
  expect((screen.getByRole("option", { name: /^IP прокси/ }) as HTMLOptionElement).disabled).toBe(true);
  expect(screen.getByText(/Прокси ещё не настроен или конфигурация не применена/)).toBeTruthy();
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
  expect(fetchMock).not.toHaveBeenCalled();
});

it("keeps the proxy exit in the subscription link after rotating both links", async () => {
  const fetchMock = vi.fn(async (_path: string) => Response.json({ subscription_path: "/sub/proxy/test-rotated" }));
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog value={{ ...xhttpProfile, exit: "proxy", subscription_path: "/sub/proxy/test-subscription" }} proxyAvailable />);
  fireEvent.click(screen.getByText("Новая ссылка"));
  expect(screen.getByText(/Прежние ссылки для IP сервера и IP прокси/)).toBeTruthy();
  fireEvent.click(screen.getByText("Заменить ссылку"));
  await waitFor(() => expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(subscriptionURL("/sub/proxy/test-rotated")));
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/devices/device-test/subscription?exit=proxy");
  expect(screen.getByText("Ссылки обоих выходов заменены: прежние больше не работают.")).toBeTruthy();
});

it("keeps port 8443 after rotating a legacy subscription link", async () => {
  const fetchMock = vi.fn(async (path: string) => {
    if (path.endsWith("/subscription"))
      return Response.json({ subscription_path: "/sub/test-rotated" });
    throw new Error("Unexpected request");
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog value={{ ...initial, subscription_path: "/sub/test-subscription?port=8443" }} />);
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(subscriptionURL("/sub/test-subscription?port=8443"));
  fireEvent.click(screen.getByText("Новая ссылка"));
  fireEvent.click(screen.getByText("Заменить ссылку"));
  await waitFor(() => expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(subscriptionURL("/sub/test-rotated")));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/devices/device-test/subscription");
  expect(screen.getByText("Ссылка заменена: прежняя больше не работает.")).toBeTruthy();
});

it("keeps the current subscription when rotation fails", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ error: "Rotation failed" }, { status: 503 })));
  render(<Dialog />);
  fireEvent.click(screen.getByText("Новая ссылка"));
  fireEvent.click(screen.getByText("Заменить ссылку"));
  await screen.findByText("Rotation failed");
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
});

it("aligns the selected exit, expected IP, subscription and QR after the preference is saved", async () => {
  const fetchMock = vi.fn(async (path: string, options: RequestInit = {}) => {
    if (path.endsWith("/profile?exit=proxy")) return Response.json({
      ...initial, uri: "vless://proxy-credential@vpn.example:8443", subscription_path: "/sub/proxy/test-subscription",
      expected_exit_ip: "1.1.1.1",
    });
    if (options.method === "PATCH") return Response.json({ ...initial.device, preferred_exit: "proxy" });
    throw new Error(`Unexpected request: ${path}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog proxyAvailable />);
  expect(screen.getByRole("option", { name: "IP сервера — 8.8.8.8" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "IP прокси — 1.1.1.1" })).toBeTruthy();
  const expected = () => within(screen.getByRole("region", { name: "Ожидаемый выходной IP" }));
  expect(expected().getByText("8.8.8.8")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await screen.findByRole("img", { name: "QR-код ссылки-подписки" });
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await waitFor(() => expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("proxy"));
  expect(expected().getByText("1.1.1.1")).toBeTruthy();
  expect(expected().queryByText("8.8.8.8")).toBeNull();
  expect(screen.queryByRole("img", { name: "QR-код ссылки-подписки" })).toBeNull();
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(subscriptionURL("/sub/proxy/test-subscription"));
  fireEvent.click(screen.getByRole("button", { name: "Показать QR-код" }));
  await waitFor(() => expect(toDataURL).toHaveBeenLastCalledWith(subscriptionURL("/sub/proxy/test-subscription"), expect.any(Object)));
  expect(fetchMock.mock.calls.map(([path, options]) => [path, options?.method ?? "GET"]))
    .toEqual([["/api/v1/devices/device-test/profile?exit=proxy", "GET"], ["/api/v1/devices/device-test", "PATCH"]]);
});

it("does not guess an unknown exit IP from a proxy endpoint or a URI", () => {
  render(<Dialog node={null} value={{ ...initial, expected_exit_ip: undefined,
    uri: "vless://test-credential@9.9.9.9:8443" }} proxyAvailable />);
  expect(screen.getByRole("option", { name: "IP сервера — IP не указан" })).toBeTruthy();
  expect(screen.getByRole("option", { name: "IP прокси — IP не указан" })).toBeTruthy();
  const expected = within(screen.getByRole("region", { name: "Ожидаемый выходной IP" }));
  expect(expected.getByText("IP не указан")).toBeTruthy();
  expect(expected.queryByText("9.9.9.9")).toBeNull();
});

it("uses the profile's expected address consistently when node metadata has changed", () => {
  render(<Dialog node={{ expected_server_ip: "9.9.9.9", expected_proxy_ip: "1.1.1.1" }} />);
  expect(screen.getByRole("option", { name: "IP сервера — 8.8.8.8" })).toBeTruthy();
  expect(screen.queryByRole("option", { name: "IP сервера — 9.9.9.9" })).toBeNull();
  const expected = within(screen.getByRole("region", { name: "Ожидаемый выходной IP" }));
  expect(expected.getByText("8.8.8.8")).toBeTruthy();
  expect(expected.queryByText("9.9.9.9")).toBeNull();
});

it.each(["profile", "preference"])("retains the previous selection, IP and link if the %s request fails", async (failure) => {
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => {
    if (failure === "profile" || options.method === "PATCH")
      return Response.json({ error: "Unavailable" }, { status: 503 });
    return Response.json({ ...initial, subscription_path: "/sub/proxy/uncommitted", expected_exit_ip: "1.1.1.1" });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog proxyAvailable />);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await screen.findByRole("alert");
  expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("server");
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
  const expected = within(screen.getByRole("region", { name: "Ожидаемый выходной IP" }));
  expect(expected.getByText("8.8.8.8")).toBeTruthy();
  expect(expected.queryByText("1.1.1.1")).toBeNull();
  expect(fetchMock.mock.calls.filter(([, options]) => options?.method === "PATCH")).toHaveLength(failure === "profile" ? 0 : 1);
});

it("keeps export actions disabled until the selected exit has been persisted", async () => {
  let save!: (response: Response) => void;
  const fetchMock = vi.fn(async (_path: string, options: RequestInit = {}) => {
    if (options.method === "PATCH") return new Promise<Response>((resolve) => { save = resolve; });
    return Response.json({ ...initial, subscription_path: "/sub/proxy/test-subscription", expected_exit_ip: "1.1.1.1" });
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog proxyAvailable />);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await waitFor(() => expect(save).toBeDefined());
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe("");
  expect((screen.getByRole("button", { name: "Скопировать ссылку" }) as HTMLButtonElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Показать QR-код" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => { save(Response.json({ ...initial.device, preferred_exit: "proxy" })); });
  await waitFor(() => expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("proxy"));
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL("/sub/proxy/test-subscription"));
});

it.each(["profile", "preference"])("aborts a late %s response when the profile is hidden", async (pendingStep) => {
  let finish!: (response: Response) => void;
  let signal: AbortSignal | undefined;
  const changed = vi.fn();
  vi.stubGlobal("fetch", vi.fn(async (_path: string, options: RequestInit = {}) => {
    if ((pendingStep === "preference") === (options.method === "PATCH")) {
      signal = options.signal as AbortSignal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    }
    return Response.json({ ...initial, expected_exit_ip: "1.1.1.1", subscription_path: "/sub/proxy/test-subscription" });
  }));
  const view = render(<ProfileDialog token="test-session" profile={initial} node={expectedIPs}
    proxyAvailable onChange={changed} onClose={() => {}} />);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await waitFor(() => expect(finish).toBeDefined());
  view.unmount();
  expect(signal?.aborted).toBe(true);
  await act(async () => { finish(Response.json(pendingStep === "profile"
    ? { ...initial, expected_exit_ip: "1.1.1.1", subscription_path: "/sub/proxy/test-subscription" }
    : { ...initial.device, preferred_exit: "proxy" })); });
  expect(changed).not.toHaveBeenCalled();
});

it.each([
  { ...initial.device, id: "another-device", preferred_exit: "proxy" },
  { ...initial.device, preferred_exit: "server" },
])("rejects a preference response that does not confirm this device and selected exit", async (saved) => {
  vi.stubGlobal("fetch", vi.fn(async (_path: string, options: RequestInit = {}) => Response.json(options.method === "PATCH"
    ? saved : { ...initial, expected_exit_ip: "1.1.1.1", subscription_path: "/sub/proxy/unconfirmed" })));
  render(<Dialog proxyAvailable />);
  fireEvent.change(screen.getByLabelText("Выходной IP"), { target: { value: "proxy" } });
  await screen.findByRole("alert");
  expect((screen.getByLabelText("Выходной IP") as HTMLSelectElement).value).toBe("server");
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(subscriptionURL());
});

it("keeps noVNC on the panel origin and rejects non-web URLs", () => {
  expect(browserURLForPanel("https://panel.example/browser/vnc.html?autoconnect=1", "https://panel.example:8443"))
    .toBe("https://panel.example:8443/browser/vnc.html?autoconnect=1");
  expect(browserURLForPanel("https://panel.example:8443/browser/vnc.html", "https://panel.example"))
    .toBe("https://panel.example/browser/vnc.html");
  expect(browserURLForPanel("https://other.example/browser/", "https://panel.example:8443"))
    .toBe("https://other.example/browser/");
  expect(browserURLForPanel("javascript:alert(1)", "https://panel.example:8443")).toBeUndefined();
});
