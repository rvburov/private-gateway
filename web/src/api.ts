export interface Node {
  id: string;
  name: string;
  host: string;
  port: number;
  server_name: string;
  public_key: string;
  short_id: string;
}

export interface Device {
  id: string;
  node_id: string;
  name: string;
  created_at: string;
  revoked_at: string | null;
}

export interface TrafficUsage {
  devices: Record<string, { uplink_bytes: number; downlink_bytes: number }>;
  period: "since_xray_restart";
}

export interface ProfileExport {
  port?: number;
  uri: string;
  sing_box_json: string;
  mihomo_yaml: string;
  // /sub/ и секретный токен ссылки-подписки устройства на домене самой панели.
  subscription_path: string;
}

export interface AuditEvent {
  id: number;
  action: string;
  device_id: string | null;
  created_at: string;
}

export interface Status {
  database: string;
  node_id: string;
  devices: number;
  active_devices: number;
  desired_revision: number;
  applied_revision: number;
  xray_configured: boolean;
}

export interface BrowserStatus {
  running: boolean;
  url: string;
}

// Код ответа нужен интерфейсу, чтобы не путать отказ входа с потерей связи.
export class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "APIError";
  }
}

export class APIResponseError extends Error {
  constructor() {
    super("Сервер ответил, но формат ответа не распознан. Обновите страницу и проверьте связь снова.");
    this.name = "APIResponseError";
  }
}

// noVNC остаётся на том же адресе и порту панели. Внешние службы не подменяются.
export function browserURLForPanel(value: string | undefined, origin: string): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    const panel = new URL(origin);
    if (url.protocol !== "http:" && url.protocol !== "https:") return undefined;
    if (url.hostname === panel.hostname && url.protocol === panel.protocol) {
      url.port = panel.port;
    }
    return url.toString();
  } catch {
    return undefined;
  }
}

export async function request<T>(
  token: string,
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...options,
    signal: options.signal
      ? AbortSignal.any([options.signal, AbortSignal.timeout(70_000)])
      : AbortSignal.timeout(70_000),
    credentials: "same-origin",
    headers: {
      Authorization: `Bearer ${token}`,
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...options.headers,
    },
  });
  let data: T | { error?: string } | null;
  try {
    data = await response.json() as T | { error?: string } | null;
  } catch (error) {
    // Полученный HTTP-код сохраняем даже без JSON; обрыв успешного ответа не
    // превращаем в ошибку формата: это может быть тайм-аут или сетевой сбой.
    if (response.ok && !(error instanceof SyntaxError)) throw error;
    data = null;
  }
  if (!response.ok) {
    const message =
      data && typeof data === "object" && "error" in data
        ? data.error
        : undefined;
    if (response.status === 401)
      throw new APIError(response.status,
        "Сервер не принял сеанс входа. Войдите снова.",
      );
    throw new APIError(response.status, message || `Сервер вернул ошибку ${response.status}`);
  }
  if (data === null)
    throw new APIResponseError();
  return data as T;
}

export function errorMessage(error: unknown): string {
  if (!(error instanceof Error) && !(error instanceof DOMException)) return "Не удалось выполнить запрос.";
  if (error.name === "TimeoutError")
    return "Сервер не ответил вовремя. Обновите статус операции.";
  if (error.name === "TypeError")
    return "Браузер не получил полный ответ сервера. Проверьте соединение и обновите состояние операции.";
  return error.message;
}
