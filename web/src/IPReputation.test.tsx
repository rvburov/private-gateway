import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { IPReputation } from "./IPReputation";
import { ipLookupFields, ipLookupValue, ipUsageName, isValidPublicIP, type IPLookupResult } from "./ipLookup";
import { formatIPLookupText } from "./ipLookupText";

afterEach(cleanup);

const fullResult: IPLookupResult = {
  ip: "8.8.8.8", source: "IP2Location.io", status: "available", checkedAt: "2026-10-23T10:00:00Z", detail: "Сведения из базы.",
  data: {
    ip: "8.8.8.8", country_code: "US", country_name: "США", region_name: "Калифорния", district: "Округ", city_name: "Маунтин-Вью",
    latitude: 0, longitude: 0, zip_code: "94043", time_zone: "-07:00", asn: "15169", as: "GOOGLE",
    as_info: { as_number: "15169", as_name: "GOOGLE", as_domain: "google.com", as_usage_type: "DCH", as_cidr: "8.8.8.0/24" },
    isp: "Google LLC", domain: "google.com", net_speed: "T1", idd_code: "1", area_code: "650", weather_station_code: "USCA0746", weather_station_name: "Mountain View",
    mcc: "310", mnc: "260", mobile_brand: "T-Mobile", elevation: 0, usage_type: "DCH", address_type: "Anycast",
    ads_category: "IAB19", ads_category_name: "Технологии", continent: { name: "Северная Америка", code: "NA", hemisphere: ["north", "west"], translation: { lang: "ru", value: "Северная Америка" } },
    country: {
      name: "United States", alpha3_code: "USA", numeric_code: 840, demonym: "Американец", flag: "https://example.test/flag.png", capital: "Вашингтон", total_area: 9833517, population: 331002651,
      currency: { code: "USD", name: "Доллар США", symbol: "$" }, language: { code: "en", name: "Английский" }, tld: ".us", translation: { lang: "ru", value: "США" },
    },
    region: { name: "Калифорния", code: "CA", translation: { lang: "ru", value: "Калифорния" } }, city: { name: "Маунтин-Вью", translation: { lang: "ru", value: "Маунтин-Вью" } },
    time_zone_info: { olson: "America/Los_Angeles", current_time: "2026-10-23T03:00:00", gmt_offset: -25200, is_dst: false, abbreviation: "PDT", dst_start_date: "2026-03-08", dst_end_date: "2026-11-01", sunrise: "07:00", sunset: "18:00" },
    geotargeting: { metro: 0 }, is_proxy: false, fraud_score: 0,
    proxy: {
      last_seen: 0, proxy_type: "DCH", threat: null, provider: null, is_vpn: false, is_tor: false, is_data_center: true,
      is_public_proxy: false, is_web_proxy: false, is_web_crawler: false, is_ai_crawler: false, is_residential_proxy: false,
      is_consumer_privacy_network: false, is_enterprise_private_network: false, is_spammer: false, is_scanner: false, is_botnet: false, is_bogon: false,
    },
  },
};

