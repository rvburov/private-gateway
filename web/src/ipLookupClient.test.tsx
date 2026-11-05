import { afterEach, describe, expect, it, vi } from "vitest";
import { normalizePublicIP, samePublicIP, type IPLookupResult } from "./ipLookup";
import { lookupIP } from "./ipLookupClient";

afterEach(() => vi.unstubAllGlobals());

function response(ip: string, dataIP: unknown = ip): IPLookupResult {
  return {
    ip, source: "IP2Location.io", status: "available", checkedAt: "2026-11-05T12:00:00Z",
    detail: "Сведения из базы.", data: { ip: dataIP as string, isp: "Проверочная сеть" },
  };
}

describe("IP lookup response identity", () => {
  it.each([
    ["8.8.8.8", response("1.1.1.1")],
    ["8.8.8.8", response("8.8.8.8", "1.1.1.1")],
    ["8.8.8.8", response("8.8.8.8", 123)],
    ["2606:4700:4700::1111", response("2606:4700:4700::1001")],
    ["8.8.8.8", { ...response("1.1.1.1"), status: "not_configured", data: null }],
  ])("discards metadata for a different address than %s", async (ip, value) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json(value)));
    const result = await lookupIP("test-session", ip, new AbortController().signal);
    expect(result).toMatchObject({ ip, status: "unavailable", data: null, checkedAt: null });
    expect(result.detail).toContain("другом IP");
    expect(JSON.stringify(result)).not.toContain("Проверочная сеть");
  });

  it.each([
    ["8.8.8.8", response("8.8.8.8")],
    ["2606:4700:4700::1111", response("2606:4700:4700:0:0:0:0:1111", "2606:4700:4700:0000:0:0000:0:1111")],
    ["::ffff:8.8.8.8", response("8.8.8.8", "::FFFF:0808:0808")],
    ["8.8.8.8", response("8.8.8.8", null)],
    ["8.8.8.8", { ...response("8.8.8.8"), data: { isp: "Проверочная сеть" } }],
  ])("accepts equivalent address representations for %s", async (ip, value) => {
    const fetch = vi.fn().mockResolvedValue(Response.json(value));
    vi.stubGlobal("fetch", fetch);
    expect(await lookupIP("test-session", ip, new AbortController().signal)).toEqual(value);
    expect(fetch).toHaveBeenCalledWith("/api/v1/ip-lookup", expect.objectContaining({
      method: "POST", body: JSON.stringify({ ip }),
    }));
  });

  it("does not expose a late successful response after cancellation", async () => {
    const controller = new AbortController();
    vi.stubGlobal("fetch", vi.fn(async () => {
      controller.abort();
      return Response.json(response("8.8.8.8"));
    }));
    await expect(lookupIP("test-session", "8.8.8.8", controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("public IP normalization", () => {
  it.each([
    ["2606:4700:4700::ABCD", "2606:4700:4700:0:0:0000:0:abcd"],
    ["::ffff:8.8.8.8", "8.8.8.8"],
    ["::FFFF:808:808", "8.8.8.8"],
    ["2001:4860::8.8.8.8", "2001:4860:0:0:0:0:808:808"],
  ])("matches %s with %s", (a, b) => {
    expect(samePublicIP(a, b)).toBe(true);
    expect(normalizePublicIP(a)).toBe(normalizePublicIP(b));
  });

  it.each([
    ["8.8.8.8", "1.1.1.1"], ["::ffff:8.8.8.8", "::ffff:8.8.8.9"],
    ["2606:4700:4700::1111", "2606:4700:4700::1001"],
    ["", ""], ["2001:db8::1", "2001:db8::1"], ["not-an-ip", "not-an-ip"],
  ])("does not match different or unsupported addresses %s and %s", (a, b) => {
    expect(samePublicIP(a, b)).toBe(false);
  });
});
