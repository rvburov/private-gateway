import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { errorMessage, request } from "./api";
import { AdvancedDiagnostics } from "./AdvancedDiagnostics";
import { CheckList, type CheckResult } from "./CheckResults";
import { DiagnosticReportDownload } from "./DiagnosticReportDownload";
import { DiagnosticProgress, type DiagnosticStage } from "./DiagnosticProgress";
import { HelpTip } from "./HelpTip";
import { Icon } from "./icons";
import {
  connectionModes,
  runPerformance,
  savePerformanceHistory,
  type ConnectionMode,
  type PerformanceRun,
} from "./performance";
import { ErrorNotice } from "./ui";
import { IPReputation } from "./IPReputation";
import { IPLookupManual, type ManualIPLookupState } from "./IPLookupManual";
import { isValidPublicIP, type IPLookupResult } from "./ipLookup";
import { lookupIP } from "./ipLookupClient";
import { manualIPDiagnosticReport } from "./ipLookupText";

export interface ServerCheckRun {
  server: CheckResult[] | null;
  serverError: string | null;
  performance: PerformanceRun | null;
  performanceError: string | null;
  checkedAt: number;
  serverIPLookup?: IPLookupResult | null;
}

// Серверный отчёт не содержит сведений браузерной проверки, сеанса или профилей.
export function serverDiagnosticReport(result: ServerCheckRun, panelOrigin: string, manualIPLookup?: IPLookupResult | null) {
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
    serverIPLookup: result.serverIPLookup ?? null,
    manualIPLookup: manualIPLookup ?? null,
  };
}

async function runServerChecks(
  token: string,
  mode: ConnectionMode,
  signal: AbortSignal,
  onProgress: (stage: DiagnosticStage) => void,
): Promise<ServerCheckRun> {
  signal.throwIfAborted();
  onProgress({ label: "Проверяем Xray и VLESS…", percent: 0 });
  let server: CheckResult[] | null = null;
  let serverError: string | null = null;
  try {
    server = (await request<{ checks: CheckResult[] }>(token, "/checks", { signal })).checks;
  } catch (error) {
    signal.throwIfAborted();
    serverError = errorMessage(error);
  }
  signal.throwIfAborted();
  const trafficIP = server?.find((check) => check.id === "traffic" &&
    (check.status === "ok" || check.status === "warn") && check.ip && isValidPublicIP(check.ip))?.ip;
  let serverIPLookup: IPLookupResult | null = null;
  if (trafficIP) {
    onProgress({ label: "Уточняем IP выхода сервера…", percent: 10 });
    serverIPLookup = await lookupIP(token, trafficIP, signal);
    signal.throwIfAborted();
  }
  onProgress({ label: "Начинаем замеры…", percent: 20 });
  let performance: PerformanceRun | null = null;
  let performanceError: string | null = null;
  try {
    // Замер скорости начинается после проверки VLESS, чтобы они не занимали канал вместе.
    performance = await runPerformance(token, mode, signal, (label, percent) => {
      signal.throwIfAborted();
      onProgress({ label, percent: 20 + Math.floor(percent * 0.8) });
    });
  } catch (error) {
    signal.throwIfAborted();
    performanceError = errorMessage(error);
  }
  signal.throwIfAborted();
  return { server, serverError, performance, performanceError, serverIPLookup, checkedAt: Date.now() };
}

