import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { InstallApp } from "./InstallApp";
import { listenInstallEvents, resetGatewayRuntime } from "./pwa/runtime";

beforeEach(() => {
  resetGatewayRuntime();
  HTMLDialogElement.prototype.showModal = function () { this.setAttribute("open", ""); };
  HTMLDialogElement.prototype.close = function () { this.removeAttribute("open"); };
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Safari/537.36");
  listenInstallEvents();
});
afterEach(() => { cleanup(); resetGatewayRuntime(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("application install dialog", () => {
  it("offers the native prompt only when the browser supplied it", async () => {
    const prompt = vi.fn().mockResolvedValue(undefined);
    window.dispatchEvent(Object.assign(new Event("beforeinstallprompt", { cancelable: true }), {
      prompt, userChoice: Promise.resolve({ outcome: "dismissed" }),
    }));
    render(<InstallApp onClose={vi.fn()} />);
    expect(prompt).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Установить" }));
    expect(await screen.findByText("Установка отменена. Её можно повторить из меню браузера.")).toBeTruthy();
    expect(prompt).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Установить" })).toBeNull();
    expect(screen.queryByText(/Приложение установлено/)).toBeNull();
  });

  it("shows iPhone home-screen steps and keeps the longer explanation under help", () => {
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Version/17 Mobile Safari/604.1");
    render(<InstallApp onClose={vi.fn()} />);
    expect(screen.getByRole("list", { name: "Установка на iPhone и iPad" })).toBeTruthy();
    expect(screen.getByText(/Поделиться.*На экран/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Установить" })).toBeNull();
    expect(screen.queryByText(/Для VPN-подключения используйте клиент/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Об установленном приложении" }));
    expect(screen.getByText(/Для VPN-подключения используйте клиент/)).toBeTruthy();
  });

  it("shows the installed state and closes through the existing modal", () => {
    window.dispatchEvent(new Event("appinstalled"));
    const close = vi.fn();
    render(<InstallApp onClose={close} />);
    expect(screen.getByText(/Приложение установлено/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Готово" }));
    expect(close).toHaveBeenCalledOnce();
  });
});
