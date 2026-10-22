import { afterEach, describe, expect, it, vi } from "vitest";
import { collectDeviceDiagnostics } from "./deviceDiagnostics";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const values = () => Object.fromEntries(collectDeviceDiagnostics().facts.map((fact) => [fact.id, fact.value]));

describe("device diagnostics", () => {
  it("collects browser reports without permissions, network calls or storage contents", () => {
    const fetch = vi.fn();
    const queryPermission = vi.fn();
    const highEntropy = vi.fn();
    const storedContents = vi.spyOn(Storage.prototype, "getItem");
    const storageWrite = vi.spyOn(Storage.prototype, "setItem");
    vi.stubGlobal("fetch", fetch);
    vi.stubGlobal("navigator", {
      userAgent: "Example browser",
      userAgentData: { platform: "Windows", brands: [{ brand: "Example", version: "1" }], mobile: false, getHighEntropyValues: highEntropy },
      language: "ru",
      languages: ["ru", "en"],
      hardwareConcurrency: 4,
      deviceMemory: 8,
      maxTouchPoints: 0,
      cookieEnabled: true,
      permissions: { query: queryPermission },
      connection: { effectiveType: "4g", downlink: 10, rtt: 50, saveData: false },
    });
    const result = collectDeviceDiagnostics();
    const facts = Object.fromEntries(result.facts.map((fact) => [fact.id, fact.value]));
    expect(facts).toMatchObject({
      "user-agent": "Example browser",
      platform: "Windows",
      "browser-hints": "Example 1",
      "mobile-hint": "Нет",
      languages: "ru, en",
      cpu: "4",
      memory: "≈ 8 ГиБ",
      touch: "0",
      "network-estimate": "4g; ≈ 10 Мбит/с; RTT ≈ 50 мс; экономия трафика выключена",
    });
    expect(result.notes.join(" ")).toContain("4g не доказывает использование мобильной сети");
    expect(result.notes.join(" ")).toContain("не измеряет уникальность отпечатка");
    expect(fetch).not.toHaveBeenCalled();
    expect(queryPermission).not.toHaveBeenCalled();
    expect(highEntropy).not.toHaveBeenCalled();
    expect(storedContents).not.toHaveBeenCalled();
    expect(storageWrite).not.toHaveBeenCalled();
  });

  it("keeps missing or restricted browser APIs unavailable instead of inventing zeros", () => {
    vi.stubGlobal("navigator", {
      get userAgentData() { throw new Error("restricted"); },
      get hardwareConcurrency() { throw new Error("restricted"); },
      deviceMemory: Number.NaN,
      maxTouchPoints: -1,
      connection: {
        get effectiveType() { throw new Error("restricted"); },
        get rtt() { throw new Error("restricted"); },
        downlink: Infinity,
      },
    });
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => { throw new Error("restricted"); });
    vi.spyOn(window, "sessionStorage", "get").mockImplementation(() => { throw new Error("restricted"); });
    vi.stubGlobal("indexedDB", undefined);
    expect(values()).toMatchObject({
      "user-agent": "Недоступно",
      "browser-hints": "Недоступно",
      "mobile-hint": "Недоступно",
      cpu: "Недоступно",
      memory: "Недоступно",
      touch: "Недоступно",
      storage: "Недоступно",
      "network-estimate": "Недоступно",
    });
  });

  it("releases a temporary graphics context even if GPU details are blocked", () => {
    vi.stubGlobal("WebGLRenderingContext", function () {});
    const loseContext = vi.fn();
    const context = {
      VENDOR: 1,
      RENDERER: 2,
      getExtension: vi.fn((name: string) => name === "WEBGL_lose_context" ? { loseContext } : null),
      getParameter: vi.fn(() => { throw new Error("blocked"); }),
    };
    const getContext = vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(context as unknown as WebGLRenderingContext);
    expect(values().graphics).toBe("Недоступно");
    expect(getContext).toHaveBeenCalledTimes(1);
    expect(loseContext).toHaveBeenCalledOnce();
  });

  it("shows permitted GPU details and bounds untrusted browser strings", () => {
    vi.stubGlobal("WebGLRenderingContext", function () {});
    vi.stubGlobal("navigator", { userAgent: "A".repeat(3000) });
    const loseContext = vi.fn();
    const context = {
      VENDOR: 1,
      RENDERER: 2,
      getExtension: vi.fn((name: string) => name === "WEBGL_lose_context" ? { loseContext } : { UNMASKED_VENDOR_WEBGL: 3, UNMASKED_RENDERER_WEBGL: 4 }),
      getParameter: vi.fn((parameter: number) => parameter === 3 ? "GPU vendor" : "GPU renderer"),
    };
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(context as unknown as WebGLRenderingContext);
    const facts = values();
    expect(facts.graphics).toBe("GPU vendor; GPU renderer");
    expect(facts["user-agent"]).toHaveLength(512);
    expect(loseContext).toHaveBeenCalledOnce();
  });
});
