import { useEffect, useState, type ReactNode } from "react";
import type { ServerCheckRun } from "./ServerDiagnostics";
import { HelpTip } from "./HelpTip";
import {
  clearPerformanceHistory,
  connectionModes,
  loadPerformanceHistory,
  type PathMeasurement,
  type PerformanceHistoryEntry,
  type PerformanceRun,
  type ServerSnapshot,
} from "./performance";
import { bytes } from "./ui";

const number = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 });
const measured = (value: number | null | undefined, unit: string) =>
  typeof value === "number" && Number.isFinite(value)
    ? <>{number.format(value)} <span className="measurement-unit">{unit}</span></>
    : "Недоступно";
const size = (value: number | null | undefined) =>
  typeof value === "number" && Number.isFinite(value) ? bytes(value) : "Недоступно";
const modeName = (mode: string) => connectionModes.find((item) => item.value === mode)?.label ?? "Текущее подключение";
const time = (value: number) => new Date(value).toLocaleString("ru-RU", { dateStyle: "short", timeStyle: "short" });

function Measurement({ title, path, method }: { title: string; path: PathMeasurement; method?: PerformanceRun["method"] }) {
  const errors = [path.latency.error, path.download.error, path.upload.error].filter(Boolean);
  const latencyLabel = method === "browser-http-v2" ? "Время до начала ответа HTTP" : "Задержка HTTP";
  const downloadIncomplete = path.download.incomplete;
  const uploadIncomplete = path.upload.incomplete;
  return (
    <section className="measurement" aria-label={title}>
      <h3>{title}</h3>
      <dl className="measurement-values">
        <div><dt>Получение</dt><dd>{measured(path.download.mbps, "Мбит/с")}</dd></div>
        <div><dt>Отправка</dt><dd>{measured(path.upload.mbps, "Мбит/с")}</dd></div>
        <div><dt>{latencyLabel}, медиана</dt><dd>{measured(path.latency.medianMs, "мс")}</dd></div>
        <div><dt>{latencyLabel}, p95</dt><dd>{measured(path.latency.p95Ms, "мс")}</dd></div>
        <div><dt>Колебание задержки</dt><dd>{measured(path.latency.jitterMs, "мс")}</dd></div>
        <div><dt>Пробы HTTP</dt><dd>{path.latency.successes} из {path.latency.attempts} успешны; ошибок: {path.latency.errorCount}</dd></div>
      </dl>
      {downloadIncomplete && <p className="hint">Скачивание не завершено: получено {bytes(downloadIncomplete.bytes)} из {bytes(downloadIncomplete.expectedBytes)} за {number.format(downloadIncomplete.durationMs / 1_000)} с.</p>}
      {uploadIncomplete && <p className="hint">Отправка не завершена: объём {bytes(uploadIncomplete.expectedBytes)} не подтверждён за {number.format(uploadIncomplete.durationMs / 1_000)} с.</p>}
      {errors.length > 0 && <p className="hint">{[...new Set(errors)].join(" ")}</p>}
      <p className="hint">
        Данные замера: получено {bytes(path.download.bytes)}, отправка в завершённых пробах — {bytes(path.upload.bytes)}.
      </p>
    </section>
  );
}

function difference(before: number | null | undefined, after: number | null | undefined) {
  if (typeof before !== "number" || typeof after !== "number" || after < before) return null;
  return after - before;
}

