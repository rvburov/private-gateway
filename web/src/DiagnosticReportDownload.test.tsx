import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { DiagnosticReportDownload } from "./DiagnosticReportDownload";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("diagnostic report download", () => {
  it.each(["privacy", "server"] as const)("offers one readable %s report with portable bytes and lets Safari open it", async (kind) => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-23T10:00:00Z"));
    const create = vi.fn((_blob: Blob | MediaSource) => "blob:local-diagnostic-report");
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    });
    const report = vi.fn(() => ({
      kind, exportedAt: new Date().toISOString(), checkedAt: new Date().toISOString(),
      server: [{ id: "database", title: "База данных", status: "ok", detail: "PostgreSQL отвечает." }],
      browser: [{ id: "ip", title: "Выход в интернет", status: "ok", detail: "Русский текст, ё и 🙂." }],
    }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.isConnected).toBe(true);
      expect(this.download).toMatch(new RegExp(`^private-gateway-${kind}-diagnostics-.*\\.txt$`));
    });
    render(<DiagnosticReportDownload kind={kind} report={report} checkedAt={Date.now()} busy={false} />);
    expect(report).not.toHaveBeenCalled();
    expect(create).not.toHaveBeenCalled();
    expect(document.querySelectorAll(".diagnostics-download-actions button")).toHaveLength(1);
    expect(screen.queryByText(/JSON/)).toBeNull();
    vi.advanceTimersByTime(5 * 60_000);
    fireEvent.click(screen.getByRole("button", { name: "Скачать отчёт" }));
    expect(report).toHaveBeenCalledOnce();
    expect(report.mock.results[0].value.exportedAt).toBe("2026-10-23T10:05:00.000Z");
    expect(create).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    expect(revoke).not.toHaveBeenCalled();
    expect(document.querySelector("a[download]")).toBeNull();
    vi.advanceTimersByTime(1_000);
    expect(revoke).toHaveBeenCalledWith("blob:local-diagnostic-report");
    const blob = create.mock.calls[0][0] as Blob;
    expect(blob.type).toBe("text/plain;charset=utf-8");
    const reader = new FileReader();
    const read = new Promise<Uint8Array>((resolve, reject) => {
      reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
      reader.onerror = () => reject(reader.error);
    });
    reader.readAsArrayBuffer(blob);
    await vi.runAllTimersAsync();
    const bytes = await read;
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
    const text = new TextDecoder("utf-8").decode(bytes);
    expect(text).toContain(kind === "privacy" ? "Русский текст, ё и 🙂." : "PostgreSQL отвечает.");
    expect(text).not.toContain("Р‘Р°Р·Р°");
  });

  it("disables the only download action during a run and keeps failures visible", () => {
    const report = vi.fn(() => { throw new Error("fixture failure"); });
    const view = render(<DiagnosticReportDownload kind="server" report={report} checkedAt={Date.now()} busy />);
    const download = screen.getByRole("button", { name: "Скачать отчёт" }) as HTMLButtonElement;
    expect(download.disabled).toBe(true);
    fireEvent.click(download);
    expect(report).not.toHaveBeenCalled();
    view.rerender(<DiagnosticReportDownload kind="server" report={report} checkedAt={Date.now()} busy={false} />);
    fireEvent.click(download);
    expect(report).toHaveBeenCalledOnce();
    expect(screen.getByRole("alert").textContent).toContain("Не удалось скачать отчёт в этом браузере.");
  });
});
