export type InstallKind = "ios" | "android" | "desktop";

export interface GatewayPlatform {
  ios: boolean;
  kind: InstallKind;
  browser: "safari" | "chrome" | "firefox" | "other";
}

// iPadOS может представляться как Mac; сенсорный экран отличает его для инструкции установки.
export function currentPlatform(): GatewayPlatform {
  const ua = navigator.userAgent;
  const ios = /iPhone|iPod|iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
  const kind = ios ? "ios" : /Android/.test(ua) ? "android" : "desktop";
  const browser = /Firefox\/|FxiOS\//.test(ua) ? "firefox"
    : /Chrome\/|CriOS\//.test(ua) && !/Edg|OPR|YaBrowser|SamsungBrowser/.test(ua) ? "chrome"
      : /Safari\//.test(ua) && !/Chrome|CriOS|Edg|OPR|YaBrowser|SamsungBrowser/.test(ua) ? "safari" : "other";
  return { ios, kind, browser };
}

export function isStandalone(): boolean {
  return (navigator as Navigator & { standalone?: boolean }).standalone === true ||
    window.matchMedia?.("(display-mode: standalone)").matches === true;
}
