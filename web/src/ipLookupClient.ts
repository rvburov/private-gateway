import { APIError, errorMessage, request } from "./api";
import type { IPLookupResult } from "./ipLookup";

const statuses = new Set(["available", "partial", "not_configured", "unavailable", "rate_limited"]);

// В запрос передаётся измеренный или явно введённый адрес, а не адрес HTTP-соединения с API.
export async function lookupIP(token: string, ip: string, signal: AbortSignal): Promise<IPLookupResult> {
  try {
    const value = await request<IPLookupResult>(token, "/ip-lookup", {
      method: "POST",
      body: JSON.stringify({ ip }),
      signal: AbortSignal.any([signal, AbortSignal.timeout(12_000)]),
    });
    signal.throwIfAborted();
    if (!value || !statuses.has(value.status) || typeof value.ip !== "string" ||
        value.source !== "IP2Location.io" || typeof value.detail !== "string" ||
        (value.data !== null && (typeof value.data !== "object" || Array.isArray(value.data)))) {
      throw new Error("Сервер вернул нераспознанный ответ проверки IP.");
    }
    return value;
  } catch (error) {
    signal.throwIfAborted();
    return {
      ip,
      source: "IP2Location.io",
      status: error instanceof APIError && error.status === 429 ? "rate_limited" : "unavailable",
      checkedAt: null,
      detail: errorMessage(error),
      data: null,
    };
  }
}
