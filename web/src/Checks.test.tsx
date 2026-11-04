import { describe, expect, it } from "vitest";
import { browserVerdicts, dnsGroups, isPrivateAddress, privacyDiagnosticReport, type BrowserFacts, type Network } from "./Checks";

const now = new Date("2026-07-01T12:00:00Z");
const gateway = "203.0.113.7";
const net = (ip: string, asn: number | null, isp: string | null, countryCode: string | null, timeZone: string | null = null): Network => ({
  ip,
  asn,
  isp,
  countryCode,
  city: null,
  timeZone,
});
const hoster = net(gateway, 14061, "DigitalOcean, LLC", "NL", "Europe/Amsterdam");
const cloudflareDNS = net("162.158.1.10", 13335, "Cloudflare, Inc.", null);
const home4 = net("198.51.100.20", 8359, "MTS PJSC", "RU", "Europe/Moscow");
const home6 = net("2001:db8::20", 8359, "MTS PJSC", "RU", "Europe/Moscow");
const otherVPN4 = net("192.0.2.44", 9009, "M247 Europe SRL", "DE", "Europe/Berlin");
const otherVPN6 = net("2001:db8:1::44", 9009, "M247 Europe SRL", "DE", "Europe/Berlin");
const viaGateway: BrowserFacts = {
  ipv4: hoster,
  ipv6: null,
  webrtc: { public: [hoster], local: [] },
  dns: [cloudflareDNS],
  // Берлин и Амстердам живут по одному времени.
  timeZone: "Europe/Berlin",
};
const statuses = (facts: BrowserFacts, serverIP: string | null = gateway) =>
  Object.fromEntries(browserVerdicts(facts, serverIP, now).map((check) => [check.id, check.status]));
const detail = (facts: BrowserFacts, id: string) =>
  browserVerdicts(facts, gateway, now).find((check) => check.id === id)?.detail ?? "";

