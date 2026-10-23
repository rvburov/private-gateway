import { describe, expect, it } from "vitest";
import { serializeDiagnosticJSON } from "./diagnosticJSON";

describe("portable diagnostic JSON", () => {
  it("preserves Russian and emoji even when the saved file is read as Windows-1251", () => {
    const report = {
      checkedAt: "2026-10-23T05:11:50.266Z",
      title: "База данных: всё в порядке — «да» 🙂",
      detail: "PostgreSQL отвечает." + String.fromCharCode(10, 9, 34, 92),
      "Русский ключ": "Ёжик, 東京, 😀",
      bytes: 1556480,
      mbps: null,
      ok: true,
    };
    const content = serializeDiagnosticJSON(report);
    const bytes = new TextEncoder().encode(content);
    expect([...bytes].every((byte) => byte < 128)).toBe(true);
    expect(bytes[0]).toBe(0x7b);
    expect(JSON.parse(content)).toEqual(report);
    const windowsText = new TextDecoder("windows-1251").decode(bytes);
    expect(JSON.parse(windowsText)).toEqual(report);
    expect(JSON.parse(windowsText).title).not.toContain("Р‘Р°Р·Р°");
  });

  it("rejects a missing report instead of downloading an undefined document", () => {
    expect(() => serializeDiagnosticJSON(undefined)).toThrow("Отчёт недоступен.");
  });
});
