export type IPLookupStatus = "available" | "partial" | "not_configured" | "unavailable" | "rate_limited";
type Translation = { lang?: string | null; value?: string | null; } | null;

interface NamedLocation {
  name?: string | null;
  code?: string | null;
  translation?: Translation;
}

export interface IP2LocationData {
  ip?: string | null;
  country_code?: string | null;
  country_name?: string | null;
  region_name?: string | null;
  district?: string | null;
  city_name?: string | null;
  latitude?: number | null;
  longitude?: number | null;
  zip_code?: string | null;
  time_zone?: string | null;
  asn?: string | number | null;
  as?: string | null;
  as_info?: { as_number?: string | number | null; as_name?: string | null; as_domain?: string | null; as_usage_type?: string | null; as_cidr?: string | null; } | null;
  isp?: string | null;
  domain?: string | null;
  net_speed?: string | null;
  idd_code?: string | null;
  area_code?: string | null;
  weather_station_code?: string | null;
  weather_station_name?: string | null;
  mcc?: string | null;
  mnc?: string | null;
  mobile_brand?: string | null;
  elevation?: string | number | null;
  usage_type?: string | null;
  address_type?: string | null;
  ads_category?: string | null;
  ads_category_name?: string | null;
  continent?: NamedLocation & { hemisphere?: string[] | string | null; } | null;
  country?: NamedLocation & {
    alpha3_code?: string | null; numeric_code?: string | number | null; demonym?: string | null;
    flag?: string | null; capital?: string | null; total_area?: number | null; population?: number | null;
    currency?: { code?: string | null; name?: string | null; symbol?: string | null; } | null;
    language?: { code?: string | null; name?: string | null; } | null;
    tld?: string | null;
  } | null;
  region?: NamedLocation | null;
  city?: NamedLocation | null;
  time_zone_info?: {
    olson?: string | null; current_time?: string | null; gmt_offset?: number | null;
    is_dst?: boolean | null; abbreviation?: string | null; dst_start_date?: string | null;
    dst_end_date?: string | null; sunrise?: string | null; sunset?: string | null;
  } | null;
  geotargeting?: { metro?: string | number | null; } | null;
  is_proxy?: boolean | null;
  fraud_score?: number | null;
  proxy?: {
    last_seen?: number | string | null; proxy_type?: string | null; threat?: string | null; provider?: string | null;
    is_vpn?: boolean | null; is_tor?: boolean | null; is_data_center?: boolean | null;
    is_public_proxy?: boolean | null; is_web_proxy?: boolean | null; is_web_crawler?: boolean | null;
    is_ai_crawler?: boolean | null; is_residential_proxy?: boolean | null; is_consumer_privacy_network?: boolean | null;
    is_enterprise_private_network?: boolean | null; is_spammer?: boolean | null; is_scanner?: boolean | null;
    is_botnet?: boolean | null; is_bogon?: boolean | null;
  } | null;
}

export interface IPLookupResult {
  ip: string;
  source: "IP2Location.io";
  status: IPLookupStatus;
  checkedAt: string | null;
  detail: string;
  data: IP2LocationData | null;
}

export interface IPLookupField { label: string; value: string; }

export const ip2LocationDemo = "https://www.ip2location.com/demo";
export const ipLookupSource = "IP2Location.io";
export const noIPData = "Нет данных";
const number = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 8 });

export function lookupStatusLabel(status: unknown): string {
  switch (status) {
    case "available": return "Данные получены";
    case "partial": return "Часть данных недоступна";
    case "not_configured": return "Проверка не настроена";
    case "rate_limited": return "Лимит запросов исчерпан";
    default: return "Данные недоступны";
  }
}

export function ipLookupValue(value: unknown, flag = false): string {
  if (value === null || value === undefined) return noIPData;
  if (typeof value === "boolean") return flag ? value ? "Отмечен в базе" : "Не отмечен в базе" : value ? "Да" : "Нет";
  if (typeof value === "number") return Number.isFinite(value) ? number.format(value) : noIPData;
  if (typeof value === "string") return value.trim() && value.trim() !== "-" ? value.trim() : noIPData;
  if (Array.isArray(value)) {
    const items = value.filter((item): item is string | number => typeof item === "string" || typeof item === "number");
    return items.length ? items.map((item) => ipLookupValue(item)).join(", ") : noIPData;
  }
  return noIPData;
}

