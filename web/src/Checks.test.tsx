import { describe, expect, it } from "vitest";
import { browserVerdicts, isPrivateAddress, type BrowserFacts } from "./Checks";

const server = "203.0.113.7";
const now = new Date("2026-07-01T12:00:00Z");
const connected: BrowserFacts = {
  ipv4: server,
  ipv6: null,
  geo: { ip: server, city: "Amsterdam", country: "NL", org: "AS14061 DigitalOcean, LLC", timezone: "Europe/Amsterdam" },
  webrtc: { public: [server], local: [] },
  timeZone: "Europe/Berlin",
};
const statuses = (facts: BrowserFacts, serverIP: string | null = server) =>
  Object.fromEntries(browserVerdicts(facts, serverIP, now).map((check) => [check.id, check.status]));

describe("connection checks in the browser", () => {
  it("passes a browser that goes out through the gateway", () => {
    expect(statuses(connected)).toEqual({
      ip: "ok",
      ipv6: "ok",
      webrtc: "ok",
      location: "ok",
      // Berlin and Amsterdam keep the same time.
      timezone: "ok",
      dns: "info",
    });
    expect(browserVerdicts(connected, server, now)[3].detail).toContain("Нидерланды, Amsterdam · AS14061");
  });

  it("finds a browser outside the tunnel and an IPv6 address around it", () => {
    const home = "198.51.100.20";
    const results = browserVerdicts(
      { ...connected, ipv4: home, ipv6: "2001:db8::20", geo: { ...connected.geo, ip: home } },
      server,
      now,
    );
    expect(results.find((check) => check.id === "ip")).toMatchObject({ status: "fail" });
    expect(results.find((check) => check.id === "ip")?.detail).toContain(`а IP сервера — ${server}`);
    expect(results.find((check) => check.id === "ipv6")).toMatchObject({ status: "fail" });
    expect(results.find((check) => check.id === "location")).toMatchObject({ status: "info" });
  });

  it("tells a WebRTC leak from a local address and from WebRTC that is off", () => {
    expect(statuses({ ...connected, webrtc: { public: [server, "198.51.100.20"], local: [] } }).webrtc).toBe("fail");
    // A public address of a network interface is as visible as one from STUN.
    expect(statuses({ ...connected, webrtc: { public: [], local: ["2001:db8::5"] } }).webrtc).toBe("fail");
    expect(statuses({ ...connected, webrtc: { public: [server], local: ["192.168.1.5"] } }).webrtc).toBe("warn");
    expect(statuses({ ...connected, webrtc: { public: [], local: [] } }).webrtc).toBe("ok");
    expect(statuses({ ...connected, webrtc: null }).webrtc).toBe("ok");
  });

  it("notes a time zone that gives the VPN away and an unknown server address", () => {
    expect(statuses({ ...connected, timeZone: "Europe/Moscow" }).timezone).toBe("warn");
    expect(statuses({ ...connected, geo: null })).toMatchObject({ location: "skip", timezone: "info" });
    expect(statuses({ ...connected, ipv4: null }).ip).toBe("warn");
    expect(statuses(connected, null).ip).toBe("info");
  });

  it("recognizes addresses of a local network", () => {
    for (const address of ["10.0.0.2", "172.19.0.1", "192.168.1.5", "100.64.0.1", "169.254.1.1", "fd00::1", "fe80::1"])
      expect(isPrivateAddress(address)).toBe(true);
    for (const address of ["203.0.113.7", "172.32.0.1", "2001:db8::5"]) expect(isPrivateAddress(address)).toBe(false);
  });
});
