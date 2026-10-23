import { useState } from "react";
import { ErrorNotice } from "./ui";
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
  function download() {
    setError(null);
    try {
      const data = report();
      const content = "\ufeff" + formatDiagnosticText(kind, data);
      const date = new Date(checkedAt).toISOString().replaceAll(":", "-");
      const url = URL.createObjectURL(new Blob([content], { type: "text/plain;charset=utf-8" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `private-gateway-${kind}-diagnostics-${date}.txt`;
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
            <p>Отчёт открывается как обычный текстовый файл с русскими подписями показателей.</p>
          </HelpTip>
        </div>
        <p className="hint">Текстовый файл на русском.</p>
      </div>
      <div className="button-group diagnostics-download-actions">
        <button className="button primary" disabled={busy} onClick={download}>
          Скачать отчёт
        </button>
      </div>
      <ErrorNotice error={error} />
    </section>
  );
}
