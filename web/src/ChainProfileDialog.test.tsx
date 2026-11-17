import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ChainProfileDialog } from "./ChainProfileDialog";

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); };
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it("updates the downloaded bundle's subscription after rotation", async () => {
  const fetchMock = vi.fn(async () => Response.json({ subscription_path: "/sub/entry/new", subscription_url: "https://panel.example:8443/sub/entry/new" }));
  vi.stubGlobal("fetch", fetchMock);
  render(<ChainProfileDialog token="session" device={{ id: "device", name: "Phone", node_id: "node", created_at: "", revoked_at: null }}
    bundle={{ version: 1, subscription_url: "https://panel.example:8443/sub/entry/old", entry_proxy: { password: "secret" }, profiles: [] }} onClose={() => {}} />);
  expect((screen.getByLabelText("Подписка с входным прокси") as HTMLTextAreaElement).value).toMatch(/\/old$/);
  fireEvent.click(screen.getByRole("button", { name: "Новая подписка цепочки" }));
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Создать новую подписку" }));
  await waitFor(() => expect((screen.getByLabelText("Подписка с входным прокси") as HTMLTextAreaElement).value).toMatch(/\/new$/));
  const bundle = JSON.parse((screen.getByLabelText("Профиль с входным прокси") as HTMLTextAreaElement).value);
  expect(bundle.subscription_url).toBe("https://panel.example:8443/sub/entry/new");
  expect(bundle.entry_proxy.password).toBe("secret");
});
