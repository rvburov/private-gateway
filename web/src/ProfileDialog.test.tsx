import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