export function ipUsageName(value: unknown): string {
  if (typeof value !== "string") return noIPData;
  const types: Record<string, string> = {
    DCH: "Дата-центр (DCH)", ISP: "Интернет-провайдер (ISP)", MOB: "Мобильная сеть (MOB)",
    COM: "Организация (COM)", EDU: "Образование (EDU)", GOV: "Государственная сеть (GOV)",
    MIL: "Военная сеть (MIL)", ORG: "Организация (ORG)", SES: "Поисковая система (SES)",
  };
  return Object.hasOwn(types, value) ? types[value] : ipLookupValue(value);
}

type Field = readonly [key: string, label: string, kind?: "flag" | "usage" | readonly Field[]];
const translations: Field = ["translation", "Перевод названия", [["lang", "Язык перевода"], ["value", "Переведённое название"]]];
const namedLocation: readonly Field[] = [["name", "Название"], ["code", "Код"], translations];
const fields: readonly Field[] = [
  ["ip", "IP-адрес"], ["country_code", "Код страны"], ["country_name", "Страна"],
  ["region_name", "Регион"], ["district", "Район"], ["city_name", "Город"],
  ["latitude", "Широта"], ["longitude", "Долгота"], ["zip_code", "Почтовый индекс"], ["time_zone", "Часовой пояс"],
  ["asn", "ASN"], ["as", "Автономная система"],
  ["as_info", "Сведения об автономной системе", [["as_number", "Номер ASN"], ["as_name", "Название"], ["as_domain", "Домен"], ["as_usage_type", "Тип использования", "usage"], ["as_cidr", "Диапазон CIDR"]]],
  ["isp", "Провайдер"], ["domain", "Домен провайдера"], ["net_speed", "Категория соединения"],
  ["idd_code", "Международный телефонный код"], ["area_code", "Телефонный код региона"],
  ["weather_station_code", "Код метеостанции"], ["weather_station_name", "Название метеостанции"],
  ["mcc", "Код страны мобильной сети (MCC)"], ["mnc", "Код мобильной сети (MNC)"], ["mobile_brand", "Мобильный оператор"],
  ["elevation", "Высота над уровнем моря, м"], ["usage_type", "Тип использования IP", "usage"], ["address_type", "Тип адреса"],
  ["ads_category", "Код рекламной категории"], ["ads_category_name", "Рекламная категория"],
  ["continent", "Континент", [...namedLocation, ["hemisphere", "Полушария"]]],
  ["country", "Сведения о стране", [
    ...namedLocation, ["alpha3_code", "Код ISO из трёх букв"], ["numeric_code", "Числовой код страны"],
    ["demonym", "Название жителей"], ["flag", "Флаг"], ["capital", "Столица"],
    ["total_area", "Площадь, км²"], ["population", "Население"],
    ["currency", "Валюта", [["code", "Код"], ["name", "Название"], ["symbol", "Символ"]]],
    ["language", "Язык", [["code", "Код"], ["name", "Название"]]], ["tld", "Национальный домен"],
  ]],
  ["region", "Сведения о регионе", namedLocation], ["city", "Сведения о городе", namedLocation],
  ["time_zone_info", "Сведения о часовом поясе", [
    ["olson", "Название IANA"], ["current_time", "Местное время"], ["gmt_offset", "Смещение от GMT, секунды"],
    ["is_dst", "Летнее время"], ["abbreviation", "Сокращение"], ["dst_start_date", "Начало летнего времени"],
    ["dst_end_date", "Конец летнего времени"], ["sunrise", "Восход"], ["sunset", "Закат"],
  ]],
  ["geotargeting", "Геотаргетинг", [["metro", "Код агломерации"]]],
  ["is_proxy", "Прокси", "flag"], ["fraud_score", "Оценка риска IP2Location"],
  ["proxy", "Классификация адреса", [
    ["last_seen", "Дней с последнего обнаружения"], ["proxy_type", "Тип прокси"], ["threat", "Отметка об угрозе"], ["provider", "Провайдер прокси"],
    ["is_vpn", "VPN", "flag"], ["is_tor", "Tor", "flag"], ["is_data_center", "Дата-центр", "flag"],
    ["is_public_proxy", "Публичный прокси", "flag"], ["is_web_proxy", "Веб-прокси", "flag"],
    ["is_web_crawler", "Поисковый робот", "flag"], ["is_ai_crawler", "Робот сбора данных для ИИ", "flag"],
    ["is_residential_proxy", "Прокси с адресом домашней сети", "flag"],
    ["is_consumer_privacy_network", "Потребительская сеть приватности", "flag"],
    ["is_enterprise_private_network", "Корпоративная частная сеть", "flag"],
    ["is_spammer", "Рассылка спама", "flag"], ["is_scanner", "Сканирование сети", "flag"],
    ["is_botnet", "Ботнет", "flag"], ["is_bogon", "Специальный или нераспределённый адрес", "flag"],
  ]],
];

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

