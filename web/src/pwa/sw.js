/*
 * Worker хранит только общедоступную оболочку панели. Сборка подставляет хэш и список файлов;
 * ответы API, подписки, удалённый браузер и неизвестные адреса worker не перехватывает.
 */
/* global self, caches */
"use strict";

const BUILD = self.__GATEWAY_PWA__ || { version: "dev", shell: [] };
const PREFIX = "private-gateway-shell-";
const CACHE = PREFIX + BUILD.version;
const ICON = "/icons/icon-192.png";
const BADGE = "/icons/badge-96.png";

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(BUILD.shell)));
});

// Новая версия включается после закрытия старых вкладок; незавершённую форму не перезагружаем.
self.addEventListener("activate", (event) => {
  event.waitUntil(caches.keys().then((keys) => Promise.all(keys
    .filter((key) => key.startsWith(PREFIX) && key !== CACHE)
    .map((key) => caches.delete(key)))).then(() => self.clients.claim()));
});

function passThrough(request, url) {
  return request.method !== "GET" || url.origin !== self.location.origin || url.search !== "" ||
    url.pathname === "/api" || url.pathname.startsWith("/api/") ||
    url.pathname === "/sub" || url.pathname.startsWith("/sub/") ||
    url.pathname.startsWith("/browser") || url.pathname === "/readyz" || url.pathname === "/healthz" ||
    url.pathname === "/sw.js";
}

async function shellFile(request, url) {
  const cache = await caches.open(CACHE);
  if (url.pathname.startsWith("/assets/")) {
    const saved = await cache.match(request);
    if (saved) return saved;
  }
  try {
    const response = await fetch(request);
    if (response.ok && response.type !== "opaqueredirect") await cache.put(request, response.clone());
    return response;
  } catch (error) {
    const saved = await cache.match(request);
    if (saved) return saved;
    throw error;
  }
}

async function navigation(request) {
  const cache = await caches.open(CACHE);
  try {
    // Не смешиваем новый index.html со старыми хэшами JS в активном кэше после обновления.
    return await fetch(request);
  } catch (error) {
    const saved = await cache.match("/index.html") || await cache.match("/offline.html");
    if (saved) return saved;
    throw error;
  }
}

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (passThrough(request, url)) return;
  if (request.mode === "navigate") {
    // Переходы к профилям и неизвестным страницам не превращаем в закэшированную панель.
    if (url.pathname === "/" || url.pathname === "/index.html") event.respondWith(navigation(request));
    return;
  }
  if (BUILD.shell.includes(url.pathname)) event.respondWith(shellFile(request, url));
});

// Уведомление никогда не ведёт на другой сайт или на ссылку-подписку.
function targetUrl(raw) {
  try {
    const url = new URL(typeof raw === "string" ? raw : "/", self.location.origin);
    if (url.origin === self.location.origin && url.pathname === "/" &&
      (url.search === "" || url.search === "?page=notifications")) return url.origin + url.pathname + url.search;
  } catch { /* Повреждённый адрес заменяется главной страницей. */ }
  return self.location.origin + "/";
}

function text(value, fallback, max) {
  return typeof value === "string" && value.trim() ? value.slice(0, max) : fallback;
}

self.addEventListener("push", (event) => {
  let data = {};
  try {
    const parsed = event.data?.json();
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) data = parsed;
  } catch { /* Не выводим произвольные нерасшифрованные данные на экран блокировки. */ }
  const title = text(data.title, "Private Gateway", 80);
  const options = {
    body: text(data.body, "В панели есть новое уведомление.", 240),
    icon: ICON,
    badge: BADGE,
    lang: "ru",
    data: { url: targetUrl(data.url) },
    tag: text(data.tag, "private-gateway", 100),
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = targetUrl(event.notification.data?.url);
  event.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(async (windows) => {
    const app = windows.find((client) => {
      try { const opened = new URL(client.url); return opened.origin === self.location.origin && opened.pathname === "/"; }
      catch { return false; }
    });
    if (app) {
      // Открытая панель сама сменит раздел, не теряя действующий сеанс входа.
      try {
        if (!app.postMessage) throw new Error("Нет связи с открытой панелью.");
        app.postMessage({ type: "gateway-notification-open" });
        return await app.focus();
      } catch { return self.clients.openWindow(url); }
    }
    return self.clients.openWindow(url);
  }));
});