function ServerResources({ run }: { run: PerformanceRun }) {
  const before = run.serverBefore;
  const after = run.serverAfter ?? before;
  if (!after) return <p className="card-body hint">{run.serverError || "Сведения о нагрузке сервера недоступны."}</p>;
  const elapsed = before && run.serverAfter
    ? (Date.parse(run.serverAfter.sampledAt) - Date.parse(before.sampledAt)) / 1_000
    : 0;
  const systemTotal = difference(before?.system.cpu?.totalTicks, after.system.cpu?.totalTicks);
  const steal = difference(before?.system.cpu?.stealTicks, after.system.cpu?.stealTicks);
  const stealPercent = systemTotal && steal !== null ? steal / systemTotal * 100 : null;
  const vpnAvailable = after.vpn.status === "ok";
  const workerElapsed = before?.vpn.sampledAt && after.vpn.sampledAt
    ? (Date.parse(after.vpn.sampledAt) - Date.parse(before.vpn.sampledAt)) / 1_000
    : 0;
  const sameWorker = vpnAvailable && before?.vpn.status === "ok" && workerElapsed > 0;
  const rx = sameWorker ? difference(before.vpn.network?.rxBytes, after.vpn.network?.rxBytes) : null;
  const tx = sameWorker ? difference(before.vpn.network?.txBytes, after.vpn.network?.txBytes) : null;
  const segments = sameWorker ? difference(before.vpn.tcp?.outSegments, after.vpn.tcp?.outSegments) : null;
  const retransmissions = sameWorker ? difference(before.vpn.tcp?.retransmittedSegments, after.vpn.tcp?.retransmittedSegments) : null;
  return (
    <details className="check-fingerprint server-resources">
      <summary>Нагрузка и сведения о сервере</summary>
      {run.serverError && <p className="hint">{run.serverError}</p>}
      <dl>
        <div><dt>Система API</dt><dd>{after.runtime.os} / {after.runtime.arch}; {after.runtime.goVersion}</dd></div>
        <div><dt>Версия</dt><dd>{after.runtime.version || "Недоступно"}{after.runtime.revision ? ` (${after.runtime.revision.slice(0, 12)})` : ""}</dd></div>
        <div><dt>Видимые процессоры</dt><dd>{after.runtime.cpuCount}</dd></div>
        <div><dt>Время работы системы</dt><dd>{measured(after.system.uptimeSeconds === null ? null : after.system.uptimeSeconds / 3_600, "ч")}</dd></div>
        <div><dt>Нагрузка 1 / 5 / 15 минут</dt><dd>{after.system.loadAverage?.map((value) => number.format(value)).join(" / ") || "Недоступно"}</dd></div>
        <div><dt>Память системы, доступно / всего</dt><dd>{size(after.system.memoryAvailableBytes)} / {size(after.system.memoryTotalBytes)}</dd></div>
        <div><dt>CPU steal за время замера</dt><dd>{measured(stealPercent, "%")}</dd></div>
      </dl>
      <p className="hint">{after.system.scope} CPU steal — время ожидания процессора виртуальной машиной; один замер не устанавливает причину низкой скорости.</p>
      <CgroupResources title="Контейнер API" values={after.api} before={before?.api} elapsed={elapsed} />
      <p className="hint">{after.api.scope}</p>
      {vpnAvailable ? (
        <>
          <CgroupResources title="Контейнер VPN" values={after.vpn.cgroup} before={sameWorker ? before.vpn.cgroup : null} elapsed={workerElapsed} />
          <dl>
            <div><dt>Процесс Xray</dt><dd>{after.vpn.running === null ? "Недоступно" : after.vpn.running ? "Работает" : "Остановлен"}</dd></div>
            <div><dt>Получено / отправлено VPN за замер</dt><dd>{size(rx)} / {size(tx)}</dd></div>
            <div><dt>Повторные отправки TCP / исходящие сегменты</dt><dd>{retransmissions === null || segments === null ? "Недоступно" : `${retransmissions} / ${segments}`}</dd></div>
          </dl>
          <p className="hint">{after.vpn.scope} Счётчики относятся ко всем подключениям Xray за время замера. Повторные отправки TCP не равны потере пакетов на вашем устройстве. Снимок VPN: {after.vpn.sampledAt ? new Date(after.vpn.sampledAt).toLocaleString("ru-RU") : "Недоступно"}.</p>
        </>
      ) : <p className="hint">VPN: {after.vpn.detail || "Свежие показатели недоступны."}</p>}
      <p className="hint">Снимок: {new Date(after.sampledAt).toLocaleString("ru-RU")}. Недоступные показатели не заменяются нулями.</p>
    </details>
  );
}

