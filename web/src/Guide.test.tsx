import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { clientSteps } from "./GuideDetails";
import { Guide, detectPlatform, platforms } from "./Guide";

describe("setup instructions", () => {
  it("opens the instructions for the system the panel is viewed on", () => {
    const cases: [string, number, string][] = [
      ["Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/141.0 Mobile Safari/537.36", 5, "android"],
      ["Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 Version/19.0 Mobile/15E148 Safari/604.1", 5, "ios"],
      // iPadOS представляется как Mac, но с сенсорным экраном.
      ["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/19.0 Safari/605.1.15", 5, "ios"],
      ["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/19.0 Safari/605.1.15", 0, "macos"],
      ["Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/141.0 Safari/537.36", 0, "windows"],
      ["Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0", 0, "linux"],
    ];
    for (const [agent, touchPoints, platform] of cases) {
      expect(detectPlatform(agent, touchPoints)).toBe(platform);
    }
  });

  it("gives every system the same path: link, app, VPN mode, full tunnel, IPv6, kill switch, check", () => {
    for (const platform of platforms) {
      const titles = platform.steps.map((step) => step.title).join(" | ");
      expect(titles).toContain("ссылку-подписку");
      expect(titles).toContain("Весь трафик через сервер");
      expect(titles).toContain("IPv6");
      expect(titles).toMatch(/[Kk]ill switch/);
      expect(platform.steps.at(-1)?.title).toBe("Проверьте подключение");
    }
    // Проверенным помечено только то, что пробовали на настоящем устройстве.
    expect(platforms.filter((platform) => platform.checked).map((platform) => platform.id)).toEqual(["ios", "windows"]);
  });

  it("ranks three clients for every system and keeps the steps general", () => {
    for (const platform of platforms) {
      expect(platform.top).toHaveLength(3);
      expect(platform.others.length).toBeGreaterThan(0);
      const names = [...platform.top, ...platform.others].map((app) => app.name);
      expect(new Set(names).size).toBe(names.length);
      for (const app of [...platform.top, ...platform.others]) {
        if (app.link) expect(app.link).toMatch(/^https:\/\//);
      }
      // Подробные шаги зависят от выбранного клиента.
      expect(clientSteps(platform.id, platform.top[0].name, platform.top[0].source)).not.toHaveLength(0);
    }
  });

  it("gives every listed client installation, import, connection and verification steps", () => {
    for (const platform of platforms) {
      for (const app of [...platform.top, ...platform.others]) {
        const titles = clientSteps(platform.id, app.name, app.source).map((step) => step.title).join(" | ");
        expect(titles).toContain("Установите");
        expect(titles).toMatch(/профиль|подписку/);
        expect(titles).toMatch(/Подключитесь|Подключение/);
        expect(titles).toContain("Проверьте");
      }
    }
  });

  it("opens Windows Hiddify details and offers both BAT files", () => {
    render(<Guide />);
    fireEvent.click(screen.getByRole("button", { name: "Windows" }));
    fireEvent.click(screen.getByRole("button", { name: "Hiddify → Инструкция" }));
    expect(screen.getByText(/Windows → Hiddify/)).toBeTruthy();
    expect(screen.getByText(/На проверенном ПК именно этот режим/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Скачать включение прокси" }).getAttribute("href")).toBe("/downloads/hiddify-proxy-on.bat");
    expect(screen.getByRole("link", { name: "Скачать выключение прокси" }).getAttribute("href")).toBe("/downloads/hiddify-proxy-off.bat");
    fireEvent.click(screen.getByRole("button", { name: /Все приложения для Windows/ }));
    expect(screen.getByRole("button", { name: "Clash Verge Rev" })).toBeTruthy();
  });

  it("explains the Chrome incognito proxy and WebRTC checks without listing unwanted browsers", () => {
    const { container } = render(<Guide />);
    const chrome = within(container).getByText("Chrome").closest("details");
    expect(chrome).not.toBeNull();
    fireEvent.click(within(container).getByText("Chrome"));
    expect(within(container).getByText(/SOCKS5 → 127\.0\.0\.1 → 2334/)).toBeTruthy();
    expect(within(container).getByText(/WebRTC Control → «Сведения»/)).toBeTruthy();
    expect(within(container).getByText(/откройте новое и повторите проверку IP, DNS и WebRTC/)).toBeTruthy();
    expect(within(container).queryByText(/Chrome, Edge, Яндекс Браузер/)).toBeNull();
  });
});
