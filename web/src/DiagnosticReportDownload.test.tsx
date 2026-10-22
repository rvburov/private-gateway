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
  it.each(["privacy", "server"] as const)("builds the %s report only on click and lets Safari open its Blob", (kind) => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-23T10:00:00Z"));
    const create = vi.fn(() => "blob:local-diagnostic-report");
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    });
    const report = vi.fn(() => ({ kind, exportedAt: new Date().toISOString() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.isConnected).toBe(true);
      expect(this.download).toMatch(new RegExp(`^private-gateway-${kind}-diagnostics-.*\\.json$`));
    });
    render(<DiagnosticReportDownload kind={kind} report={report} checkedAt={Date.now()} busy={false} />);
    expect(report).not.toHaveBeenCalled();
    expect(create).not.toHaveBeenCalled();
    vi.advanceTimersByTime(5 * 60_000);
    fireEvent.click(screen.getByRole("button", {
      name: kind === "privacy" ? "Скачать отчёт безопасности JSON" : "Скачать отчёт сервера JSON",
    }));
    expect(report).toHaveBeenCalledOnce();
    expect(report.mock.results[0].value.exportedAt).toBe("2026-10-23T10:05:00.000Z");
    expect(create).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    expect(revoke).not.toHaveBeenCalled();
    expect(document.querySelector("a[download]")).toBeNull();
    vi.advanceTimersByTime(1_000);
    expect(revoke).toHaveBeenCalledWith("blob:local-diagnostic-report");
  });
});