// Один список разрешённых полей используется в карточке и в читаемом отчёте.
export function ipLookupFields(value: unknown): IPLookupField[] {
  const output: IPLookupField[] = [];
  const visit = (source: unknown, schema: readonly Field[], prefix = "") => {
    const data = object(source);
    if (!data) return;
    for (const [key, title, kind] of schema) {
      if (!Object.hasOwn(data, key)) continue;
      const item = data[key];
      const label = prefix ? `${prefix} — ${title}` : title;
      if (Array.isArray(kind)) {
        if (object(item)) visit(item, kind, label);
        else output.push({ label, value: noIPData });
      } else output.push({ label, value: kind === "usage" ? ipUsageName(item) : ipLookupValue(item, kind === "flag") });
    }
  };
  visit(value, fields);
  return output;
}

function ipv4Parts(value: string): number[] | null {
  if (!/^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){3}$/.test(value)) return null;
  const parts = value.split(".").map(Number);
  return parts.every((part) => part <= 255) ? parts : null;
}

function publicIPv4(parts: number[]): boolean {
  const [a, b, c] = parts;
  if (a === 0 || a === 10 || a === 127 || a >= 224) return false;
  if (a === 100 && b >= 64 && b <= 127 || a === 169 && b === 254 || a === 172 && b >= 16 && b <= 31) return false;
  if (a === 192 && (b === 168 || b === 0 && (c === 0 || c === 2) || b === 88 && c === 99)) return false;
  if (a === 198 && (b === 18 || b === 19 || b === 51 && c === 100) || a === 203 && b === 0 && c === 113) return false;
  return true;
}

export function isValidPublicIP(value: string): boolean {
  const ipv4 = ipv4Parts(value);
  if (ipv4) return publicIPv4(ipv4);
  if (!value.includes(":") || /[^0-9a-f:.]/i.test(value) || value.split("::").length > 2) return false;
  let address = value;
  let embedded: number[] | null = null;
  if (value.includes(".")) {
    const divider = value.lastIndexOf(":");
    embedded = ipv4Parts(value.slice(divider + 1));
    if (!embedded) return false;
    address = `${value.slice(0, divider)}:${((embedded[0] << 8) | embedded[1]).toString(16)}:${((embedded[2] << 8) | embedded[3]).toString(16)}`;
  }
  const [left, right] = address.split("::");
  const leading = left ? left.split(":") : [];
  const trailing = right ? right.split(":") : [];
  if (![...leading, ...trailing].every((group) => /^[0-9a-f]{1,4}$/i.test(group))) return false;
  const compressed = address.includes("::");
  const explicit = leading.length + trailing.length;
  if (compressed ? explicit >= 8 : explicit !== 8) return false;
  const groups = [...leading, ...Array(compressed ? 8 - explicit : 0).fill("0"), ...trailing].map((group) => parseInt(group, 16));
  if (groups.slice(0, 5).every((group) => group === 0) && groups[5] === 0xffff) {
    return publicIPv4([groups[6] >> 8, groups[6] & 255, groups[7] >> 8, groups[7] & 255]);
  }
  if (embedded && !publicIPv4(embedded)) return false;
  // Диапазоны совпадают с проверкой API: исключаем также специальные туннельные и документационные адреса.
  if ((groups[0] & 0xe000) !== 0x2000 || groups[0] === 0x2002 ||
      groups[0] === 0x2001 && (groups[1] <= 0x01ff || groups[1] === 0x0db8) ||
      groups[0] === 0x3fff && groups[1] <= 0x0fff) return false;
  return true;
}
