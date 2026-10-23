import { describe, expect, it } from "vitest";
import { formatDiagnosticText } from "./diagnosticText";

const snapshot = {
  sampledAt: "2026-10-23T05:11:13Z",
  runtime: { os: "linux", arch: "amd64", goVersion: "go1.26.9", cpuCount: 1, goroutines: 12, version: "1.0", revision: "test-revision" },
  system: {
    scope: "Видимая виртуальная машина, а не физический хост.", uptimeSeconds: 0,
    loadAverage: [0, 0.1, 0.2], memoryTotalBytes: 4096, memoryAvailableBytes: 2048,
    cpu: { totalTicks: 123, stealTicks: 0 },
  },
  api: { scope: "Группа API.", memoryBytes: 0, memoryLimitBytes: null, cpuQuotaCores: null, cpuUsageUsec: 100, cpuThrottledUsec: 0, cpuThrottledPeriods: 0 },
  vpn: {
    status: "ok", detail: "Свежий снимок VPN.", scope: "Контейнер Xray, все его соединения.",
    sampledAt: "2026-10-23T05:11:13Z", running: true,
    cgroup: { memoryBytes: 0, memoryLimitBytes: null, cpuQuotaCores: 1, cpuUsageUsec: 0, cpuThrottledUsec: 0, cpuThrottledPeriods: 0 },
    network: { rxBytes: 0, txBytes: 2 }, tcp: { outSegments: 20, retransmittedSegments: 0 },
  },
};

const path = {
  latency: { medianMs: 162, p95Ms: 211, jitterMs: 25.714, attempts: 8, successes: 8, errorCount: 0, error: null },
  download: { mbps: null, bytes: 1556480, durationMs: null, error: "Ответ не получен за 10 секунд." },
  upload: { mbps: 1.329836, bytes: 262144, durationMs: 1577, error: null },
};

function serverReport() {
  return {
    format: "private-gateway-server-diagnostics", version: 1,
    exportedAt: "2026-10-23T05:12:24Z", checkedAt: "2026-10-23T05:11:50Z", panelOrigin: "https://gateway.example:8443",
    server: [{ id: "database", title: "База данных", status: "ok", detail: "PostgreSQL отвечает.", ip: "203.0.113.7" }],
    serverError: null, performanceError: null,
    performance: {
      checkedAt: Date.parse("2026-10-23T05:11:50Z"), mode: "mobile-vpn", panel: path, internet: path,
      serverBefore: snapshot, serverAfter: snapshot, serverError: null,
    },
  };
}

