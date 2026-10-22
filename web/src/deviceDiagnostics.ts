export interface DeviceFact {
  id: string;
  label: string;
  value: string;
}

export interface DeviceDiagnostics {
  collectedAt: number;
  facts: DeviceFact[];
  notes: string[];
}

interface BrowserHints {
  brands?: Array<{ brand?: string; version?: string }>;
  mobile?: boolean;
  platform?: string;
}

interface ConnectionEstimate {
  effectiveType?: string;
  downlink?: number;
  rtt?: number;
  saveData?: boolean;
}

type DiagnosticNavigator = Navigator & {
  userAgentData?: BrowserHints;
  deviceMemory?: number;
  connection?: ConnectionEstimate;
};

const unavailable = "Недоступно";

function read<T>(get: () => T): T | undefined {
  try {
    return get();
  } catch {
    return undefined;
  }
}

function text(value: unknown, limit = 512): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim().slice(0, limit) : undefined;
}

function positive(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}

function nonnegative(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function dimensions(width: unknown, height: unknown): string | undefined {
  return positive(width) && positive(height) ? `${width} × ${height}` : undefined;
}

function webGL(): string | undefined {
  // Не создаём контекст там, где браузер вовсе не предоставляет WebGL.
  if (typeof window === "undefined" || typeof document === "undefined" ||
      !("WebGLRenderingContext" in window || "WebGL2RenderingContext" in window)) return undefined;
  let context: WebGLRenderingContext | WebGL2RenderingContext | null = null;
  try {
    const canvas = document.createElement("canvas");
    context = canvas.getContext("webgl2") ?? canvas.getContext("webgl");
    if (!context) return undefined;
    const debug = context.getExtension("WEBGL_debug_renderer_info");
    const vendor = text(context.getParameter(debug?.UNMASKED_VENDOR_WEBGL ?? context.VENDOR), 180);
    const renderer = text(context.getParameter(debug?.UNMASKED_RENDERER_WEBGL ?? context.RENDERER), 300);
    return [vendor, renderer].filter(Boolean).join("; ") || undefined;
  } catch {
    return undefined;
  } finally {
    // Контекст служит только этому замеру и не должен занимать ресурсы вкладки.
    read(() => context?.getExtension("WEBGL_lose_context")?.loseContext());
  }
}

/** Сведения, доступные странице без разрешений. Вызывать только при запуске проверки. */
export function collectDeviceDiagnostics(): DeviceDiagnostics {
  const nav = typeof navigator === "undefined" ? undefined : navigator as DiagnosticNavigator;
  const win = typeof window === "undefined" ? undefined : window;
  const display = read(() => win?.screen);
  const facts: DeviceFact[] = [];
  const add = (id: string, label: string, value: unknown) => {
    facts.push({ id, label, value: text(value) ?? unavailable });
  };
  add("user-agent", "User-Agent", read(() => nav?.userAgent));
  add("platform", "Платформа, сообщённая браузером", read(() => nav?.userAgentData?.platform ?? nav?.platform));
  const hints = read(() => nav?.userAgentData);
  const brands = read(() => hints?.brands?.slice(0, 8).map((entry) =>
    [text(entry.brand, 64), text(entry.version, 32)].filter(Boolean).join(" ")).filter(Boolean).join(", "));
  add("browser-hints", "Данные браузера (Client Hints)", brands);
  const mobile = read(() => hints?.mobile);
  add("mobile-hint", "Признак мобильного браузера", typeof mobile === "boolean" ? mobile ? "Да" : "Нет" : undefined);
  add("languages", "Языки", read(() => nav?.languages?.slice(0, 16).join(", ") || nav?.language));
  add("time-zone", "Часовой пояс", read(() => Intl.DateTimeFormat().resolvedOptions().timeZone));
  const utcOffset = new Date().getTimezoneOffset();
  add("utc-offset", "Смещение часов относительно UTC", Number.isFinite(utcOffset) ?
    `UTC${utcOffset <= 0 ? "+" : "−"}${Math.floor(Math.abs(utcOffset) / 60).toString().padStart(2, "0")}:${(Math.abs(utcOffset) % 60).toString().padStart(2, "0")}` : undefined);
  add("screen", "Экран, CSS-пиксели", read(() => dimensions(display?.width, display?.height)));
  add("viewport", "Область страницы, CSS-пиксели", read(() => dimensions(win?.innerWidth, win?.innerHeight)));
  const density = read(() => win?.devicePixelRatio);
  add("pixel-ratio", "Плотность пикселей", positive(density) ? String(density) : undefined);
  const colorDepth = read(() => display?.colorDepth);
  add("color-depth", "Глубина цвета", positive(colorDepth) ? `${colorDepth} бит` : undefined);
  const touchPoints = read(() => nav?.maxTouchPoints);
  add("touch", "Точки одновременного касания", nonnegative(touchPoints) ? String(touchPoints) : undefined);
  const cores = read(() => nav?.hardwareConcurrency);
  add("cpu", "Логические ядра, доступные браузеру", positive(cores) ? String(cores) : undefined);
  const memory = read(() => nav?.deviceMemory);
  add("memory", "Оценка памяти устройства браузером", positive(memory) ? `≈ ${memory} ГиБ` : undefined);
  const cookies = read(() => nav?.cookieEnabled);
  add("cookies", "Поддержка cookie", typeof cookies === "boolean" ? cookies ? "Включена" : "Выключена" : undefined);
  const storage = [
    read(() => win?.localStorage) ? "localStorage" : null,
    read(() => win?.sessionStorage) ? "sessionStorage" : null,
    read(() => win?.indexedDB) ? "IndexedDB" : null,
  ].filter(Boolean);
  add("storage", "Доступные API хранилищ", storage.length ? `${storage.join(", ")}; запись не проверялась` : undefined);
  const connection = read(() => nav?.connection);
  const downlink = read(() => connection?.downlink);
  const rtt = read(() => connection?.rtt);
  const saveData = read(() => connection?.saveData);
  const estimates = [
    text(read(() => connection?.effectiveType), 32),
    positive(downlink) ? `≈ ${downlink} Мбит/с` : null,
    nonnegative(rtt) ? `RTT ≈ ${rtt} мс` : null,
    typeof saveData === "boolean" ? saveData ? "экономия трафика включена" : "экономия трафика выключена" : null,
  ].filter(Boolean);
  add("network-estimate", "Оценка сети браузером", estimates.length ? estimates.join("; ") : undefined);
  const doNotTrack = read(() => nav?.doNotTrack);
  add("do-not-track", "Do Not Track", doNotTrack === "1" ? "Включён" : doNotTrack === "0" ? "Выключен" : "Не указан");
  add("graphics", "Графика WebGL", webGL());
  return {
    collectedAt: Date.now(),
    facts,
    notes: [
      "Это сведения, которые браузер сообщает странице. Часть значений он округляет, скрывает или подменяет; точную модель устройства по ним определить нельзя.",
      "Язык, часовой пояс, экран, User-Agent и графика входят в отпечаток браузера. VPN не скрывает их. Эта проверка не измеряет уникальность отпечатка и не оценивает анонимность.",
      "Оценка сети браузером не заменяет замер скорости. Значение вроде 4g не доказывает использование мобильной сети и не определяет оператора или Wi-Fi.",
      "Страница не получает MAC-адрес, IMEI, серийный номер устройства, список установленных программ или содержимое других сайтов. В этой проверке не запрашиваются дополнительные разрешения и не читается содержимое хранилищ.",
    ],
  };
}
