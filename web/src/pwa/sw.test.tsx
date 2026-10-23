import { runInNewContext } from "node:vm";
import { describe, expect, it, vi } from "vitest";
import source from "./sw.js?raw";

const origin = "https://gateway.example:8443";
type WorkerEvent = { request?: Request; notification?: unknown; data?: unknown; waitUntil?: (value: Promise<unknown>) => void; respondWith?: (value: Promise<unknown>) => void };

function worker() {
  const handlers = new Map<string, (event: WorkerEvent) => void>();
  const cache = { addAll: vi.fn().mockResolvedValue(undefined), match: vi.fn(), put: vi.fn().mockResolvedValue(undefined) };
  const storage = { open: vi.fn().mockResolvedValue(cache), keys: vi.fn().mockResolvedValue([]), delete: vi.fn().mockResolvedValue(true) };
  const showNotification = vi.fn().mockResolvedValue(undefined);
  const clients = { claim: vi.fn().mockResolvedValue(undefined), matchAll: vi.fn().mockResolvedValue([]), openWindow: vi.fn().mockResolvedValue(undefined) };
  const fetch = vi.fn().mockResolvedValue(new Response("public shell", { headers: { "Content-Type": "text/html" } }));
  runInNewContext(source, {
    self: { __GATEWAY_PWA__: { version: "test", shell: ["/index.html", "/offline.html", "/assets/main-safe.js"] }, location: { origin },
      addEventListener: (name: string, handler: (event: WorkerEvent) => void) => handlers.set(name, handler), registration: { showNotification }, clients },
    caches: storage, fetch, URL, Promise,
  });
  return { handlers, cache, storage, fetch, showNotification, clients };
}

describe("private gateway worker", () => {
  it("never intercepts API, credentials, browser, unknown paths or query strings", () => {
    const { handlers, storage, fetch } = worker();
    for (const path of ["/api/v1/devices/device/profile", "/sub/private-secret", "/browser/", "/browser-authorization", "/readyz", "/healthz", "/unknown", "/?token=secret", "/?page=notifications"]) {
      const request = new Request(origin + path);
      Object.defineProperty(request, "mode", { value: "navigate" });
      const respondWith = vi.fn();
      handlers.get("fetch")!({ request, respondWith });
      expect(respondWith, path).not.toHaveBeenCalled();
    }
    expect(storage.open).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("caches only the explicit public shell and serves it when the root is offline", async () => {
    const { handlers, cache, storage, fetch } = worker();
    let task!: Promise<unknown>;
    handlers.get("install")!({ waitUntil: (value) => { task = value; } });
    await task;
    expect(cache.addAll).toHaveBeenCalledWith(["/index.html", "/offline.html", "/assets/main-safe.js"]);
    fetch.mockRejectedValue(new Error("offline"));
    cache.match.mockResolvedValue(new Response("public shell"));
    const request = new Request(origin + "/");
    Object.defineProperty(request, "mode", { value: "navigate" });
    handlers.get("fetch")!({ request, respondWith: (value) => { task = value; } });
    const response = await task as Response;
    expect(await response.text()).toBe("public shell");
    expect(storage.open).toHaveBeenCalledWith("private-gateway-shell-test");
    expect(cache.put).not.toHaveBeenCalled();
  });

  it("reuses only current build assets and leaves unrelated caches alone", async () => {
    const { handlers, cache, storage, fetch, clients } = worker();
    storage.keys.mockResolvedValue(["private-gateway-shell-old", "private-gateway-shell-test", "another-app"]);
    let task!: Promise<unknown>;
    handlers.get("activate")!({ waitUntil: (value) => { task = value; } });
    await task;
    expect(storage.delete).toHaveBeenCalledExactlyOnceWith("private-gateway-shell-old");
    expect(clients.claim).toHaveBeenCalledOnce();
    cache.match.mockResolvedValue(new Response("cached asset"));
    handlers.get("fetch")!({ request: new Request(origin + "/assets/main-safe.js"), respondWith: (value) => { task = value; } });
    expect(await (await task as Response).text()).toBe("cached asset");
    expect(fetch).not.toHaveBeenCalled();
    const respondWith = vi.fn();
    handlers.get("fetch")!({ request: new Request(origin + "/assets/not-in-build.js"), respondWith });
    expect(respondWith).not.toHaveBeenCalled();
  });

  it("does not replace the precached HTML with a later release before its worker activates", async () => {
    const { handlers, cache, fetch } = worker();
    fetch.mockResolvedValue(new Response("new release with new asset hashes", { headers: { "Content-Type": "text/html" } }));
    const request = new Request(origin + "/");
    Object.defineProperty(request, "mode", { value: "navigate" });
    let task!: Promise<unknown>;
    handlers.get("fetch")!({ request, respondWith: (value) => { task = value; } });
    expect(await (await task as Response).text()).toContain("new release");
    expect(cache.put).not.toHaveBeenCalled();
  });

  it("sanitizes push URLs and malformed payloads while showing a bounded generic fallback", async () => {
    const { handlers, showNotification } = worker();
    let task!: Promise<unknown>;
    handlers.get("push")!({ data: { json: () => ({ title: "A".repeat(100), body: "B".repeat(500), url: origin + "/sub/private-key", tag: "t" }) }, waitUntil: (value) => { task = value; } });
    await task;
    expect(showNotification.mock.calls[0][0]).toHaveLength(80);
    expect(showNotification.mock.calls[0][1].body).toHaveLength(240);
    expect(showNotification.mock.calls[0][1].data.url).toBe(origin + "/");
    handlers.get("push")!({ data: { json: () => { throw new Error("bad JSON"); } }, waitUntil: (value) => { task = value; } });
    await task;
    expect(showNotification.mock.calls[1][0]).toBe("Private Gateway");
    expect(showNotification.mock.calls[1][1].body).toBe("В панели есть новое уведомление.");
  });

  it("focuses an open app without navigation or opens only a safe new app URL", async () => {
    const { handlers, clients } = worker();
    const app = { url: origin + "/", focus: vi.fn().mockResolvedValue(undefined), postMessage: vi.fn(), navigate: vi.fn() };
    clients.matchAll.mockResolvedValue([app]);
    const close = vi.fn();
    let task!: Promise<unknown>;
    handlers.get("notificationclick")!({ notification: { close, data: { url: "/?page=notifications" } }, waitUntil: (value) => { task = value; } });
    await task;
    expect(app.postMessage).toHaveBeenCalledWith({ type: "gateway-notification-open" });
    expect(app.focus).toHaveBeenCalledOnce();
    expect(app.navigate).not.toHaveBeenCalled();
    clients.matchAll.mockResolvedValue([]);
    handlers.get("notificationclick")!({ notification: { close, data: { url: "https://foreign.example/steal" } }, waitUntil: (value) => { task = value; } });
    await task;
    expect(clients.openWindow).toHaveBeenCalledWith(origin + "/");
    expect(close).toHaveBeenCalledTimes(2);
  });
});