function CgroupResources({ title, values, before, elapsed }: {
  title: string;
  values: ServerSnapshot["vpn"]["cgroup"];
  before?: ServerSnapshot["vpn"]["cgroup"];
  elapsed: number;
}) {
  if (!values) return <p className="hint">{title}: показатели ресурсов недоступны.</p>;
  const usage = difference(before?.cpuUsageUsec, values.cpuUsageUsec);
  const cores = usage !== null && elapsed > 0 ? usage / (elapsed * 1_000_000) : null;
  const throttled = difference(before?.cpuThrottledUsec, values.cpuThrottledUsec);
  return (
    <>
      <h3 className="diagnostics-subheading">{title}</h3>
      <dl>
        <div><dt>Память / лимит</dt><dd>{size(values.memoryBytes)} / {values.memoryLimitBytes === null ? "Лимит не задан или недоступен" : size(values.memoryLimitBytes)}</dd></div>
        <div><dt>Лимит CPU</dt><dd>{measured(values.cpuQuotaCores, "ядра")}</dd></div>
        <div><dt>CPU за время замера</dt><dd>{values.cpuQuotaCores && cores !== null ? measured(cores / values.cpuQuotaCores * 100, "% лимита") : measured(cores, "ядра")}</dd></div>
        <div><dt>Ограничение CPU контейнера за замер</dt><dd>{measured(throttled === null ? null : throttled / 1_000, "мс")}</dd></div>
      </dl>
    </>
  );
}

function HistoryCell({ label, value }: { label: string; value: ReactNode }) {
  return (
    <td role="cell" data-label={label}>
      <span className="diagnostics-cell-label" aria-hidden="true">{label}</span>
      <span className="diagnostics-cell-value">{value}</span>
    </td>
  );
}

function TransportComparison({ history }: { history: PerformanceHistoryEntry[] }) {
  const latest = [...history].sort((first, second) => second.checkedAt - first.checkedAt);
  const tcp = latest.find((entry) => entry.mode === "tcp" && entry.method === "browser-http-v2");
  const xhttp = latest.find((entry) => entry.mode === "xhttp" && entry.method === "browser-http-v2");
  if (!tcp || !xhttp) return null;
  const usable = (value: number | null, failed: boolean) => !failed && value !== null && Number.isFinite(value) && value > 0;
  const bothDownload = [tcp, xhttp].every((entry) => usable(entry.internet.downloadMbps, entry.internet.downloadError !== false));
  const bothUpload = [tcp, xhttp].every((entry) => usable(entry.internet.uploadMbps, entry.internet.uploadError !== false));
  const bothLatency = [tcp, xhttp].every((entry) => usable(entry.internet.latencyMs, entry.internet.latencyError !== false || entry.internet.errorCount > 0));
  return (
    <div className="transport-comparison">
      <h3>TCP и XHTTP до Cloudflare</h3>
      <p className="hint">Последний замер каждой метки по одной методике. Профиль выбран вручную; условия и узел Cloudflare могут отличаться.</p>
      <div className="transport-comparison-scroll">
        <table className="transport-comparison-table" aria-label="Последние замеры TCP и XHTTP">
          <thead><tr><th scope="col">Метка</th><th scope="col">Получение</th><th scope="col">Отправка</th><th scope="col">До начала ответа HTTP, медиана</th></tr></thead>
          <tbody>{[tcp, xhttp].map((entry) => (
            <tr key={entry.mode}>
              <th scope="row">{modeName(entry.mode)}<span>{time(entry.checkedAt)}</span></th>
              <td>{bothDownload ? measured(entry.internet.downloadMbps, "Мбит/с") : "Недостаточно данных"}</td>
              <td>{bothUpload ? measured(entry.internet.uploadMbps, "Мбит/с") : "Недостаточно данных"}</td>
              <td>{bothLatency ? measured(entry.internet.latencyMs, "мс") : "Недостаточно данных"}</td>
            </tr>
          ))}</tbody>
        </table>
      </div>
    </div>
  );
}

