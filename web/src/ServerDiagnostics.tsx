import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage, request } from "./api";
import { AdvancedDiagnostics } from "./AdvancedDiagnostics";
import { CheckList, type CheckResult } from "./CheckResults";
import { DiagnosticReportDownload } from "./DiagnosticReportDownload";
import { Icon } from "./icons";
import {
  connectionModes,
  runPerformance,
  savePerformanceHistory,
  type ConnectionMode,
  type PerformanceRun,
} from "./performance";
import { ErrorNotice } from "./ui";

export interface ServerCheckRun {
  server: CheckResult[] | null;
  serverError: string | null;
  performance: PerformanceRun | null;
  performanceError: string | null;
  checkedAt: number;
}

// Серверный отчёт не содержит сведений браузерной проверки, сеанса или профилей.
export function serverDiagnosticReport(result: ServerCheckRun, panelOrigin: string) {
  return {
    format: "private-gateway-server-diagnostics",
    version: 1,
    exportedAt: new Date().toISOString(),
    checkedAt: new Date(result.checkedAt).toISOString(),
    panelOrigin,
    server: result.server,
    serverError: result.serverError,
    performance: result.performance,
    performanceError: result.performanceError,
  };
}

async function runServerChecks(
  token: string,
  mode: ConnectionMode,
  signal: AbortSignal,
  onProgress: (phase: string) => void,
): Promise<ServerCheckRun> {
  signal.throwIfAborted();
  onProgress("Проверяем работу сервера и VLESS…");
  let server: CheckResult[] | null = null;
  let serverError: string | null = null;
  try {
    server = (await request<{ checks: CheckResult[] }>(token, "/checks", { signal })).checks;
  } catch (error) {
    signal.throwIfAborted();
    serverError = errorMessage(error);
  }
  signal.throwIfAborted();
  let performance: PerformanceRun | null = null;
  let performanceError: string | null = null;
  try {
    // Замер скорости начинается после проверки VLESS, чтобы они не занимали канал вместе.
    performance = await runPerformance(token, mode, signal, onProgress);
  } catch (error) {
    signal.throwIfAborted();
    performanceError = errorMessage(error);
  }
  signal.throwIfAborted();
  return { server, serverError, performance, performanceError, checkedAt: Date.now() };
}

export function ServerDiagnostics({ token }: { token: string }) {
  const client = useQueryClient();
  const [mode, setMode] = useState<ConnectionMode>("current");
  const [progress, setProgress] = useState("");
  const [stopped, setStopped] = useState(false);
  const run = useQuery({
    queryKey: ["server-checks"],
    queryFn: async ({ signal }) => {
      const result = await runServerChecks(token, mode, signal, setProgress);
      signal.throwIfAborted();
      if (result.performance) savePerformanceHistory(result.performance);
      return result;
    },
    enabled: false,
    retry: false,
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  });
  const result = run.data;
  const problems = result?.server?.filter((check) => check.status === "fail").length ?? 0;
  const warnings = result?.server?.filter((check) => check.status === "warn").length ?? 0;
  return (
    <>
      <section className="card check-run" aria-label="Проверка сервера">
        <div className="check-run-text">
          <h2>Проверить работу сервера</h2>
          <p>
            Проверим базу данных, конфигурацию и процесс Xray, маскировку REALITY, сертификат панели и
            передачу VLESS-трафика. Затем измерим скорость, задержку и стабильность HTTP-запросов до
            панели и Cloudflare, снимем доступные показатели нагрузки. До полутора минут и до 40 МБ
            трафика на один запуск.
          </p>
          <p className="hint">
            Замер до Cloudflare проверяет выход в интернет через текущее подключение браузера.
            speed.cloudflare.com увидит ваш внешний адрес; токен входа и VPN-профиль ему не передаются.
            Проверки IP, DNS, WebRTC и отпечатка браузера находятся в разделе «Проверка».
          </p>
        </div>
        <div className="check-run-actions">
          <label htmlFor="server-connection-mode">Как подключены сейчас</label>
          <select
            id="server-connection-mode"
            value={mode}
            disabled={run.isFetching}
            onChange={(event) => setMode(event.target.value as ConnectionMode)}
          >
            {connectionModes.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
          </select>
          {run.isFetching ? (
            <button className="button" onClick={() => {
              void client.cancelQueries({ queryKey: ["server-checks"] });
              setStopped(true);
              setProgress("");
            }}>
              <Icon name="close" size={18} />Остановить
            </button>
          ) : (
            <button className="button primary" onClick={() => { setStopped(false); void run.refetch(); }}>
              <Icon name="server" size={18} />{result ? "Проверить сервер снова" : "Проверить сервер"}
            </button>
          )}
        </div>
        <p className="hint check-comparison-note">
          Для сравнения выполните отдельные замеры с VPN и без него, затем в другой сети. Переключайте
          VPN и сеть самостоятельно; закройте загрузки и видео. Выбранная подпись сохраняется вместе с числами.
        </p>
        {run.isFetching && <p className="check-progress" role="status" aria-live="polite">{progress || "Начинаем проверку сервера…"}</p>}
        {stopped && <p className="check-progress" role="status">Проверка сервера остановлена. Незавершённый замер не добавлен в историю.</p>}
        {result && (
          <div className={`notice check-summary ${problems ? "error" : warnings || result.serverError ? "warning-notice" : "success-notice"}`} role="status">
            <Icon name={problems ? "close" : warnings || result.serverError ? "alert" : "check"} size={17} />
            <span>
              {result.serverError
                ? "Не удалось завершить проверку работы сервера. Доступные результаты замеров приведены ниже."
                : problems
                  ? `Проверки сервера: проблем — ${problems}${warnings ? `, замечаний — ${warnings}` : ""}. Подробности ниже.`
                  : warnings
                    ? `Проверки сервера: замечаний — ${warnings}. Подробности ниже.`
                    : "По выполненным проверкам работы сервера проблем не найдено."}{" "}
              Проверено в {new Date(result.checkedAt).toLocaleTimeString("ru-RU", { timeStyle: "short" })}.
            </span>
          </div>
        )}
        <ErrorNotice error={run.error} />
      </section>
      <section className="card" aria-label="Работа сервера">
        <div className="card-heading">
          <div><h2>Работа сервера</h2><p>Проверки, которые сервер выполняет сам.</p></div>
          <Icon name="server" size={23} />
        </div>
        {result?.server ? <CheckList checks={result.server} /> : result?.serverError ? (
          <div className="card-body"><ErrorNotice error={new Error(result.serverError)} /></div>
        ) : <div className="loading">{run.isFetching ? "Проверяем…" : "Нажмите «Проверить сервер»."}</div>}
        <div className="card-footer">
          <Icon name="lock" size={15} />
          <span>
            Для проверки трафика сервер подключается к Xray с профилем самого старого активного устройства:
            его счётчик вырастет на несколько килобайт. Профиль никуда не передаётся.
          </span>
        </div>
      </section>
      <AdvancedDiagnostics result={result} busy={run.isFetching} />
      {result && <DiagnosticReportDownload
        kind="server"
        report={() => serverDiagnosticReport(result, window.location.origin)}
        checkedAt={result.checkedAt}
        busy={run.isFetching}
      />}
    </>
  );
}