describe("IP reputation details", () => {
  it("shows database classifications without treating false, zero or a data center as a guarantee or failure", () => {
    render(<IPReputation result={fullResult} description="Текущий выход браузера." />);
    const summary = document.querySelector(".ip-reputation-summary") as HTMLElement;
    expect(within(summary).getByText("8.8.8.8")).not.toBeNull();
    expect(within(summary).getByText("США, Маунтин-Вью")).not.toBeNull();
    expect(within(summary).getByText("Дата-центр (DCH)")).not.toBeNull();
    expect(within(summary).getByText("0")).not.toBeNull();
    expect(within(summary).getAllByText("Не отмечен в базе")).toHaveLength(2);
    expect(summary.querySelector(".danger,.error,.success")).toBeNull();
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.queryByText("https://example.test/flag.png")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "О данных «IP и репутация»" }));
    const help = screen.getByRole("note");
    expect(help.textContent).toContain("не гарантирует отсутствие VPN");
    expect(help.textContent).toContain("а не к Claude");
    expect(help.textContent).toContain("не означает угрозу");
  });

  it.each(["not_configured", "unavailable", "rate_limited"] as const)("keeps %s separate from a successful check and links to a manual check", (status) => {
    render(<IPReputation result={{ ...fullResult, status, data: null, checkedAt: null, detail: "Дополнительная проверка недоступна." }} />);
    expect(screen.getByText("Дополнительная проверка недоступна.")).not.toBeNull();
    expect(screen.getByText("8.8.8.8")).not.toBeNull();
    expect(screen.queryByText("Данные получены")).toBeNull();
    expect((screen.getByRole("link", { name: /Проверить на IP2Location/ }) as HTMLAnchorElement).href).toBe("https://www.ip2location.com/demo");
  });

  it("handles partial nullable and empty nested responses without inventing negative flags", () => {
    const result = { ...fullResult, status: "partial", data: { country_name: null, asn: 0, fraud_score: null, proxy: null, country: {}, region: null } } as IPLookupResult;
    render(<IPReputation result={result} />);
    expect(screen.getByText(/Часть данных недоступна/)).not.toBeNull();
    const summary = document.querySelector(".ip-reputation-summary") as HTMLElement;
    expect(within(summary).getByText("0")).not.toBeNull();
    expect(within(summary).getAllByText("Нет данных").length).toBeGreaterThan(4);
    expect(within(summary).queryByText("Не отмечен в базе")).toBeNull();
    expect(ipLookupFields({ country: {}, proxy: null, region: null })).toEqual([
      { label: "Сведения о регионе", value: "Нет данных" }, { label: "Классификация адреса", value: "Нет данных" },
    ]);
    expect(ipLookupFields(null)).toEqual([]);
    expect(ipLookupFields({ proxy: "invalid", as_info: ["invalid"] })).toEqual([
      { label: "Сведения об автономной системе", value: "Нет данных" }, { label: "Классификация адреса", value: "Нет данных" },
    ]);
  });

  it("exports every allowlisted supplied field, including nested translations, flags and zeroes, as readable text", () => {
    const text = formatIPLookupText({ ...fullResult, token: "secret-session", data: { ...fullResult.data, private_key: "secret-key", proxy: { ...fullResult.data?.proxy, unexpected: "secret-proxy" } } });
    const fields = ipLookupFields(fullResult.data);
    expect(fields).toHaveLength(90);
    for (const field of fields) expect(text).toContain(`${field.label}: ${field.value}`);
    expect(text).toContain("Широта: 0");
    expect(text).toContain("Классификация адреса — Дней с последнего обнаружения: 0");
    expect(text).toContain("Классификация адреса — VPN: Не отмечен в базе");
    expect(text).toContain("Сведения о часовом поясе — Летнее время: Нет");
    expect(text).toContain("Континент — Перевод названия — Язык перевода: ru");
    expect(text).toContain("Континент — Перевод названия — Переведённое название: Северная Америка");
    expect(text).toContain("Классификация адреса — Отметка об угрозе: Нет данных");
    expect(text).not.toMatch(/secret-session|secret-key|secret-proxy|\[object Object\]/);
    expect(formatIPLookupText(null)).toContain("недоступны");
  });

  it("uses the provider translation schema and treats its missing marker as unavailable", () => {
    expect(ipLookupFields({ continent: { translation: { lang: "ru", value: "Европа", token: "secret" } } })).toEqual([
      { label: "Континент — Перевод названия — Язык перевода", value: "ru" },
      { label: "Континент — Перевод названия — Переведённое название", value: "Европа" },
    ]);
    expect(ipLookupValue(" - ")).toBe("Нет данных");
    expect(ipLookupValue(null)).toBe("Нет данных");
    expect(ipLookupValue(false, true)).toBe("Не отмечен в базе");
    expect(ipLookupValue(0)).toBe("0");
    expect(ipUsageName("constructor")).toBe("constructor");
    render(<IPReputation result={{ ...fullResult, data: { country_name: "-", city_name: "-", isp: "-" } }} />);
    expect(screen.queryByText("-, -")).toBeNull();
    expect(screen.queryByText("-")).toBeNull();
  });
});

describe("public IP input validation", () => {
  it.each(["8.8.8.8", "1.1.1.1", "162.35.172.6", "2001:4860:4860::8888", "2606:4700:4700::1111", "2001:4860:0:0:0:0:0:8888", "::ffff:8.8.8.8"])("accepts %s", (ip) => {
    expect(isValidPublicIP(ip)).toBe(true);
  });
  it.each(["", "example.com", "https://8.8.8.8", "8.8.8.8:443", "8.8.8.8/32", "8.8.8.8 ", "08.8.8.8", "256.1.1.1", "0.0.0.0", "10.0.0.1", "127.0.0.1", "100.64.0.1", "169.254.1.1", "172.31.0.1", "192.168.1.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::", "::1", "fe80::1", "fc00::1", "ff02::1", "2001:db8::1", "3fff::1", "2001::1", "2001:1ff:ffff::1", "2002:808:808::1", "2001:4860:::1", "2001::4860::1", "2001:4860:1:2:3:4:5:6:7", "2001:4860:1:2:3:4:5", "2001:4860:12345::1", "::ffff:192.168.1.1", "[2001:4860::1]", "fe80::1%eth0"])("rejects %s", (ip) => {
    expect(isValidPublicIP(ip)).toBe(false);
  });
});