describe("connection checks in the browser", () => {
  it("passes a browser that goes out through the gateway", () => {
    expect(statuses(viaGateway)).toEqual({ exit: "ok", ipv6: "ok", webrtc: "ok", dns: "info", timezone: "ok" });
    expect(detail(viaGateway, "exit")).toContain("ваш шлюз (DigitalOcean, LLC, Нидерланды)");
    expect(detail(viaGateway, "dns")).toContain("Cloudflare, Inc. — обнаружено 1 адрес");
    expect(detail(viaGateway, "dns")).toContain("Маршрут DNS-запросов этой проверкой не подтверждён");
  });

  it("does not call another VPN a leak when all its addresses are in one network", () => {
    const otherVPN: BrowserFacts = {
      ipv4: otherVPN4,
      ipv6: otherVPN6,
      webrtc: { public: [otherVPN4, otherVPN6], local: [] },
      dns: [net("192.0.2.53", 9009, "M247 Europe SRL", "DE")],
      timeZone: "Europe/Berlin",
    };
    expect(statuses(otherVPN)).toEqual({ exit: "info", ipv6: "ok", webrtc: "ok", dns: "info", timezone: "ok" });
    expect(detail(otherVPN, "exit")).toContain("Это не IP сервера");
  });

  it("names the provider an IPv6 or WebRTC leak reveals without judging DNS by ASN", () => {
    const leaking: BrowserFacts = {
      ipv4: otherVPN4,
      ipv6: home6,
      webrtc: { public: [otherVPN4, home4], local: [] },
      dns: [net("192.0.2.54", 8359, "MTS PJSC", "RU"), cloudflareDNS],
      timeZone: "Europe/Moscow",
    };
    expect(statuses(leaking)).toEqual({ exit: "info", ipv6: "fail", webrtc: "fail", dns: "info", timezone: "warn" });
    expect(detail(leaking, "ipv6")).toContain("MTS PJSC, Россия, а IPv4 идёт через M247 Europe SRL, Германия");
    expect(detail(leaking, "webrtc")).toContain("198.51.100.20 (MTS PJSC, Россия)");
    expect(detail(leaking, "dns")).toContain("MTS PJSC — обнаружено 1 адрес");
    expect(detail(leaking, "dns")).toContain("Cloudflare, Inc. — обнаружено 1 адрес");
  });

  it("groups all resolver addresses by operator even when ASNs differ", () => {
    const dns = [
      net("192.0.2.1", 13335, "Cloudflare, Inc.", "US"),
      net("192.0.2.2", 64500, "Cloudflare, Inc.", "NL"),
      net("192.0.2.3", 64501, "Cloudflare, Inc.", null),
    ];
    const facts = { ...viaGateway, dns };
    expect(statuses(facts).dns).toBe("info");
    expect(detail(facts, "dns")).toContain("Cloudflare, Inc. — обнаружено 3 адреса");
    expect(dnsGroups(dns)[0].addresses).toEqual(["192.0.2.1", "192.0.2.2", "192.0.2.3"]);
  });

  it("does not call a selected proxy IP a VPN failure", () => {
    const proxy = net("155.103.141.121", 64502, "Proxy provider", "US");
    const facts = { ...viaGateway, ipv4: proxy };
    expect(statuses(facts).exit).toBe("info");
    expect(detail(facts, "exit")).toContain("Если выбран профиль «IP прокси»");
    expect(detail(facts, "exit")).not.toContain("VPN на этом устройстве не работает");
  });

  it("keeps unknown operators separate and never treats WoodyNet or HostPapa names as proof", () => {
    const dns = [
      net("192.0.2.1", 42, "WoodyNet", null),
      net("192.0.2.2", 43, "HostPapa", null),
      net("192.0.2.3", null, null, null),
      net("192.0.2.4", null, "Unknown", null),
    ];
    const facts = { ...viaGateway, dns };
    expect(statuses(facts).dns).toBe("info");
    expect(detail(facts, "dns")).toContain("WoodyNet — обнаружено 1 адрес");
    expect(detail(facts, "dns")).toContain("HostPapa — обнаружено 1 адрес");
    expect(detail(facts, "dns")).toContain("Оператор неизвестен — обнаружено 2 адреса");
    expect(dnsGroups(dns).at(-1)?.addresses).toEqual(["192.0.2.3", "192.0.2.4"]);
  });

  it("marks a partial DNS probe and missing network metadata as incomplete information", () => {
    const facts = { ...viaGateway, dns: [cloudflareDNS, net("192.0.2.5", null, null, null)],
      dnsProgress: { answered: 6, total: 10 } };
    expect(statuses(facts).dns).toBe("warn");
    expect(detail(facts, "dns")).toContain("Проверка DNS не завершена: ответили 6 из 10 проб");
    expect(detail(facts, "dns")).toContain("Оператор неизвестен — обнаружено 1 адрес");
    expect(detail(facts, "dns")).not.toMatch(/утечки нет|ваш провайдер не видит/);
  });

  it("marks unavailable operator metadata without losing the observed resolver", () => {
    const unknown = { ...net("192.0.2.5", null, null, null), metadataUnavailable: true };
    const facts = { ...viaGateway, dns: [cloudflareDNS, unknown], dnsProgress: { answered: 10, total: 10 } };
    expect(statuses(facts).dns).toBe("warn");
    expect(detail(facts, "dns")).toContain("Оператор неизвестен — обнаружено 1 адрес");
    expect(detail(facts, "dns")).toContain("Сведения об операторе недоступны для 1 адреса");
    expect(dnsGroups(facts.dns)[1].addresses).toEqual(["192.0.2.5"]);
  });

  it("reports an address sites already see once, not again under WebRTC", () => {
    const facts: BrowserFacts = { ...viaGateway, ipv6: home6, webrtc: { public: [hoster, home6], local: [] } };
    expect(statuses(facts)).toMatchObject({ ipv6: "fail", webrtc: "ok" });
  });

  it("tells unknown, local and missing answers apart", () => {
    expect(statuses({ ...viaGateway, webrtc: { public: [], local: ["192.168.1.5"] } }).webrtc).toBe("warn");
    expect(statuses({ ...viaGateway, webrtc: null }).webrtc).toBe("ok");
    expect(statuses({ ...viaGateway, ipv6: net("2001:db8::9", null, null, null) }).ipv6).toBe("warn");
    expect(statuses({ ...viaGateway, dns: null }).dns).toBe("skip");
    expect(statuses({ ...viaGateway, dns: [] }).dns).toBe("skip");
    expect(detail({ ...viaGateway, dns: null }, "dns")).toContain("Проверка DNS не завершена");
    expect(detail({ ...viaGateway, dns: [] }, "dns")).toContain("Недостаточно данных");
    expect(detail({ ...viaGateway, dns: [], dnsProgress: { answered: 8, total: 10 } }, "dns"))
      .toContain("Проверка DNS не завершена: ответили 8 из 10 проб");
    // Без ответа ipleak.net сравнивать не с чем: это не утечка.
    expect(statuses({ ...viaGateway, ipv4: null, ipv6: null })).toMatchObject({ exit: "warn", webrtc: "info" });
    expect(statuses(viaGateway, null).exit).toBe("info");
    expect(statuses({ ...viaGateway, timeZone: "Europe/Moscow" }).timezone).toBe("warn");
  });

  it("recognizes addresses of a local network", () => {
    for (const address of ["10.0.0.2", "172.19.0.1", "192.168.1.5", "100.64.0.1", "169.254.1.1", "fd00::1", "fe80::1"])
      expect(isPrivateAddress(address)).toBe(true);
    for (const address of ["203.0.113.7", "172.32.0.1", "2001:db8::5"]) expect(isPrivateAddress(address)).toBe(false);
  });
  it("exports only privacy diagnostics even if the caller has server and credential fields", () => {
    const run = {
      checkedAt: now.getTime(), browser: browserVerdicts(viaGateway, gateway, now),
      browserFacts: viaGateway,
      device: { collectedAt: now.getTime(), facts: [{ id: "ua", label: "User-Agent", value: "Test browser" }], notes: [] },
      server: [{ detail: "server-diagnostics-only" }], serverError: "private-server-error",
      performance: { upload: 500, memoryBytes: 987654 }, performanceError: "private-speed-error",
      token: "session-secret", profile: "vless://profile-secret",
    };
    const report = privacyDiagnosticReport(run, "https://panel.example:8443");
    expect(report.format).toBe("private-gateway-privacy-diagnostics");
    expect(report.browserFacts.ipv4?.ip).toBe(gateway);
    expect(report.device.facts[0].value).toBe("Test browser");
    expect(JSON.stringify(report)).not.toMatch(/server-diagnostics-only|private-server-error|private-speed-error|memoryBytes|987654|session-secret|profile-secret/);
    expect(Object.keys(report)).not.toContain("performance");
    expect(Object.keys(report)).not.toContain("server");
  });

});
