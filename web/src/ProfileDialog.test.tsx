import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ProfileDialog, type RevealedProfile } from "./ProfileDialog";
import { browserURLForPanel } from "./api";

vi.mock("qrcode", () => ({ toDataURL: vi.fn().mockResolvedValue("data:image/png;base64,cXJjb2Rl") }));

const initial: RevealedProfile = {
  device: { id: "device-test", node_id: "node-test", name: "Test", created_at: "", revoked_at: null },
  port: 443, fallback_port: 8443,
  uri: "vless://test-credential@vpn.example:443",
  sing_box_json: "{}", mihomo_yaml: "proxies: []",
  subscription_path: "/sub/test-subscription?port=primary",
};

function Dialog() {
  const [profile, setProfile] = useState(initial);
  return <ProfileDialog token="test-session" profile={profile} onChange={setProfile}
    onClose={() => {}} onGuide={() => {}} />;
}

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); };
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("imports and rotates the fallback subscription on port 8443", async () => {
  const fetchMock = vi.fn(async (path: string) => {
    if (path.endsWith("/profile?port=8443")) return Response.json({
      ...initial, port: 8443, uri: initial.uri.replace(":443", ":8443"),
      subscription_path: "/sub/test-subscription?port=8443",
    });
    if (path.endsWith("/subscription?port=8443"))
      return Response.json({ subscription_path: "/sub/test-rotated?port=8443" });
    throw new Error("Unexpected request");
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<Dialog />);
  fireEvent.change(screen.getByLabelText("Вход на сервер"), { target: { value: "8443" } });
  const expected = new URL("/sub/test-subscription?port=8443", window.location.origin);
  expected.port = "8443";
  await waitFor(() => expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(expected.toString()));
  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "uri" } });
  expect((screen.getByLabelText("Ссылка подключения") as HTMLTextAreaElement).value).toContain(":8443");
  fireEvent.change(screen.getByLabelText("Формат профиля"), { target: { value: "subscription" } });
  fireEvent.click(screen.getByText("Новая ссылка"));
  fireEvent.click(screen.getByText("Заменить ссылку"));
  expected.pathname = "/sub/test-rotated";
  await waitFor(() => expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value).toBe(expected.toString()));
  expect(fetchMock).toHaveBeenCalledTimes(2);
});

it("keeps the previous profile when fallback export fails", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ error: "Export failed" }, { status: 503 })));
  render(<Dialog />);
  fireEvent.change(screen.getByLabelText("Вход на сервер"), { target: { value: "8443" } });
  await screen.findByText("Export failed");
  expect((screen.getByLabelText("Вход на сервер") as HTMLSelectElement).value).toBe("primary");
  expect((screen.getByLabelText("Ссылка-подписка") as HTMLTextAreaElement).value)
    .toBe(new URL(initial.subscription_path, window.location.origin).toString());
});

it("keeps noVNC on the panel origin and rejects non-web URLs", () => {
  expect(browserURLForPanel("https://panel.example/browser/vnc.html?autoconnect=1", "https://panel.example:8443"))
    .toBe("https://panel.example:8443/browser/vnc.html?autoconnect=1");
  expect(browserURLForPanel("https://other.example/browser/", "https://panel.example:8443"))
    .toBe("https://other.example/browser/");
  expect(browserURLForPanel("javascript:alert(1)", "https://panel.example:8443")).toBeUndefined();
});
