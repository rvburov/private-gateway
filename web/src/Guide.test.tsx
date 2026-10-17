import { describe, expect, it } from "vitest";
import { detectPlatform, platforms } from "./Guide";

describe("setup instructions", () => {
  it("opens the instructions for the system the panel is viewed on", () => {
    const cases: [string, number, string][] = [
      ["Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 Chrome/141.0 Mobile Safari/537.36", 5, "android"],
      ["Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 Version/19.0 Mobile/15E148 Safari/604.1", 5, "ios"],
      // iPadOS presents itself as a Mac, but with a touch screen.
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
      expect(platform.apps.length).toBeGreaterThan(0);
      const titles = platform.steps.map((step) => step.title).join(" | ");
      expect(titles).toContain("ссылку-подписку");
      expect(titles).toContain("Весь трафик через сервер");
      expect(titles).toContain("IPv6");
      expect(titles).toMatch(/[Kk]ill switch/);
      expect(platform.steps.at(-1)?.title).toBe("Проверьте подключение");
      for (const app of platform.apps) {
        if (app.link) expect(app.link).toMatch(/^https:\/\//);
      }
    }
    // Only what was tried on a real device is marked as checked.
    expect(platforms.filter((platform) => platform.checked).map((platform) => platform.id)).toEqual(["ios"]);
  });
});
