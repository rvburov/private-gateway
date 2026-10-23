import { useState } from "react";
import { ErrorNotice } from "./ui";
import { serializeDiagnosticJSON } from "./diagnosticJSON";
import { formatDiagnosticText } from "./diagnosticText";
import { HelpTip } from "./HelpTip";

// Каждый раздел передаёт только свой отчёт, собранный из явно перечисленных полей.
export function DiagnosticReportDownload({ kind, report, checkedAt, busy }: {
  kind: "privacy" | "server";
  report: () => unknown;
  checkedAt: number;
  busy: boolean;
}) {
  const [error, setError] = useState<unknown>(null);
  const title = kind === "privacy" ? "Отчёт о безопасности" : "Отчёт о сервере";
  function download(format: "txt" | "json") {
    setError(null);
    try {
      const data = report();
      const content = format === "txt"
        ? "\ufeff" + formatDiagnosticText(kind, data)
        : serializeDiagnosticJSON(data);
      const date = new Date(checkedAt).toISOString().replaceAll(":", "-");
      const type = format === "txt" ? "text/plain;charset=utf-8" : "application/json;charset=utf-8";
      const url = URL.createObjectURL(new Blob([content], { type }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `private-gateway-${kind}-diagnostics-${date}.${format}`;
      document.body.append(link);
      try { link.click(); } finally {
        link.remove();
        // Safari начинает скачивание асинхронно; даём браузеру открыть Blob.
        setTimeout(() => URL.revokeObjectURL(url), 1_000);
      }
    } catch {
      setError(new Error("Не удалось скачать отчёт в этом браузере."));
    }
  }
  return (
    <section className="card diagnostics-export" aria-label={title}>
      <div>
        <div className="diagnostics-title-with-help">
          <h2>{title}</h2>
          <HelpTip label={kind === "privacy" ? "Об отчёте о безопасности" : "Об отчёте о сервере"}>
            <p>
              {kind === "privacy"
                ? "Отчёт содержит IP-адреса, результаты проверки безопасности и сведения об устройстве и браузере."
                : "Отчёт содержит результаты проверки сервера, показатели его работы, скорости и задержки."}{" "}
              Скачивается на ваше устройство; передавайте его только тому, кому доверяете.
            </p>
            <p>Для чтения выберите текстовый отчёт по-русски. В JSON символы записаны кодами Unicode: программы восстанавливают исходный текст автоматически.</p>
          </HelpTip>
        </div>
        <p className="hint">TXT — для чтения, JSON — для программ.</p>
      </div>
      <div className="button-group diagnostics-download-actions">
        <button className="button primary" disabled={busy} onClick={() => download("txt")}>
          Скачать отчёт по-русски
        </button>
        <button className="button" disabled={busy} onClick={() => download("json")}>
          {kind === "privacy" ? "Скачать отчёт безопасности JSON" : "Скачать отчёт сервера JSON"}
        </button>
      </div>
      <ErrorNotice error={error} />
    </section>
  );
}
