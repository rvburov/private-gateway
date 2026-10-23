import { formatIPLookupText } from "./ipLookupText";

type Row = Record<string, unknown>;

const missing = "Недоступно";
const numbers = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 3 });
const dates = new Intl.DateTimeFormat("ru-RU", { dateStyle: "medium", timeStyle: "medium", timeZone: "UTC" });
const statuses: Record<string, string> = {
  ok: "В порядке", warn: "Внимание", fail: "Проблема", skip: "Не проверено", info: "Сведения",
  unavailable: "Недоступно", stale: "Устаревшие данные", invalid: "Некорректные данные",
};
const modes: Record<string, string> = {
  current: "Текущее подключение", "wifi-direct": "Wi-Fi без VPN", "wifi-vpn": "Wi-Fi с VPN",
  "mobile-direct": "Мобильная сеть без VPN", "mobile-vpn": "Мобильная сеть с VPN",
};

function row(value: unknown): Row {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Row : {};
}

function text(value: unknown): string {
  return typeof value === "string" && value.trim() ? value : missing;
}

function numeric(value: unknown, unit = ""): string {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? `${numbers.format(value)}${unit ? ` ${unit}` : ""}` : missing;
}

function bytes(value: unknown): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return missing;
  const unit = value >= 1024 ** 3 ? 3 : value >= 1024 ** 2 ? 2 : value >= 1024 ? 1 : 0;
  return unit ? `${numeric(value / 1024 ** unit)} ${["байт", "КиБ", "МиБ", "ГиБ"][unit]} (${numeric(value)} байт)` : `${numeric(value)} байт`;
}