export function AdvancedDiagnostics({ result, busy }: { result?: ServerCheckRun; busy: boolean }) {
  const [history, setHistory] = useState<PerformanceHistoryEntry[]>(loadPerformanceHistory);
  useEffect(() => { setHistory(loadPerformanceHistory()); }, [result?.performance?.checkedAt]);
  const performance = result?.performance;
  return (
    <>
      <section className="card" aria-label="Скорость и задержка">
        <div className="card-heading">
          <div>
            <div className="diagnostics-title-with-help">
              <h2>Скорость и задержка</h2>
              <HelpTip label="О замерах скорости и задержки">
                <p>Клиент VPN может исключать адрес своего сервера из туннеля. Скорость до панели показывает доступность канала до VPS; она сама по себе не измеряет передачу через VLESS.</p>
                <p>Внешний замер идёт до Cloudflare. Его ближайший узел может меняться после переключения VPN. Повторите замеры несколько раз в одинаковых условиях.</p>
                <p>Новые замеры: 2 прогрева и 10 HTTP-проб. Время до начала ответа считается от запуска запроса браузером до получения заголовков; оно может включать установку соединения. Браузер может повторно использовать соединения, поэтому это не замер отдельных новых соединений через curl.</p>
                <p>Объём отправки учитывается по телам запросов с ожидаемым успешным ответом. Панель подтверждает точное число полученных байтов; внешний сервис Cloudflare такого счётчика в ответе не предоставляет.</p>
                <p>Медиана — среднее по порядку значение; p95 — верхняя граница для 95% проб. При 10 успешных пробах p95 совпадает с самой долгой. Колебания и ошибки HTTP не измеряют потерю IP-пакетов и не устанавливают причину низкой скорости.</p>
              </HelpTip>
            </div>
            <p>{performance ? `${modeName(performance.mode)} · ${time(performance.checkedAt)}` : "До панели и в интернет."}</p>
          </div>
        </div>
        {performance ? (
          <>
            <div className="measurement-columns">
              <Measurement title="Канал до панели" path={performance.panel} method={performance.method} />
              <Measurement title="Интернет через текущее подключение" path={performance.internet} method={performance.method} />
            </div>
            <ServerResources run={performance} />
          </>
        ) : <div className="card-body"><p className="hint">{result?.performanceError || "Нажмите «Проверить подключение», чтобы начать замер."}</p></div>}
      </section>
      <section className="card" aria-label="История замеров">
        <div className="card-heading">
          <div>
            <div className="diagnostics-title-with-help">
              <h2>История замеров</h2>
              <HelpTip label="Об истории замеров">
                <p>Сравнивайте результаты в одинаковых условиях. Скорость измеряется до Cloudflare, задержка — HTTP-запросами.</p>
                <p>Новые записи измеряют время до начала ответа HTTP, старые — длительность полного запроса. В сравнении TCP и XHTTP участвуют только новые записи; метрика с ошибкой или без результата не сравнивается.</p>
                <p>В этом браузере сохраняются числа, время и подпись подключения. IP-адреса, сведения об устройстве и токен входа в историю не записываются.</p>
              </HelpTip>
            </div>
            <p>Последние 12 замеров в этом браузере.</p>
          </div>
          <button className="button" disabled={busy || history.length === 0} onClick={() => { clearPerformanceHistory(); setHistory([]); }}>Очистить историю</button>
        </div>
        <TransportComparison history={history} />
        {history.length > 0 ? (
          <div className="diagnostics-table-scroll">
            <table className="diagnostics-table" role="table">
              <caption className="sr-only">Сравнение замеров: скорость до Cloudflare и задержка HTTP.</caption>
              <thead role="rowgroup"><tr role="row"><th role="columnheader" scope="col">Время и подключение</th><th role="columnheader" scope="col">Получение</th><th role="columnheader" scope="col">Отправка</th><th role="columnheader" scope="col">Ответ HTTP: интернет</th><th role="columnheader" scope="col">Ответ HTTP: панель</th><th role="columnheader" scope="col">Ошибки HTTP в интернете</th></tr></thead>
              <tbody role="rowgroup">{history.map((entry, index) => (
                <tr role="row" key={`${entry.checkedAt}-${index}`}>
                  <th role="rowheader" scope="row" data-label="Время и подключение"><span>{time(entry.checkedAt)}</span><span>{modeName(entry.mode)}</span><span>{entry.method === "browser-http-v2" ? "До начала ответа HTTP" : "Полный HTTP-запрос"}</span></th>
                  <HistoryCell label="Получение" value={measured(entry.internet.downloadMbps, "Мбит/с")} />
                  <HistoryCell label="Отправка" value={measured(entry.internet.uploadMbps, "Мбит/с")} />
                  <HistoryCell label={entry.method === "browser-http-v2" ? "До начала ответа HTTP, интернет" : "Задержка интернета"} value={measured(entry.internet.latencyMs, "мс")} />
                  <HistoryCell label={entry.method === "browser-http-v2" ? "До начала ответа HTTP, панель" : "Задержка панели"} value={measured(entry.panel.latencyMs, "мс")} />
                  <HistoryCell label="Ошибки HTTP в интернете" value={entry.internet.errorCount} />
                </tr>
              ))}</tbody>
            </table>
          </div>
        ) : <p className="card-body hint">Пока нет сохранённых замеров.</p>}
      </section>
    </>
  );
}