export function ServerDiagnostics({ token }: { token: string }) {
  const client = useQueryClient();
  const [manual, setManual] = useState<ManualIPLookupState>({ data: undefined, checkedAt: 0, busy: false });
  const [mode, setMode] = useState<ConnectionMode>("current");
  const [progress, setProgress] = useState<DiagnosticStage>({ label: "Начинаем проверку…", percent: 0 });
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
  const incompleteMeasurements = !!result && (!result.performance || !!result.performanceError || !!result.performance.serverError ||
    [result.performance.panel, result.performance.internet].some((path) =>
      path.download.mbps === null || path.upload.mbps === null || !!path.download.error || !!path.upload.error ||
      path.latency.medianMs === null || path.latency.errorCount > 0 || !!path.latency.error));
  const hasWarnings = warnings > 0 || !!result?.serverError || incompleteMeasurements;
  const previousResult = run.isFetching || stopped || run.isError;
  return (
    <>
      <section className="card check-run" aria-label="Проверка сервера">
        <div className="check-run-text">
          <div className="diagnostics-title-with-help">
            <h2>Проверить работу сервера</h2>
            <HelpTip label="О проверке сервера">
              <p>Проверяем базу данных, Xray, REALITY, сертификат панели и VLESS-трафик. Затем измеряем скорость, задержку HTTP и нагрузку сервера.</p>
              <p>Внешний замер идёт до speed.cloudflare.com через текущее подключение браузера. Сервис увидит внешний IP; токен входа и VPN-профиль ему не передаются. Проверки утечек и браузера — в разделе «Проверка».</p>
              <p>Для сравнения повторите замер с VPN, без него и в другой сети. Переключайте подключение самостоятельно; закройте загрузки и видео. Выбранная подпись сохраняется с результатом.</p>
              <p>Процент показывает завершённые этапы, а не оставшееся время. 100% означает завершение; недоступные замеры и ошибки отмечаются отдельно. Остановленный замер в историю не записывается.</p>
            </HelpTip>
          </div>
          <p>Xray, скорость и задержки.</p>
          <p className="hint">До 1,5 минуты · до 40 МБ за запуск.</p>
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
            }}>
              <Icon name="close" size={18} />Остановить
            </button>
          ) : (
            <button className="button primary" onClick={() => {
              setStopped(false);
              setProgress({ label: "Начинаем проверку…", percent: 0 });
              void run.refetch();
            }}>
              <Icon name="server" size={18} />{result ? "Проверить сервер снова" : "Проверить сервер"}
            </button>
          )}
        </div>
        {result && (
          <div className={`notice check-summary ${previousResult ? "" : problems ? "error" : hasWarnings ? "warning-notice" : "success-notice"}`} role="status">
            <Icon name={previousResult ? "info" : problems ? "close" : hasWarnings ? "alert" : "check"} size={17} />
            <span>
              {previousResult && "Предыдущая проверка: "}
              {result.serverError
                ? "Не удалось завершить проверку работы сервера. Доступные результаты замеров приведены ниже."
                : problems
                  ? `Проверки сервера: проблем — ${problems}${warnings ? `, замечаний — ${warnings}` : ""}. Подробности ниже.`
                  : warnings
                    ? `Проверки сервера: замечаний — ${warnings}. Подробности ниже.`
                    : incompleteMeasurements
                      ? "Проверка закончена, часть замеров недоступна. Подробности ниже."
                      : "По выполненным проверкам работы сервера проблем не найдено."}{" "}
              Проверено в {new Date(result.checkedAt).toLocaleTimeString("ru-RU", { timeStyle: "short" })}.
            </span>
          </div>
        )}
        <ErrorNotice error={run.error} />
        <DiagnosticProgress stage={progress} busy={run.isFetching} stopped={stopped} failed={run.isError}
          complete={!!result} label="Ход проверки сервера" stoppedLabel="Проверка сервера остановлена." />
      </section>
      <section className="card" aria-label="Работа сервера">
        <div className="card-heading">
          <div>
            <div className="diagnostics-title-with-help">
              <h2>Работа сервера</h2>
              <HelpTip label="О проверке работы Xray">
                <p>Эти проверки выполняет сам сервер. Для проверки трафика он подключается к Xray с профилем самого старого активного устройства: его счётчик вырастет на несколько килобайт. Профиль никуда не передаётся.</p>
              </HelpTip>
            </div>
            <p>Состояние служб и VLESS.</p>
          </div>
          <Icon name="server" size={23} />
        </div>
        {result?.server ? <CheckList checks={result.server} /> : result?.serverError ? (
          <div className="card-body"><ErrorNotice error={new Error(result.serverError)} /></div>
        ) : <div className="loading">{run.isFetching ? "Проверяем…" : "Нажмите «Проверить сервер»."}</div>}
      </section>
      {result && <IPReputation result={result.serverIPLookup ?? null} title="IP выхода сервера"
        description={result.serverIPLookup ? "Выход, измеренный серверной проверкой VLESS; не IP браузера." : "IP выхода не подтверждён серверной проверкой VLESS. Сведения об адресе не запрашивались."} />}
      <AdvancedDiagnostics result={result} busy={run.isFetching} />
      <IPLookupManual token={token} scope="server" busy={run.isFetching} onStateChange={setManual} />
      {(result || manual.data) && <DiagnosticReportDownload
        kind="server"
        report={() => result
          ? serverDiagnosticReport(result, window.location.origin, manual.data)
          : manualIPDiagnosticReport("server", manual.data!, manual.checkedAt, window.location.origin)}
        checkedAt={result?.checkedAt ?? manual.checkedAt}
        busy={run.isFetching || manual.busy}
      />}
    </>
  );
}
