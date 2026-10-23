import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { getGatewayRegistration, listenInstallEvents, promptInstall, registerGatewayWorker, resetGatewayRuntime, useInstall } from "./runtime";
import { currentPlatform, isStandalone } from "./platform";

function State() {
  const state = useInstall();
  return <span>{JSON.stringify(state)}</span>;
}

beforeEach(() => {
  resetGatewayRuntime();
  vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  vi.stubEnv("DEV", false);
  vi.stubGlobal("isSecureContext", true);
  Object.defineProperty(navigator, "maxTouchPoints", { configurable: true, get: () => 0 });
});

afterEach(() => {
  cleanup(); resetGatewayRuntime(); vi.unstubAllGlobals(); vi.unstubAllEnvs(); vi.restoreAllMocks();
  delete (navigator as unknown as Record<string, unknown>).maxTouchPoints;
  delete (navigator as unknown as Record<string, unknown>).serviceWorker;
});

describe("PWA installation and registration", () => {
  it("captures an early install event, consumes it once and confirms installation only after appinstalled", async () => {
    listenInstallEvents();
    listenInstallEvents();
    const prompt = vi.fn().mockResolvedValue(undefined);
    const event = Object.assign(new Event("beforeinstallprompt", { cancelable: true }), {
      prompt, userChoice: Promise.resolve({ outcome: "accepted" }),
    });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
    render(<State />);
    expect(screen.getByText(/"canPrompt":true/)).toBeTruthy();
    await act(async () => expect(await promptInstall()).toBe("accepted"));
    expect(prompt).toHaveBeenCalledOnce();
    expect(screen.getByText(/"installed":false/)).toBeTruthy();
    expect(await promptInstall()).toBe("unavailable");
    act(() => { window.dispatchEvent(new Event("appinstalled")); });
    expect(screen.getByText(/"installed":true/)).toBeTruthy();
  });

  it("keeps a dismissed prompt uninstalled and detects iPadOS with desktop user agent", async () => {
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 (Macintosh; Intel Mac OS X) AppleWebKit/605.1.15 Version/17 Safari/605.1.15");
    vi.spyOn(navigator, "maxTouchPoints", "get").mockReturnValue(5);
    expect(currentPlatform()).toMatchObject({ ios: true, kind: "ios", browser: "safari" });
    listenInstallEvents();
    window.dispatchEvent(Object.assign(new Event("beforeinstallprompt", { cancelable: true }), {
      prompt: vi.fn().mockResolvedValue(undefined), userChoice: Promise.resolve({ outcome: "dismissed" }),
    }));
    expect(await promptInstall()).toBe("dismissed");
    render(<State />);
    expect(screen.getByText(/"installed":false/)).toBeTruthy();
    expect(isStandalone()).toBe(false);
  });

  it("registers a single same-origin worker and waits for it to become active", async () => {
    const active = { active: {} } as ServiceWorkerRegistration;
    const installing = { active: null } as ServiceWorkerRegistration;
    let ready!: (value: ServiceWorkerRegistration) => void;
    const register = vi.fn().mockResolvedValue(installing);
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { register, ready: new Promise<ServiceWorkerRegistration>((resolve) => { ready = resolve; }) } });
    await registerGatewayWorker();
    let finished = false;
    const result = getGatewayRegistration().then((value) => { finished = true; return value; });
    await Promise.resolve();
    expect(finished).toBe(false);
    ready(active);
    expect(await result).toBe(active);
    expect(register).toHaveBeenCalledOnce();
    expect(register).toHaveBeenCalledWith("/sw.js", { scope: "/", updateViaCache: "none" });
    delete (navigator as unknown as Record<string, unknown>).serviceWorker;
  });

  it("does not register on insecure or development pages and returns null on failure", async () => {
    const register = vi.fn().mockRejectedValue(new Error("offline"));
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: { register } });
    vi.stubGlobal("isSecureContext", false);
    expect(await registerGatewayWorker()).toBeNull();
    expect(register).not.toHaveBeenCalled();
    vi.stubGlobal("isSecureContext", true);
    vi.stubEnv("DEV", true);
    expect(await registerGatewayWorker()).toBeNull();
    expect(register).not.toHaveBeenCalled();
    vi.stubEnv("DEV", false);
    expect(await getGatewayRegistration()).toBeNull();
    expect(register).toHaveBeenCalledOnce();
    delete (navigator as unknown as Record<string, unknown>).serviceWorker;
  });
});
