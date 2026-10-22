import { useState } from "react";
import { ErrorNotice } from "./ui";

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
      const content = JSON.stringify(report(), null, 2);
      if (typeof content !== "string") throw new Error("Отчёт недоступен.");
      const date = new Date(checkedAt).toISOString().replaceAll(":", "-");
      const url = URL.createObjectURL(new Blob([content], { type: "application/json" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `private-gateway-${kind}-diagnostics-${date}.json`;
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
        <h2>{title}</h2>
        <p className="hint">
          {kind === "privacy"
            ? "JSON содержит IP-адреса, результаты проверки безопасности и сведения об устройстве и браузере."
            : "JSON содержит результаты проверки сервера, показатели его работы, скорости и задержки."}{" "}
          Скачивается на ваше устройство; передавайте его только тому, кому доверяете.
        </p>
      </div>
      <button className="button" disabled={busy} onClick={download}>
        {kind === "privacy" ? "Скачать отчёт безопасности JSON" : "Скачать отчёт сервера JSON"}
      </button>
      <ErrorNotice error={error} />
    </section>
  );
}
