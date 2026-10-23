import { createHash, webcrypto } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanupBrowserPush, currentPushHashForLogout, disableDevicePush, enableDevicePush, inspectDevicePush, vapidKeyBytes } from "./push";
import { currentPlatform, isStandalone } from "./pwa/platform";
import { getGatewayRegistration } from "./pwa/runtime";
import type { GatewayPushSubscription } from "./notifications";

vi.mock("./pwa/platform", () => ({ currentPlatform: vi.fn(), isStandalone: vi.fn() }));
vi.mock("./pwa/runtime", () => ({ getGatewayRegistration: vi.fn() }));

const endpoint = "https://fcm.googleapis.com/fcm/send/browser-subscription";
const key = Uint8Array.from({ length: 65 }, (_, index) => index === 0 ? 4 : index);
const publicKey = Buffer.from(key).toString("base64url");
const own: GatewayPushSubscription = {
  id: "00000000-0000-4000-8000-000000000001", endpoint_hash: createHash("sha256").update(endpoint).digest("hex").slice(0, 16),
  device: "Windows · Chrome", created_at: "2026-10-23T08:00:00Z", last_sent_at: null,
};
const subscription = () => ({
  endpoint, options: { applicationServerKey: key.buffer },
  toJSON: () => ({ endpoint, keys: { p256dh: "browser-p256dh", auth: "browser-auth" } }),
  unsubscribe: vi.fn().mockResolvedValue(true),
});
let local: ReturnType<typeof subscription>;
let pushManager: { getSubscription: ReturnType<typeof vi.fn>; subscribe: ReturnType<typeof vi.fn> };
let registration: ServiceWorkerRegistration;
let notification: { permission: NotificationPermission; requestPermission: ReturnType<typeof vi.fn> };
let fetchMock: ReturnType<typeof vi.fn>;
let getRegistration: ReturnType<typeof vi.fn>;