function timestamp(value: unknown): string {
  if (typeof value !== "string" && typeof value !== "number") return missing;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? `${dates.format(date)} UTC` : missing;
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function translated(value: unknown, labels: Record<string, string>): string {
  return typeof value === "string" && Object.hasOwn(labels, value) ? labels[value] : missing;
}

/** Читаемый отчёт: только известные поля выбранного раздела, без обхода произвольных объектов. */
export function formatDiagnosticText(kind: "privacy" | "server", report: unknown): string {
  const source = row(report);
  const lines: string[] = [];
  const add = (label: string, value: string) => lines.push(`${label}: ${value}`);
  const section = (title: string) => lines.push("", title, "—".repeat(title.length));
  const error = (value: unknown) => { if (typeof value === "string" && value.trim()) add("Замечание", value); };
  lines.push(kind === "privacy" ? "Отчёт о безопасности подключения" : "Отчёт о работе сервера");
  add("Версия формата", numeric(source.version));
  add("Дата проверки", timestamp(source.checkedAt));
  add("Дата скачивания отчёта", timestamp(source.exportedAt));
  add("Адрес панели", text(source.panelOrigin));
  if (source.mainCheckPerformed === false) {
    lines.push("", kind === "privacy" ? "Основная проверка безопасности не запускалась." : "Основная проверка сервера не запускалась.");
    section("Ручная проверка другого IP");
    lines.push("Введённый адрес; не результат проверки текущего подключения.");
    lines.push(formatIPLookupText(source.manualIPLookup));
    return lines.join("\n").trimEnd() + "\n";
  }

  function checks(value: unknown) {
    const items = list(value);
    if (!items.length) { lines.push("Результаты проверок недоступны."); return; }
    for (const item of items) {
      const check = row(item);
      lines.push(`${text(check.title)} — ${translated(check.status, statuses)}`);
      lines.push(text(check.detail));
      if (typeof check.ip === "string" && check.ip) add("IP, который видят сайты", check.ip);
      lines.push("");
    }
  }

  function network(label: string, value: unknown) {
    const entry = row(value);
    add(label, text(entry.ip));
    if (!Object.keys(entry).length) return;
    add("  Сеть (ASN)", numeric(entry.asn));
    add("  Провайдер", text(entry.isp));
    add("  Код страны", text(entry.countryCode));
    add("  Город", text(entry.city));
    add("  Часовой пояс", text(entry.timeZone));
  }

  function resources(label: string, value: unknown) {
    const group = row(value);
    lines.push("", label);
    if (typeof group.scope === "string") add("Область показателей", text(group.scope));
    add("Память", bytes(group.memoryBytes));
    add("Лимит памяти", group.memoryLimitBytes === null ? "Не задан или недоступен" : bytes(group.memoryLimitBytes));
    add("Лимит процессора", group.cpuQuotaCores === null ? "Не задан или недоступен" : numeric(group.cpuQuotaCores, "ядра"));
    add("Накопленное процессорное время", numeric(group.cpuUsageUsec, "мкс"));
    add("Накопленное время ограничения процессора", numeric(group.cpuThrottledUsec, "мкс"));
    add("Число периодов ограничения процессора", numeric(group.cpuThrottledPeriods));
  }

  function snapshot(label: string, value: unknown) {
    section(label);
    const sample = row(value);
    if (!Object.keys(sample).length) { lines.push("Снимок недоступен."); return; }
    add("Время снимка", timestamp(sample.sampledAt));
    const runtime = row(sample.runtime);
    add("Операционная система сервера", text(runtime.os));
    add("Архитектура процессора", text(runtime.arch));
    add("Версия Go", text(runtime.goVersion));
    add("Доступные логические процессоры", numeric(runtime.cpuCount));
    add("Задачи Go (горутины)", numeric(runtime.goroutines));
    add("Версия приложения", text(runtime.version));
    add("Ревизия приложения", text(runtime.revision));
    const system = row(sample.system);
    add("Область системных показателей", text(system.scope));
    add("Время работы системы", numeric(system.uptimeSeconds, "с"));
    const loads = list(system.loadAverage);
    add("Средняя нагрузка за 1 / 5 / 15 минут", loads.length === 3 ? loads.map((item) => numeric(item)).join(" / ") : missing);
    add("Всего памяти системы", bytes(system.memoryTotalBytes));
    add("Доступно памяти системы", bytes(system.memoryAvailableBytes));
    const cpu = row(system.cpu);
    add("Накопленные такты процессора системы", numeric(cpu.totalTicks));
    add("Накопленные такты ожидания процессора виртуальной машиной", numeric(cpu.stealTicks));
    resources("Контейнер API", sample.api);
    const vpn = row(sample.vpn);
    lines.push("", "Состояние VPN");
    add("Состояние снимка VPN", translated(vpn.status, statuses));
    add("Пояснение", text(vpn.detail));
    add("Область показателей VPN", text(vpn.scope));
    add("Время снимка VPN", timestamp(vpn.sampledAt));
    add("Процесс Xray", typeof vpn.running === "boolean" ? vpn.running ? "Работает" : "Остановлен" : missing);
    resources("Ресурсы контейнера VPN", vpn.cgroup);
    const traffic = row(vpn.network);
    add("Всего получено контейнером VPN", bytes(traffic.rxBytes));
    add("Всего отправлено контейнером VPN", bytes(traffic.txBytes));
    const tcp = row(vpn.tcp);
    add("Накопленные исходящие сегменты TCP", numeric(tcp.outSegments));
    add("Накопленные повторные отправки TCP", numeric(tcp.retransmittedSegments));
  }

  function path(label: string, value: unknown) {
    section(label);
    const measurement = row(value);
    const latency = row(measurement.latency);
    add("Задержка HTTP, медиана", numeric(latency.medianMs, "мс"));
    add("Задержка HTTP, 95-й процентиль", numeric(latency.p95Ms, "мс"));
    add("Колебание задержки", numeric(latency.jitterMs, "мс"));
    add("Всего HTTP-проб", numeric(latency.attempts));
    add("Успешных HTTP-проб", numeric(latency.successes));
    add("Ошибок HTTP-проб", numeric(latency.errorCount));
    error(latency.error);
    for (const [key, title] of [["download", "Получение"], ["upload", "Отправка"]]) {
      const transfer = row(measurement[key]);
      add(`${title}, скорость`, numeric(transfer.mbps, "Мбит/с"));
      add(`${title}, объём данных`, bytes(transfer.bytes));
      add(`${title}, длительность`, numeric(transfer.durationMs, "мс"));
      error(transfer.error);
    }
  }

  if (kind === "privacy") {
    section("Что видят сайты");
    checks(source.browser);
    section("Адреса и DNS, сообщённые браузером");
    const facts = row(source.browserFacts);
    network("Внешний IPv4", facts.ipv4);
    network("Внешний IPv6", facts.ipv6);
    add("Часовой пояс браузера", text(facts.timeZone));
    const rtc = row(facts.webrtc);
    if (!Object.keys(rtc).length) add("WebRTC", "Выключен или сведения недоступны");
    else {
      const external = list(rtc.public);
      if (!external.length) add("Внешние адреса WebRTC", "Не обнаружены");
      external.forEach((item, index) => network(`Внешний адрес WebRTC ${index + 1}`, item));
      const local = list(rtc.local).filter((item): item is string => typeof item === "string");
      add("Локальные адреса WebRTC", local.length ? local.join(", ") : "Не обнаружены");
    }
    const dns = list(facts.dns);
    if (!dns.length) add("DNS-серверы", facts.dns === null ? missing : "Не обнаружены");
    dns.forEach((item, index) => network(`DNS-сервер ${index + 1}`, item));
    section("Сведения об устройстве и браузере");
    const device = row(source.device);
    add("Время сбора сведений", timestamp(device.collectedAt));
    for (const item of list(device.facts)) {
      const fact = row(item);
      add(text(fact.label), text(fact.value));
    }
    for (const note of list(device.notes)) if (typeof note === "string") lines.push("", note);
    section("IP и репутация текущего выхода браузера");
    const lookups = list(source.ipLookups);
    if (!lookups.length) lines.push("Сведения об измеренном IP не запрашивались или адрес не определён.");
    for (const lookup of lookups) lines.push(formatIPLookupText(lookup));
  } else {
    section("Проверки работы сервера");
    checks(source.server);
    error(source.serverError);
    section("IP выхода, измеренный серверной проверкой VLESS");
    lines.push(source.serverIPLookup ? formatIPLookupText(source.serverIPLookup) : "Подтверждённый IP выхода недоступен; сведения об адресе не запрашивались.");
    const performance = row(source.performance);
    section("Замеры скорости и задержки");
    if (!Object.keys(performance).length) lines.push("Замеры недоступны.");
    else {
      add("Время замера", timestamp(performance.checkedAt));
      add("Подключение, выбранное пользователем", translated(performance.mode, modes));
      path("Канал до панели сервера", performance.panel);
      path("Интернет через текущее подключение браузера", performance.internet);
      error(performance.serverError);
      snapshot("Показатели сервера до замера", performance.serverBefore);
      snapshot("Показатели сервера после замера", performance.serverAfter);
      lines.push("", "Показатели системы и контейнеров имеют разную область наблюдения. Счётчики накопительные и относятся ко всем соединениям контейнера; повторные отправки TCP не равны потерям пакетов этого устройства.");
    }
    error(source.performanceError);
    lines.push("", "Канал до панели может обходить VPN по исключению клиента. Замер до Cloudflare использует текущее подключение браузера; ближайший узел сервиса может меняться. HTTP-пробы не являются ICMP ping, а один запуск не доказывает замедление VPN оператором.");
  }
  if (source.manualIPLookup) {
    section("Ручная проверка другого IP");
    lines.push("Введённый адрес; не результат проверки текущего подключения.");
    lines.push(formatIPLookupText(source.manualIPLookup));
  }
  return lines.join("\n").trimEnd() + "\n";
}
