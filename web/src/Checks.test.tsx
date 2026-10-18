import { describe, expect, it } from "vitest";
import { browserVerdicts, isPrivateAddress, type BrowserFacts, type Network } from "./Checks";

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
    expect(statuses(viaGateway)).toEqual({ exit: "ok", ipv6: "ok", webrtc: "ok", dns: "ok", timezone: "ok" });
    expect(detail(viaGateway, "exit")).toContain("ваш шлюз (DigitalOcean, LLC, Нидерланды)");
    expect(detail(viaGateway, "dns")).toContain("Cloudflare, Inc. — публичный DNS");
  });

  it("does not call another VPN a leak when all its addresses are in one network", () => {
    const otherVPN: BrowserFacts = {
      ipv4: otherVPN4,
      ipv6: otherVPN6,
      webrtc: { public: [otherVPN4, otherVPN6], local: [] },
      dns: [net("192.0.2.53", 9009, "M247 Europe SRL", "DE")],
      timeZone: "Europe/Berlin",
    };
    expect(statuses(otherVPN)).toEqual({ exit: "warn", ipv6: "ok", webrtc: "ok", dns: "ok", timezone: "ok" });
    expect(detail(otherVPN, "exit")).toContain("Это не ваш шлюз");
  });

  it("names the provider an IPv6 address, WebRTC or DNS leaks to", () => {
    const leaking: BrowserFacts = {
      ipv4: otherVPN4,
      ipv6: home6,
      webrtc: { public: [otherVPN4, home4], local: [] },
      dns: [net("192.0.2.54", 8359, "MTS PJSC", "RU"), cloudflareDNS],
      timeZone: "Europe/Moscow",
    };
    expect(statuses(leaking)).toEqual({ exit: "warn", ipv6: "fail", webrtc: "fail", dns: "warn", timezone: "warn" });
    expect(detail(leaking, "ipv6")).toContain("MTS PJSC, Россия, а IPv4 идёт через M247 Europe SRL, Германия");
    expect(detail(leaking, "webrtc")).toContain("198.51.100.20 (MTS PJSC, Россия)");
    expect(detail(leaking, "dns")).toContain("MTS PJSC, Россия");
    expect(detail(leaking, "dns")).not.toContain("Cloudflare");
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
});
