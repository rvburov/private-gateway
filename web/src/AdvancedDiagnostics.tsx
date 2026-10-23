import { useEffect, useState } from "react";
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
  typeof value === "number" && Number.isFinite(value) ? `${number.format(value)} ${unit}` : "Недоступно";
const size = (value: number | null | undefined) =>
  typeof value === "number" && Number.isFinite(value) ? bytes(value) : "Недоступно";
const modeName = (mode: string) => connectionModes.find((item) => item.value === mode)?.label ?? "Текущее подключение";
const time = (value: number) => new Date(value).toLocaleString("ru-RU", { dateStyle: "short", timeStyle: "short" });

function Measurement({ title, path }: { title: string; path: PathMeasurement }) {
  const errors = [path.latency.error, path.download.error, path.upload.error].filter(Boolean);
  return (
    <section className="measurement" aria-label={title}>
      <h3>{title}</h3>
      <dl className="measurement-values">
        <div><dt>Получение</dt><dd>{measured(path.download.mbps, "Мбит/с")}</dd></div>
        <div><dt>Отправка</dt><dd>{measured(path.upload.mbps, "Мбит/с")}</dd></div>
        <div><dt>Задержка HTTP, медиана</dt><dd>{measured(path.latency.medianMs, "мс")}</dd></div>
        <div><dt>Задержка HTTP, p95</dt><dd>{measured(path.latency.p95Ms, "мс")}</dd></div>
        <div><dt>Колебание задержки</dt><dd>{measured(path.latency.jitterMs, "мс")}</dd></div>
        <div><dt>Пробы HTTP</dt><dd>{path.latency.successes} из {path.latency.attempts} успешны; ошибок: {path.latency.errorCount}</dd></div>
      </dl>
      {errors.length > 0 && <p className="hint">{[...new Set(errors)].join(" ")}</p>}
      <p className="hint">
        Данные замера: получено {bytes(path.download.bytes)}, отправлено {bytes(path.upload.bytes)}.
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
                <p>Задержки измеряются HTTP-запросами. Медиана — среднее по порядку значение; p95 — верхняя граница для 95% проб. Колебания задержки и ошибки HTTP помогают оценить стабильность, но не измеряют потерю IP-пакетов и не доказывают ограничение скорости оператором.</p>
              </HelpTip>
            </div>
            <p>{performance ? `${modeName(performance.mode)} · ${time(performance.checkedAt)}` : "До панели и в интернет."}</p>
          </div>
        </div>
        {performance ? (
          <>
            <div className="measurement-columns">
              <Measurement title="Канал до панели" path={performance.panel} />
              <Measurement title="Интернет через текущее подключение" path={performance.internet} />
            </div>
            <ServerResources run={performance} />
          </>
        ) : <div className="card-body"><p className="hint">{result?.performanceError || "Нажмите «Проверить сервер», чтобы начать замер."}</p></div>}
      </section>
      <section className="card" aria-label="История замеров">
        <div className="card-heading">
          <div>
            <div className="diagnostics-title-with-help">
              <h2>История замеров</h2>
              <HelpTip label="Об истории замеров">
                <p>Сравнивайте результаты в одинаковых условиях. Скорость измеряется до Cloudflare, задержка — HTTP-запросами.</p>
                <p>В этом браузере сохраняются числа, время и подпись подключения. IP-адреса, сведения об устройстве и токен входа в историю не записываются.</p>
              </HelpTip>
            </div>
            <p>Последние 12 замеров в этом браузере.</p>
          </div>
          <button className="button" disabled={busy || history.length === 0} onClick={() => { clearPerformanceHistory(); setHistory([]); }}>Очистить историю</button>
        </div>
        {history.length > 0 ? (
          <div className="diagnostics-table-scroll">
            <table className="diagnostics-table">
              <caption className="sr-only">Сравнение замеров: скорость до Cloudflare и задержка HTTP.</caption>
              <thead><tr><th scope="col">Время и подключение</th><th scope="col">Получение</th><th scope="col">Отправка</th><th scope="col">Задержка интернета</th><th scope="col">Задержка панели</th><th scope="col">Ошибки HTTP в интернете</th></tr></thead>
              <tbody>{history.map((entry, index) => (
                <tr key={`${entry.checkedAt}-${index}`}>
                  <th scope="row"><span>{time(entry.checkedAt)}</span><span>{modeName(entry.mode)}</span></th>
                  <td>{measured(entry.internet.downloadMbps, "Мбит/с")}</td>
                  <td>{measured(entry.internet.uploadMbps, "Мбит/с")}</td>
                  <td>{measured(entry.internet.latencyMs, "мс")}</td>
                  <td>{measured(entry.panel.latencyMs, "мс")}</td>
                  <td>{entry.internet.errorCount}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        ) : <p className="card-body hint">Пока нет сохранённых замеров.</p>}
      </section>
    </>
  );
}
