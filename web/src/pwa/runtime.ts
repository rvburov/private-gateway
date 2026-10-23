import { useSyncExternalStore } from "react";
import { currentPlatform, isStandalone } from "./platform";

interface BeforeInstallPromptEvent extends Event {
  prompt(): Promise<void>;
  readonly userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

interface InstallSnapshot {
  canPrompt: boolean;
  installed: boolean;
}

let deferred: BeforeInstallPromptEvent | null = null;
let justInstalled = false;
let snapshot: InstallSnapshot = { canPrompt: false, installed: false };
const listeners = new Set<() => void>();
let stopListening: (() => void) | null = null;

function refreshInstall() {
  const next = { canPrompt: deferred !== null, installed: justInstalled || isStandalone() };
  if (snapshot.canPrompt === next.canPrompt && snapshot.installed === next.installed) return;
  snapshot = next;
  listeners.forEach((listener) => listener());
}

// Событие может прийти до входа в панель, поэтому слушатель ставится в main.tsx.
export function listenInstallEvents(): void {
  if (stopListening) return;
  const install = (event: Event) => {
    event.preventDefault();
    deferred = event as BeforeInstallPromptEvent;
    refreshInstall();
  };
  const installed = () => { deferred = null; justInstalled = true; refreshInstall(); };
  const media = window.matchMedia?.("(display-mode: standalone)");
  window.addEventListener("beforeinstallprompt", install);
  window.addEventListener("appinstalled", installed);
  media?.addEventListener?.("change", refreshInstall);
  stopListening = () => {
    window.removeEventListener("beforeinstallprompt", install);
    window.removeEventListener("appinstalled", installed);
    media?.removeEventListener?.("change", refreshInstall);
  };
  refreshInstall();
}

export function useInstall() {
  const value = useSyncExternalStore(
    (listener) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
    () => snapshot,
  );
  return { ...value, kind: currentPlatform().kind };
}

export async function promptInstall(): Promise<"accepted" | "dismissed" | "unavailable"> {
  const event = deferred;
  if (!event) return "unavailable";
  deferred = null;
  refreshInstall();
  await event.prompt();
  const { outcome } = await event.userChoice;
  // Только appinstalled или standalone подтверждает установку, а не согласие в диалоге.
  return outcome;
}

let registrationPromise: Promise<ServiceWorkerRegistration | null> | null = null;

export function registerGatewayWorker(): Promise<ServiceWorkerRegistration | null> {
  if (import.meta.env.DEV || !window.isSecureContext || !("serviceWorker" in navigator)) return Promise.resolve(null);
  if (!registrationPromise) {
    registrationPromise = navigator.serviceWorker.register("/sw.js", { scope: "/", updateViaCache: "none" })
      .catch(() => { registrationPromise = null; return null; });
  }
  return registrationPromise;
}

// Push включается отдельно по кнопке; ожидание активного worker ограничено по времени.
export async function getGatewayRegistration(): Promise<ServiceWorkerRegistration | null> {
  const registration = await registerGatewayWorker();
  if (!registration) return null;
  if (registration.active) return registration;
  return new Promise((resolve) => {
    const timeout = setTimeout(() => resolve(null), 10_000);
    void navigator.serviceWorker.ready.then((ready) => { clearTimeout(timeout); resolve(ready); }, () => { clearTimeout(timeout); resolve(null); });
  });
}

// Тесты изолируют события окна и не сохраняют имитацию установки между сценариями.
export function resetGatewayRuntime(): void {
  stopListening?.();
  stopListening = null;
  deferred = null;
  justInstalled = false;
  snapshot = { canPrompt: false, installed: false };
  registrationPromise = null;
}
