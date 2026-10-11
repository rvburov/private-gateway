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
  const data = (await response.json().catch(() => null)) as
    T | { error?: string } | null;
  if (!response.ok) {
    const message =
      data && typeof data === "object" && "error" in data
        ? data.error
        : undefined;
    if (response.status === 401)
      throw new Error(
        "Токен недействителен. Проверьте GATEWAY_API_TOKEN и войдите снова.",
      );
    throw new Error(message || `Сервер вернул ошибку ${response.status}`);
  }
  if (data === null)
    throw new Error("Сервер вернул ответ в неизвестном формате.");
  return data as T;
}

export function errorMessage(error: unknown): string {
  if (!(error instanceof Error)) return "Не удалось выполнить запрос.";
  if (error.name === "TimeoutError")
    return "Сервер не ответил вовремя. Обновите статус операции.";
  if (error.name === "TypeError")
    return "Не удалось подключиться к API. Проверьте сервер и SSH-туннель.";
  return error.message;
}