describe("readable Russian diagnostic report", () => {
  it("keeps browser, measured server and manual IP lookups in their respective report scopes", () => {
    const lookup = (ip: string) => ({
      ip, source: "IP2Location.io", status: "partial", checkedAt: null,
      detail: "Часть сведений недоступна.",
      data: { ip, isp: "Проверочная сеть", fraud_score: 0, proxy: { is_vpn: false, is_data_center: true },
        token: "must-not-export-ip-token", country: { translation: { lang: "ru", value: "Страна" } } },
    });
    const source = {
      ...serverReport(), ipLookups: [lookup("8.8.8.8")], serverIPLookup: lookup("1.1.1.1"), manualIPLookup: lookup("9.9.9.9"),
    };
    const privacy = formatDiagnosticText("privacy", source);
    const server = formatDiagnosticText("server", source);
    expect(privacy).toContain("IP-адрес: 8.8.8.8");
    expect(privacy).not.toContain("IP-адрес: 1.1.1.1");
    expect(server).toContain("IP-адрес: 1.1.1.1");
    expect(server).not.toContain("IP-адрес: 8.8.8.8");
    for (const report of [privacy, server]) {
      expect(report).toContain("Ручная проверка другого IP");
      expect(report).toContain("IP-адрес: 9.9.9.9");
      expect(report).toContain("Оценка риска IP2Location: 0");
      expect(report).toContain("VPN: Не отмечен в базе");
      expect(report).toContain("Переведённое название: Страна");
      expect(report).not.toContain("must-not-export-ip-token");
    }
  });

  it("explains the server measurements in Russian and keeps unknown values distinct from zero", () => {
    const result = formatDiagnosticText("server", serverReport());
    expect(result).toContain("Отчёт о работе сервера");
    expect(result).toContain("База данных — В порядке\nPostgreSQL отвечает.");
    expect(result).toContain("Подключение, выбранное пользователем: Мобильная сеть с VPN");
    expect(result).toContain("Получение, скорость: Недоступно");
    expect(result).toContain("Отправка, скорость: 1,33 Мбит/с");
    expect(result).toContain("Задержка HTTP, медиана: 162 мс");
    expect(result).toContain("Ошибок HTTP-проб: 0");
    expect(result).toContain("Замечание: Ответ не получен за 10 секунд.");
    expect(result).toContain("Память: 0 байт");
    expect(result).toContain("Лимит памяти: Не задан или недоступен");
    expect(result).toContain("Время работы системы: 0 с");
    expect(result).toContain("Область системных показателей: Видимая виртуальная машина, а не физический хост.");
    expect(result).toContain("Область показателей VPN: Контейнер Xray, все его соединения.");
    expect(result).toContain("Накопленные повторные отправки TCP: 0");
    expect(result).toContain("повторные отправки TCP не равны потерям пакетов этого устройства");
    expect(result).toContain("Показатели сервера до замера");
    expect(result).toContain("Показатели сервера после замера");
    expect(result).toContain("05:12:24 UTC");
    expect(result.charCodeAt(0)).not.toBe(0xfeff);
    expect(result).not.toContain("Р‘Р°Р·Р°");
    expect(result).not.toMatch(/medianMs|memoryBytes|cpuUsageUsec|serverBefore|mobile-vpn/);
  });

  it("includes browser addresses, DNS and device explanations only in the privacy report", () => {
    const network = { ip: "203.0.113.7", asn: 8359, isp: "Проверочный провайдер", countryCode: "RU", city: "Санкт-Петербург", timeZone: "Europe/Moscow" };
    const source = {
      version: 1, exportedAt: "2026-10-23T05:12:24Z", checkedAt: "2026-10-23T05:11:50Z", panelOrigin: "https://gateway.example:8443",
      browser: [{ title: "DNS", status: "warn", detail: "Проверьте маршрут DNS." }],
      browserFacts: { ipv4: network, ipv6: null, timeZone: "Europe/Moscow", webrtc: { public: [network], local: ["192.168.1.2"] }, dns: [network] },
      device: { collectedAt: Date.parse("2026-10-23T05:11:50Z"), facts: [{ id: "cpu", label: "Логические ядра", value: "4", token: "nested-device-secret" }], notes: ["VPN не скрывает отпечаток браузера."] },
      server: [{ title: "server-only-secret", detail: "private-server-scope" }],
      performance: { serverBefore: { api: { memoryBytes: "private-server-memory" } } },
      token: "session-secret", profile: "profile-secret",
    };
    const result = formatDiagnosticText("privacy", source);
    expect(result).toContain("Отчёт о безопасности подключения");
    expect(result).toContain("DNS — Внимание");
    expect(result).toContain("Внешний IPv4: 203.0.113.7");
    expect(result).toContain("Внешний IPv6: Недоступно");
    expect(result).toContain("DNS-сервер 1: 203.0.113.7");
    expect(result).toContain("Внешний адрес WebRTC 1: 203.0.113.7");
    expect(result).toContain("Локальные адреса WebRTC: 192.168.1.2");
    expect(result).toContain("Город: Санкт-Петербург");
    expect(result).toContain("Логические ядра: 4");
    expect(result).toContain("VPN не скрывает отпечаток браузера.");
    expect(result).not.toMatch(/server-only-secret|private-server|session-secret|profile-secret|nested-device-secret/);
  });

  it("does not inspect arbitrary fields or include device information in the server report", () => {
    const source = {
      ...serverReport(),
      browser: [{ title: "private-browser-data" }],
      browserFacts: { ipv4: { ip: "private-client-ip" } },
      device: { facts: [{ label: "private-device-label", value: "private-device-value" }] },
    };
    Object.defineProperty(source, "token", { enumerable: true, get() { throw new Error("must not inspect credentials"); } });
    Object.defineProperty(source.performance.serverBefore.runtime, "secret", { enumerable: true, get() { throw new Error("must not inspect unknown runtime fields"); } });
    const result = formatDiagnosticText("server", source);
    expect(result).toContain("Версия Go: go1.26.9");
    expect(result).not.toMatch(/private-browser|private-client|private-device|must not inspect/);
  });

  it("describes unavailable snapshots and failed checks without making up healthy or zero metrics", () => {
    const result = formatDiagnosticText("server", {
      version: 1, checkedAt: "invalid", exportedAt: null,
      server: [{ title: "Процесс Xray", status: "fail", detail: "Xray не работает." }],
      serverError: "Проверка не завершена.", performanceError: "Служба измерений недоступна.",
      performance: { mode: "unknown", panel: null, internet: null, serverBefore: null, serverAfter: { vpn: { status: "stale", running: null } } },
    });
    expect(result).toContain("Дата проверки: Недоступно");
    expect(result).toContain("Процесс Xray — Проблема");
    expect(result).toContain("Замечание: Проверка не завершена.");
    expect(result).toContain("Снимок недоступен.");
    expect(result).toContain("Состояние снимка VPN: Устаревшие данные");
    expect(result).toContain("Процесс Xray: Недоступно");
    expect(result).toContain("Замечание: Служба измерений недоступна.");
    expect(result).not.toContain("скорость: 0");
  });
});
