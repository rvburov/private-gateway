// ASCII-представление остаётся обычным JSON, даже если редактор выбрал Windows-1251.
// Экранируем кодовые единицы UTF-16 отдельно, сохраняя пары суррогатов для эмодзи.
export function serializeDiagnosticJSON(report: unknown): string {
  const content = JSON.stringify(report, null, 2);
  if (typeof content !== "string") throw new Error("Отчёт недоступен.");
  return content.replace(/[\u007f-\uffff]/g, (character) =>
    `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`);
}