beforeEach(() => {
  local = subscription();
  pushManager = { getSubscription: vi.fn().mockResolvedValue(null), subscribe: vi.fn().mockResolvedValue(local) };
  registration = { pushManager } as unknown as ServiceWorkerRegistration;
  getRegistration = vi.fn().mockResolvedValue(registration);
  notification = { permission: "default", requestPermission: vi.fn().mockResolvedValue("granted") };
  vi.mocked(currentPlatform).mockReturnValue({ ios: false, kind: "desktop", browser: "chrome" });
  vi.mocked(isStandalone).mockReturnValue(false);
  vi.mocked(getGatewayRegistration).mockResolvedValue(registration);
  vi.stubGlobal("navigator", { platform: "Win32", serviceWorker: { getRegistration } });
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("Notification", notification);
  vi.stubGlobal("PushManager", class {});
  fetchMock = vi.fn().mockImplementation(async (path: string) => Response.json(path.endsWith("/push-key") ? { public_key: publicKey } : own));
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("browser push setup", () => {
  it("recognizes this device from the server hash without permission prompts, subscriptions or API requests", async () => {
    notification.permission = "granted";
    pushManager.getSubscription.mockResolvedValue(local);
    const result = await inspectDevicePush([own]);
    expect(result).toMatchObject({ state: "on", hash: own.endpoint_hash, subscription: local });
    expect(notification.requestPermission).not.toHaveBeenCalled();
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(getGatewayRegistration).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("asks permission synchronously before network work and saves only the browser subscription to the gateway", async () => {
    let answer!: (value: NotificationPermission) => void;
    notification.requestPermission.mockReturnValue(new Promise<NotificationPermission>((resolve) => { answer = resolve; }));
    const storageWrite = vi.spyOn(Storage.prototype, "setItem");
    const enabling = enableDevicePush("administrator-session", []);
    expect(notification.requestPermission).toHaveBeenCalledOnce();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(getGatewayRegistration).not.toHaveBeenCalled();
    answer("granted");
    expect(await enabling).toEqual(own);
    expect(pushManager.subscribe).toHaveBeenCalledWith({ userVisibleOnly: true, applicationServerKey: key });
    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/notifications/push-key", "/api/v1/notifications/push-subscriptions"]);
    const options = fetchMock.mock.calls[1][1] as RequestInit;
    expect(new Headers(options.headers).get("Authorization")).toBe("Bearer administrator-session");
    expect(JSON.parse(String(options.body))).toEqual({ endpoint, keys: { p256dh: "browser-p256dh", auth: "browser-auth" }, device: "Windows · Chrome" });
    expect(storageWrite).not.toHaveBeenCalled();
  });

  it.each(["denied", "default"] as const)("does not contact the gateway when permission is %s", async (permission) => {
    notification.requestPermission.mockResolvedValue(permission);
    await expect(enableDevicePush("administrator-session", [])).rejects.toThrow(permission === "denied" ? "запрещены" : "не выдано");
    expect(fetchMock).not.toHaveBeenCalled();
    expect(getGatewayRegistration).not.toHaveBeenCalled();
    expect(pushManager.subscribe).not.toHaveBeenCalled();
  });

  it("requires installation on iPhone before asking permission or performing network work", async () => {
    vi.mocked(currentPlatform).mockReturnValue({ ios: true, kind: "ios", browser: "safari" });
    expect((await inspectDevicePush([])).state).toBe("ios");
    await expect(enableDevicePush("administrator-session", [])).rejects.toThrow("экран «Домой»");
    expect(notification.requestPermission).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("removes a newly created browser subscription when the gateway cannot save it", async () => {
    notification.permission = "granted";
    fetchMock.mockImplementation(async (path: string) => path.endsWith("/push-key")
      ? Response.json({ public_key: publicKey })
      : Response.json({ error: "Сохранение подписки недоступно." }, { status: 503 }));
    await expect(enableDevicePush("administrator-session", [])).rejects.toThrow("Сохранение подписки недоступно");
    expect(local.unsubscribe).toHaveBeenCalledOnce();
  });

  it("replaces a subscription with the old application key instead of reusing an unusable grant", async () => {
    notification.permission = "granted";
    const old = subscription();
    old.options.applicationServerKey = Uint8Array.from(key, (value, index) => index === 1 ? value + 1 : value).buffer;
    pushManager.getSubscription.mockResolvedValue(old);
    await enableDevicePush("administrator-session", [own]);
    expect(old.unsubscribe).toHaveBeenCalledOnce();
    expect(pushManager.subscribe).toHaveBeenCalledOnce();
    expect(local.unsubscribe).not.toHaveBeenCalled();
    expect(vapidKeyBytes(publicKey)).toEqual(key);
    expect(() => vapidKeyBytes("invalid-key")).toThrow("неверный ключ");
  });

  it("still unsubscribes the browser when server revocation fails and reports the incomplete result", async () => {
    fetchMock.mockResolvedValue(Response.json({ error: "Сервер недоступен." }, { status: 503 }));
    await expect(disableDevicePush("administrator-session", own, local as unknown as PushSubscription)).rejects.toThrow("сервер не подтвердил отзыв");
    expect(local.unsubscribe).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/notifications/push-subscriptions/${own.id}`, expect.objectContaining({ method: "DELETE" }));
    local.unsubscribe.mockRejectedValueOnce(new Error("Browser unavailable"));
    await expect(disableDevicePush("administrator-session", own, local as unknown as PushSubscription)).rejects.not.toThrow("Браузер отключён");
  });

  it("cleans up the local browser subscription after logout without credentials or API calls", async () => {
    pushManager.getSubscription.mockResolvedValue(local);
    await cleanupBrowserPush();
    expect(local.unsubscribe).toHaveBeenCalledOnce();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(notification.requestPermission).not.toHaveBeenCalled();
  });

  it("reads only the current endpoint hash for logout without registration, permission prompts or API requests", async () => {
    pushManager.getSubscription.mockResolvedValue(local);
    const storageRead = vi.spyOn(Storage.prototype, "getItem");
    expect(await currentPushHashForLogout()).toBe(own.endpoint_hash);
    expect(getRegistration).toHaveBeenCalledWith("/");
    expect(getGatewayRegistration).not.toHaveBeenCalled();
    expect(notification.requestPermission).not.toHaveBeenCalled();
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(local.unsubscribe).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(storageRead).not.toHaveBeenCalled();
  });

  it("does not delay logout indefinitely when the browser never returns a registration", async () => {
    vi.useFakeTimers();
    getRegistration.mockReturnValue(new Promise(() => {}));
    const settled = vi.fn();
    const reading = currentPushHashForLogout().then((value) => { settled(value); return value; });
    await vi.advanceTimersByTimeAsync(1_499);
    expect(settled).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(await reading).toBeNull();
    expect(notification.requestPermission).not.toHaveBeenCalled();
    expect(getGatewayRegistration).not.toHaveBeenCalled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("lets logout continue when service workers are unsupported or the browser rejects inspection", async () => {
    vi.stubGlobal("navigator", { platform: "Win32" });
    expect(await currentPushHashForLogout()).toBeNull();
    vi.stubGlobal("navigator", { platform: "Win32", serviceWorker: { getRegistration } });
    getRegistration.mockRejectedValue(new Error("Browser unavailable"));
    expect(await currentPushHashForLogout()).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(notification.requestPermission).not.toHaveBeenCalled();
    expect(getGatewayRegistration).not.toHaveBeenCalled();
  });

  it("bounds a browser that never answers the subscription inspection", async () => {
    vi.useFakeTimers();
    getRegistration.mockReturnValue(new Promise(() => {}));
    const inspection = inspectDevicePush([]);
    const failure = expect(inspection).rejects.toThrow("Браузер не ответил вовремя");
    await vi.advanceTimersByTimeAsync(10_000);
    await failure;
    expect(notification.requestPermission).not.toHaveBeenCalled();
  });
});
