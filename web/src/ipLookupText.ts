import { ipLookupFields, ipLookupSource, ipLookupValue, lookupStatusLabel, type IPLookupResult } from "./ipLookup";

// Ручной отчёт не выдумывает результаты основной проверки, которую ещё не запускали.
export function manualIPDiagnosticReport(kind: "privacy" | "server", manualIPLookup: IPLookupResult, checkedAt: number, panelOrigin: string) {
  return {
    format: `private-gateway-${kind === "privacy" ? "privacy" : "server"}-diagnostics`,
    version: 1,
    exportedAt: new Date().toISOString(),
    checkedAt: new Date(checkedAt).toISOString(),
    panelOrigin,
    mainCheckPerformed: false,
    manualIPLookup,
  };
}

// Отчёт не копирует произвольные поля ответа или параметры сеанса.
export function formatIPLookupText(value: unknown): string {
  if (!value || typeof value !== "object" || Array.isArray(value)) return "Данные об IP недоступны.\n";
  const result = value as Record<string, unknown>;
  const lines = [
    `IP-адрес: ${ipLookupValue(result.ip)}`,
    `Источник: ${ipLookupSource}`,
    `Результат: ${lookupStatusLabel(result.status)}`,
    `Время проверки: ${ipLookupValue(result.checkedAt)}`,
  ];
  if (typeof result.detail === "string" && result.detail.trim()) lines.push(`Примечание: ${result.detail}`);
  for (const field of ipLookupFields(result.data)) lines.push(`${field.label}: ${field.value}`);
  return lines.join("\n") + "\n";
}
