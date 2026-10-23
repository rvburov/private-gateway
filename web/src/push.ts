import { deletePushSubscription, notificationRequest as request, type GatewayPushSubscription } from "./notifications";
import { currentPlatform, isStandalone } from "./pwa/platform";
import { getGatewayRegistration } from "./pwa/runtime";

export type PushState = "off" | "on" | "denied" | "ios" | "unsupported";
export interface DevicePush {
  state: PushState;
  subscription: PushSubscription | null;
  hash: string | null;
}

export function browserPushState(): Exclude<PushState, "on"> {
  if (currentPlatform().ios && !isStandalone()) return "ios";
  if (window.isSecureContext === false || !("serviceWorker" in navigator) ||
      !("PushManager" in window) || !("Notification" in window) || !crypto.subtle) return "unsupported";
  return Notification.permission === "denied" ? "denied" : "off";
}

// Сервер возвращает только короткий хеш адреса push-службы, а не сам адрес.
export async function endpointHash(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
  return [...new Uint8Array(digest)].map((byte) => byte.toString(16).padStart(2, "0")).join("").slice(0, 16);
}

export function vapidKeyBytes(value: string): Uint8Array<ArrayBuffer> {
  if (!/^[\w-]+$/.test(value)) throw new Error("Сервер вернул неверный ключ уведомлений.");
  let raw: string;
  try { raw = atob(value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4)); }
  catch { throw new Error("Сервер вернул неверный ключ уведомлений."); }
  const bytes = Uint8Array.from(raw, (char) => char.charCodeAt(0));
  if (bytes.length !== 65 || bytes[0] !== 4) throw new Error("Сервер вернул неверный ключ уведомлений.");
  return bytes;
}

function matchesKey(subscription: PushSubscription, key: Uint8Array): boolean {
  const current = subscription.options?.applicationServerKey;
  if (!current) return true;
  const bytes = new Uint8Array(current);
  return bytes.length === key.length && bytes.every((byte, index) => byte === key[index]);
}

async function bounded<T>(work: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([work, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("Браузер не ответил вовремя. Обновите страницу и повторите.")), 10_000);
    })]);
  } finally { clearTimeout(timer); }
}

export async function inspectDevicePush(known: readonly GatewayPushSubscription[]): Promise<DevicePush> {
  const state = browserPushState();
  if (state !== "off") return { state, subscription: null, hash: null };
  // Проверка состояния ничего не подписывает и не вызывает окно разрешения.
  const subscription = await bounded((async () => {
    const registration = await navigator.serviceWorker.getRegistration("/");
    return await registration?.pushManager.getSubscription() ?? null;
  })());
  const hash = subscription ? await endpointHash(subscription.endpoint) : null;
  return {
    state: subscription && Notification.permission === "granted" && known.some((item) => item.endpoint_hash === hash) ? "on" : "off",
    subscription, hash,
  };
}

function permissionFromClick(): Promise<NotificationPermission> {
  if (Notification.permission !== "default") return Promise.resolve(Notification.permission);
  // Вызов расположен до первого await: iOS и другие браузеры требуют жест пользователя.
  return Notification.requestPermission();
}

async function activeRegistration(): Promise<ServiceWorkerRegistration> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const registration = await Promise.race([
      getGatewayRegistration(),
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("Не удалось подготовить приложение. Обновите страницу и повторите.")), 10_000); }),
    ]);
    if (!registration) throw new Error("Уведомления недоступны. Обновите приложение и повторите.");
    return registration;
  } finally { clearTimeout(timer); }
}

function deviceName(): string {
  const platform = currentPlatform();
  const os = platform.ios ? "iPhone / iPad" : platform.kind === "android" ? "Android" : /Mac/.test(navigator.platform) ? "Mac" : /Win/.test(navigator.platform) ? "Windows" : "Компьютер";
  const browser = platform.browser === "safari" ? "Safari" : platform.browser === "chrome" ? "Chrome" : platform.browser === "firefox" ? "Firefox" : "браузер";
  return `${os} · ${browser}`;
}

export async function enableDevicePush(token: string, known: readonly GatewayPushSubscription[]): Promise<GatewayPushSubscription> {
  const capability = browserPushState();
  if (capability === "ios") throw new Error("Сначала установите приложение на экран «Домой» и откройте его оттуда.");
  if (capability === "unsupported") throw new Error("Этот браузер не поддерживает push-уведомления.");
  const permission = await permissionFromClick();
  if (permission === "denied") throw new Error("Уведомления запрещены в настройках браузера.");
  if (permission !== "granted") throw new Error("Разрешение не выдано. Вы сможете включить уведомления позже.");
  const { public_key } = await request<{ public_key: string }>(token, "/notifications/push-key");
  const key = vapidKeyBytes(public_key);
  const registration = await activeRegistration();
  let subscription = await registration.pushManager.getSubscription();
  const subscriptionHash = subscription ? await endpointHash(subscription.endpoint) : null;
  if (subscription && (!matchesKey(subscription, key) || !known.some((item) => item.endpoint_hash === subscriptionHash))) {
    if (!await subscription.unsubscribe()) throw new Error("Не удалось обновить подписку браузера. Повторите позже.");
    subscription = null;
  }
  let created = false;
  if (!subscription) {
    try { subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }); }
    catch { throw new Error("Браузер не смог включить уведомления. Проверьте разрешение и подключение к интернету."); }
    created = true;
  }
  try {
    const json = subscription.toJSON();
    return await request<GatewayPushSubscription>(token, "/notifications/push-subscriptions", {
      method: "POST", body: JSON.stringify({ endpoint: subscription.endpoint, keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" }, device: deviceName() }),
    });
  } catch (error) {
    if (created) await subscription.unsubscribe().catch(() => false);
    throw error;
  }
}

export async function disableDevicePush(token: string, current: GatewayPushSubscription | undefined, subscription: PushSubscription | null): Promise<void> {
  const [server, browser] = await Promise.allSettled([
    current ? deletePushSubscription(token, current.id) : Promise.resolve(),
    subscription ? subscription.unsubscribe() : Promise.resolve(true),
  ]);
  const browserFailed = browser.status === "rejected" || !browser.value;
  if (server.status === "rejected" && browserFailed) throw new Error("Не удалось подтвердить отключение в браузере и на сервере. Повторите позже.");
  if (server.status === "rejected") throw new Error("Браузер отключён, но сервер не подтвердил отзыв. Повторите отключение в списке устройств.");
  if (browserFailed) throw new Error("Сервер отключил уведомления. Браузер не подтвердил отписку; повторите позже.");
}

// Сервер уже отозвал подписку при выходе; браузерную копию убираем без токена и запросов API.
export async function cleanupBrowserPush(): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  const registration = await navigator.serviceWorker.getRegistration("/");
  const subscription = await registration?.pushManager.getSubscription();
  if (subscription) await subscription.unsubscribe();
}

// Короткий хеш позволяет серверу отозвать именно это устройство и после восстановления входа.
export async function currentPushHashForLogout(): Promise<string | null> {
  if (!("serviceWorker" in navigator) || !crypto.subtle) return null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      (async () => {
        const registration = await navigator.serviceWorker.getRegistration("/");
        const subscription = await registration?.pushManager.getSubscription();
        return subscription ? await endpointHash(subscription.endpoint) : null;
      })().catch(() => null),
      new Promise<null>((resolve) => { timer = setTimeout(() => resolve(null), 1_500); }),
    ]);
  } finally { clearTimeout(timer); }
}
