import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toDataURL } from "qrcode";
import { ProfileDialog, type RevealedProfile } from "./ProfileDialog";
import { browserURLForPanel } from "./api";

vi.mock("qrcode", () => ({ toDataURL: vi.fn().mockResolvedValue("data:image/png;base64,cXJjb2Rl") }));

const initial: RevealedProfile = {
  device: { id: "device-test", node_id: "node-test", name: "Test", created_at: "", revoked_at: null },
  port: 8443,
  uri: "vless://test-credential@vpn.example:8443",
  sing_box_json: '{"outbounds":[{"server_port":8443}]}',
  mihomo_yaml: "proxies:\n  - port: 8443",
  subscription_path: "/sub/test-subscription",
};

const xhttpProfile: RevealedProfile = {
  ...initial,
  xhttp_port: 8444,
  xhttp_uri: "vless://11111111-2222-3333-4444-555555555555@198.51.100.10:8444?type=xhttp&security=reality&path=%2Ftest-xhttp-path-0001&mode=auto&sni=example.org&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=0011223344556677#Test%20XHTTP",
};

function Dialog({ value = initial }: { value?: RevealedProfile }) {
  const [profile, setProfile] = useState(value);
  return <ProfileDialog token="test-session" profile={profile} onChange={setProfile}
    onClose={() => {}} onGuide={() => {}} />;
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
  const props = { token: "test-session", onChange: () => {}, onClose: () => {}, onGuide: () => {} };
  const { rerender } = render(<ProfileDialog {...props} profile={xhttpProfile} />);
  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "xhttp_uri" } });
  rerender(<ProfileDialog {...props} profile={initial} />);
  expect((screen.getByLabelText("Формат профиля") as HTMLSelectElement).value).toBe("uri");
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toBe(initial.uri);
  expect(screen.queryByRole("option", { name: /XHTTP/ })).toBeNull();
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

it("keeps noVNC on the panel origin and rejects non-web URLs", () => {
  expect(browserURLForPanel("https://panel.example/browser/vnc.html?autoconnect=1", "https://panel.example:8443"))
    .toBe("https://panel.example:8443/browser/vnc.html?autoconnect=1");
  expect(browserURLForPanel("https://panel.example:8443/browser/vnc.html", "https://panel.example"))
    .toBe("https://panel.example/browser/vnc.html");
  expect(browserURLForPanel("https://other.example/browser/", "https://panel.example:8443"))
    .toBe("https://other.example/browser/");
  expect(browserURLForPanel("javascript:alert(1)", "https://panel.example:8443")).toBeUndefined();
});
