import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { collectDeviceDiagnostics, type DeviceDiagnostics } from "./deviceDiagnostics";
import { CheckList, type CheckResult } from "./CheckResults";
import { DiagnosticReportDownload } from "./DiagnosticReportDownload";
import { DiagnosticProgress, type DiagnosticStage } from "./DiagnosticProgress";
import { HelpTip } from "./HelpTip";
import { Icon } from "./icons";
import { ErrorNotice } from "./ui";
import { IPReputation } from "./IPReputation";
import { isValidPublicIP, samePublicIP, type IPLookupResult } from "./ipLookup";
import { lookupIP } from "./ipLookupClient";
import { manualIPDiagnosticReport } from "./ipLookupText";

export type { CheckStatus, CheckResult } from "./CheckResults";

// Адрес и сеть, которой он принадлежит; сеть неизвестна, если её не удалось узнать.
export interface Network {
  ip: string;
  asn: number | null;
  isp: string | null;
  countryCode: string | null;
  city: string | null;
  timeZone: string | null;
  metadataUnavailable?: boolean;
}

// Что браузер узнаёт о себе по нажатию кнопки.
export interface BrowserFacts {
  ipv4: Network | null;
  ipv6: Network | null;
  // Внешние адреса, которые раскрывает WebRTC, и локальные адреса сети; null — WebRTC выключен.
  webrtc: { public: Network[]; local: string[] } | null;
  // DNS-серверы, которые разрешали имена для этого браузера; null — проверка DNS не ответила.
  dns: Network[] | null;
  // Сколько проб DNS получили ответ: частичный результат нельзя считать завершённой проверкой.
  dnsProgress?: { answered: number; total: number };
  timeZone: string;
}

export interface CheckRun {
  browser: CheckResult[];
  browserFacts: BrowserFacts;
  device: DeviceDiagnostics;
  checkedAt: number;
  ipLookups?: IPLookupResult[];
  targetIP?: string;
}

interface AddressCheckRun {
  targetIP: string;
  ipLookup: IPLookupResult;
  checkedAt: number;
}

type PrivacyCheckRun = CheckRun | AddressCheckRun;

// Внешние сервисы проверки. ipleak.net сообщает адрес и сеть по IPv4 и по IPv6, узнаёт сеть
// любого адреса и видит, какие DNS-серверы спрашивают у него уникальные имена его поддоменов.
export const ipv4Service = "https://ipv4.ipleak.net/json/";
export const ipv6Service = "https://ipv6.ipleak.net/json/";
const lookupService = "https://ipleak.net/json/";
const stunServers = ["stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"];
// Так выглядят IPv4- и IPv6-адреса; всё остальное в запрос к ipleak.net не подставляется.
const addressPattern = /^[0-9a-f:.]+$/i;

const reputationSites = [
  {
    name: "IP2Location",
    url: "https://www.ip2location.com/demo",
    checks: "География, провайдер, тип сети и отметки VPN или прокси в базе.",
    expect: "Сведения относятся к проверяемому IP; отсутствие отметки VPN не гарантирует доступ к сайтам.",
  },
  {
    name: "Scamalytics",
    url: "https://scamalytics.com/ip",
    checks: "Оценка риска IP и отметки VPN или прокси.",
    expect: "Проверьте выбранный IP; оценка отражает данные сервиса, а не фактический маршрут.",
  },
  {
    name: "IPData",
    url: "https://ipdata.co/",
    checks: "География, ASN и признаки угроз для IP.",
    expect: "Сведения относятся к выбранному IP; отсутствие отметки не гарантирует отсутствие риска.",
  },
  {
    name: "IPLocation",
    url: "https://www.iplocation.net/",
    checks: "Геолокация IP по нескольким базам.",
    expect: "Геолокация разных баз может различаться и не показывает точное местоположение.",
  },
  {
    name: "DB-IP",
    url: "https://db-ip.com/",
    checks: "География и сведения о сети IP.",
    expect: "Проверьте выбранный IP; сведения базы могут отличаться от других источников.",
  },
  {
    name: "ipapi.is",
    url: "https://ipapi.is/",
    checks: "ASN, хостинг, VPN и прокси по данным сервиса.",
    expect: "Признаки относятся к базе сервиса и не подтверждают маршрут вашего подключения.",
  },
  {
    name: "Hurricane Electric BGP",
    url: "https://bgp.he.net/",
    checks: "ASN, сетевой префикс и маршрутизация IP.",
    expect: "Данные BGP показывают сеть выбранного IP, а не качество или безопасность прокси.",
  },
  {
    name: "AbuseIPDB",
    url: "https://www.abuseipdb.com/",
    checks: "Жалобы на подозрительную активность с IP.",
    expect: "Отсутствие жалоб не доказывает чистую репутацию; проверьте дату сообщений.",
  },
  {
    name: "VirusTotal",
    url: "https://www.virustotal.com/",
    checks: "Репутация IP по нескольким источникам.",
    expect: "Сверьте выбранный IP; отметки источников могут отличаться и устаревать.",
  },
  {
    name: "Spamhaus",
    url: "https://check.spamhaus.org/",
    checks: "Наличие IP в блок-листах Spamhaus.",
    expect: "Отсутствие записи в списке не гарантирует доступность всех сайтов.",
  },
  {
    name: "2ip.ru",
    url: "https://2ip.ru/",
    checks: "IP, провайдер и подозрение на прокси — по-русски.",
    expect: "IP и сеть выбранного выхода.",
  },
  {
    name: "IPinfo",
    url: "https://ipinfo.io/",
    checks: "Кому принадлежит адрес: страна, сеть, отметки VPN и hosting.",
    expect: "Сеть выбранного выхода; отметка hosting сама по себе не означает проблему.",
  },
];

const connectionSites = [
  {
    name: "ipleak.net",
    url: "https://ipleak.net/",
    checks: "IP, DNS, WebRTC и IPv6 на одной странице.",
    expect: "IP выбранного выхода; DNS-серверы не вашего провайдера; в WebRTC нет адреса домашней сети.",
  },
  {
    name: "BrowserLeaks WebRTC",
    url: "https://browserleaks.com/webrtc",
    checks: "Подробно WebRTC, а в соседних разделах — отпечаток браузера.",
    expect: "Public IP пуст или это IP выбранного выхода; локальные адреса скрыты (.local).",
  },
  {
    name: "BrowserLeaks IP",
    url: "https://browserleaks.com/ip",
    checks: "Внешний IP и сетевые сведения, видимые сайту.",
    expect: "Виден IP выбранного выхода, а не адрес домашнего или мобильного провайдера.",
  },
  {
    name: "BrowserLeaks DNS",
    url: "https://browserleaks.com/dns",
    checks: "DNS-серверы, видимые при проверке браузера.",
    expect: "Домашний или мобильный провайдер не виден; список резолверов не доказывает путь DNS-запросов.",
  },
  {
    name: "DNS Leak Test",
    url: "https://www.dnsleaktest.com/",
    checks: "Чьи DNS-серверы узнают, какие сайты вы открываете. Нажмите «Extended test».",
    expect: "Нет серверов вашего домашнего или мобильного провайдера.",
  },
  {
    name: "test-ipv6.com",
    url: "https://test-ipv6.com/",
    checks: "Есть ли у устройства IPv6 мимо туннеля.",
    expect: "IPv4 — IP выбранного выхода; IPv6 идёт тем же маршрутом или недоступен.",
  },
  {
    name: "Whoer",
    url: "https://whoer.net/",
    checks: "Сводка: IP, DNS, часовой пояс, язык, признаки прокси.",
    expect: "IP выбранного выхода; замечания о поясе и языке — подсказки, а не утечка.",
  },
  {
    name: "IPhey",
    url: "https://iphey.com/",
    checks: "IP и согласованность параметров браузера.",
    expect: "Виден IP выбранного выхода; замечания об отпечатке сами по себе не означают утечку.",
  },
  {
    name: "Pixelscan",
    url: "https://pixelscan.net/",
    checks: "IP, WebRTC и признаки отпечатка браузера.",
    expect: "Виден IP выбранного выхода; сравните WebRTC с выбранным подключением.",
  },
  {
    name: "Cover Your Tracks",
    url: "https://coveryourtracks.eff.org/",
    checks: "Защищает ли браузер от трекеров и насколько уникален его отпечаток.",
    expect: "Строгая защита от трекеров; отпечаток не уникален.",
  },
  {
    name: "AmIUnique",
    url: "https://amiunique.org/",
    checks: "Насколько уникален отпечаток браузера среди всех посетителей.",
    expect: "Чем меньше уникальность, тем лучше; VPN её не меняет.",
  },
  {
    name: "BrowserScan",
    url: "https://www.browserscan.net/",
    checks: "Согласованы ли отпечаток, часовой пояс, язык, WebRTC и IP.",
    expect: "Без красных пунктов; несовпадение пояса с IP выдаёт VPN.",
  },
];
const siteGroups = [
  {
    title: "Данные об IP и репутации",
    description: "Проверьте нужный IP на сайте сервиса; где доступно, введите его вручную. Результаты баз могут различаться.",
    sites: reputationSites,
  },
  {
    title: "Текущее подключение и браузер",
    description: "Откройте эти сайты на проверяемом устройстве с включённым нужным VPN-подключением.",
    sites: connectionSites,
  },
];
const sites = siteGroups.flatMap((group) => group.sites);
const siteWord = { one: "сайт", few: "сайта", many: "сайтов", zero: "сайтов", two: "сайта", other: "сайтов" }[
  new Intl.PluralRules("ru").select(sites.length)
];

export function isIPAddress(value: string) {
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(value) || value.includes(":");
}

// Адреса, которые ничего не значат за пределами сети самого устройства.
export function isPrivateAddress(address: string) {
  const parts = address.split(".").map(Number);
  if (parts.length === 4 && parts.every((part) => Number.isInteger(part) && part >= 0 && part <= 255)) {
    const [a, b] = parts;
    return (
      a === 10 ||
      a === 127 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      (a === 169 && b === 254) ||
      (a === 100 && b >= 64 && b <= 127)
    );
  }
  const v6 = address.toLowerCase();
  return v6 === "::1" || v6.startsWith("fe80:") || v6.startsWith("fc") || v6.startsWith("fd");
}

function same(a?: string | null, b?: string | null) {
  return !!a && !!b && a.toLowerCase() === b.toLowerCase();
}

function utcOffset(timeZone: string, at: Date) {
  try {
    return (
      new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "longOffset" })
        .formatToParts(at)
        .find((part) => part.type === "timeZoneName")?.value ?? null
    );
  } catch {
    return null;
  }
}

function countryName(code: string | null) {
  if (!code) return "";
  try {
    return new Intl.DisplayNames(["ru"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

// Чей адрес — так, чтобы владелец узнал провайдера: «MTS PJSC, Россия».
function owner(network: Network) {
  const country = countryName(network.countryCode);
  const isp = network.isp ?? "сеть неизвестна";
  return country ? `${isp}, ${country}` : isp;
}

function sameNetwork(a: Network, b: Network) {
  if (a.asn !== null && b.asn !== null) return a.asn === b.asn;
  return !!a.isp && a.isp === b.isp;
}

function unknownNetwork(network: Network) {
  return network.asn === null && !network.isp;
}

function addresses(networks: Network[]) {
  return networks.map((network) => `${network.ip} (${owner(network)})`).join(", ");
}

interface DNSGroup {
  operator: string;
  addresses: string[];
}

// Группируем по названию оператора из справки об IP, не по ASN или знакомому бренду DNS.
// Неизвестные сети остаются отдельной группой, а каждый обнаруженный IP — в подробностях.
export function dnsGroups(networks: Network[]): DNSGroup[] {
  const groups = new Map<string, DNSGroup>();
  const seen = new Set<string>();
  for (const network of networks) {
    const ip = network.ip.toLowerCase();
    if (seen.has(ip)) continue;
    seen.add(ip);
    const name = network.isp?.trim();
    const operator = name && !/^(unknown|n\/?a|-)$/i.test(name) ? name : "Оператор неизвестен";
    const key = operator.toLowerCase();
    const group = groups.get(key) ?? { operator, addresses: [] };
    group.addresses.push(network.ip);
    groups.set(key, group);
  }
  return [...groups.values()].sort((a, b) =>
    Number(a.operator === "Оператор неизвестен") - Number(b.operator === "Оператор неизвестен"));
}

function dnsCount(count: number) {
  const last = count % 10;
  const lastTwo = count % 100;
  const form = last === 1 && lastTwo !== 11 ? "адрес"
    : last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14) ? "адреса" : "адресов";
  return `${count} ${form}`;
}

function dnsCountAfterFor(count: number) {
  return `${count} ${count % 10 === 1 && count % 100 !== 11 ? "адреса" : "адресов"}`;
}

// Превращает увиденное браузером в результаты. Основной выход в интернет — по IPv4.
// Список DNS-резолверов не устанавливает маршрут DNS-запросов. serverIP — адрес шлюза.
export function browserVerdicts(facts: BrowserFacts, serverIP: string | null, now = new Date()): CheckResult[] {
  const results: CheckResult[] = [];
  const main = facts.ipv4 ?? facts.ipv6;
  const exit = { id: "exit", title: "Выход в интернет" };
  if (!main) {
    results.push({
      ...exit,
      status: "warn",
      detail: "Не удалось узнать адрес: ipleak.net не ответил. Проверьте вручную на сайтах из списка ниже.",
    });
  } else if (serverIP && same(main.ip, serverIP)) {
    results.push({ ...exit, status: "ok", detail: `Сайты видят ${main.ip} — ваш шлюз (${owner(main)}).` });
  } else if (serverIP) {
    results.push({
      ...exit,
      status: "info",
      detail: `Сайты видят ${main.ip} — ${owner(main)}. Это не IP сервера (${serverIP}). Если выбран профиль «IP прокси» или другой VPN, сравните адрес с ожидаемым IP выбранного выхода. Панель не знает, какой профиль сейчас включён на устройстве.`,
    });
  } else {
    results.push({
      ...exit,
      status: "info",
      detail: `Сайты видят ${main.ip} — ${owner(main)}. IP сервера не указан или задан доменом; автоматическое сравнение недоступно. Сверьте с ожидаемым IP выбранного профиля.`,
    });
  }

  const ipv6 = { id: "ipv6", title: "IPv6" };
  if (!facts.ipv6) {
    results.push({ ...ipv6, status: "ok", detail: "Сайты не видят IPv6-адреса: всё идёт по IPv4." });
  } else if (!facts.ipv4) {
    results.push({
      ...ipv6,
      status: "info",
      detail: "Устройство выходит в интернет только по IPv6 — этот адрес и проверен выше.",
    });
  } else if (sameNetwork(facts.ipv6, facts.ipv4)) {
    results.push({
      ...ipv6,
      status: "ok",
      detail: `Сайты видят IPv6-адрес ${facts.ipv6.ip} той же сети, что и IPv4 (${owner(facts.ipv6)}): IPv6 идёт тем же путём.`,
    });
  } else if (unknownNetwork(facts.ipv6)) {
    results.push({
      ...ipv6,
      status: "warn",
      detail: `Сайты видят IPv6-адрес ${facts.ipv6.ip}, но чей он, узнать не удалось. Если это адрес вашего провайдера, IPv6 идёт мимо VPN.`,
    });
  } else {
    results.push({
      ...ipv6,
      status: "fail",
      detail: `Сайты видят IPv6-адрес ${facts.ipv6.ip} — ${owner(facts.ipv6)}, а IPv4 идёт через ${owner(facts.ipv4)}: IPv6 идёт мимо VPN. Отключите IPv6 в приложении VPN или в системе — шаг «IPv6» в «Инструкциях».`,
    });
  }

  const webrtc = { id: "webrtc", title: "WebRTC" };
  if (!facts.webrtc) {
    results.push({ ...webrtc, status: "ok", detail: "WebRTC в этом браузере выключен — через него адрес не утечёт." });
  } else {
    const exits = [facts.ipv4, facts.ipv6].filter((network): network is Network => network !== null);
    // Адреса, которые сайты и так видят, оценены выше; утечка WebRTC — только то, что он добавляет.
    const added = facts.webrtc.public.filter((network) => !exits.some((known) => same(known.ip, network.ip)));
    const foreign = main ? added.filter((network) => !sameNetwork(network, main)) : [];
    if (!main && added.length) {
      results.push({
        ...webrtc,
        status: "info",
        detail: `WebRTC показывает ${addresses(added)}; сравнить с выходом в интернет не удалось: ipleak.net не ответил.`,
      });
    } else if (foreign.length) {
      results.push({
        ...webrtc,
        status: "fail",
        detail: `WebRTC раскрывает ${addresses(foreign)} — сайты узнают этот адрес в обход прокси или VPN. Откройте раздел «Браузеры» в «Инструкциях», настройте WebRTC и повторите проверку в этом же режиме браузера, включая инкогнито.`,
      });
    } else if (facts.webrtc.local.length) {
      results.push({
        ...webrtc,
        status: "warn",
        detail: `WebRTC показывает адрес в вашей локальной сети: ${facts.webrtc.local.join(", ")}. Внешний IP он не раскрывает, но делает браузер заметнее.`,
      });
    } else {
      results.push({
        ...webrtc,
        status: "ok",
        detail: facts.webrtc.public.length
          ? "WebRTC показывает только адреса той сети, через которую вы выходите."
          : "WebRTC не раскрывает ваших адресов.",
      });
    }
  }

  const dns = { id: "dns", title: "DNS" };
  if (!facts.dns) {
    results.push({ ...dns, status: "skip", detail: "Проверка DNS не завершена: ipleak.net не ответил. Недостаточно данных о резолверах и маршруте запросов." });
  } else if (!facts.dns.length) {
    const partial = !!facts.dnsProgress && facts.dnsProgress.answered < facts.dnsProgress.total;
    results.push({
      ...dns,
      status: "skip",
      detail: `${partial ? `Проверка DNS не завершена: ответили ${facts.dnsProgress!.answered} из ${facts.dnsProgress!.total} проб. ` : ""}Недостаточно данных: ipleak.net не показал DNS-резолверов этого браузера. Повторите проверку; отсутствие адресов не подтверждает отсутствие утечки.`,
    });
  } else {
    const summary = dnsGroups(facts.dns)
      .map((group) => `${group.operator} — обнаружено ${dnsCount(group.addresses.length)}`)
      .join("; ");
    const incomplete = !!facts.dnsProgress && facts.dnsProgress.answered < facts.dnsProgress.total;
    const missingMetadata = facts.dns.filter((network) => network.metadataUnavailable).length;
    results.push({
      ...dns,
      status: incomplete || missingMetadata ? "warn" : "info",
      detail: `${incomplete ? `Проверка DNS не завершена: ответили ${facts.dnsProgress!.answered} из ${facts.dnsProgress!.total} проб. ` : ""}Обнаружены резолверы: ${summary}. ${missingMetadata ? `Сведения об операторе недоступны для ${dnsCountAfterFor(missingMetadata)}. ` : ""}Адреса показаны в подробностях. Маршрут DNS-запросов этой проверкой не подтверждён; она относится только к этому браузеру.`,
    });
  }

  const ipZone = main?.timeZone ?? null;
  const browserOffset = utcOffset(facts.timeZone, now);
  const ipOffset = ipZone ? utcOffset(ipZone, now) : null;
  const timezone = { id: "timezone", title: "Часовой пояс" };
  if (!ipZone) {
    results.push({
      ...timezone,
      status: "info",
      detail: `Часовой пояс браузера — ${facts.timeZone}. Пояс вашего IP узнать не удалось.`,
    });
  } else if (ipZone === facts.timeZone || (browserOffset !== null && browserOffset === ipOffset)) {
    results.push({
      ...timezone,
      status: "ok",
      detail: `Пояс браузера ${facts.timeZone} совпадает по времени с поясом IP (${ipZone}).`,
    });
  } else {
    results.push({
      ...timezone,
      status: "warn",
      detail: `Пояс браузера — ${facts.timeZone}${browserOffset ? ` (${browserOffset})` : ""}, а IP — из ${ipZone}${ipOffset ? ` (${ipOffset})` : ""}. Это не утечка адреса, но по несовпадению сайты могут понять, что вы за VPN.`,
    });
  }
  return results;
}

async function outside<T>(url: string, signal: AbortSignal): Promise<T> {
  // Без cookies и без адреса панели: сервис узнаёт только адрес, как любой сайт.
  const response = await fetch(url, {
    credentials: "omit",
    referrerPolicy: "no-referrer",
    cache: "no-store",
    signal: AbortSignal.any([signal, AbortSignal.timeout(8_000)]),
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return (await response.json()) as T;
}

// Ответ ipleak.net об адресе.
interface Lookup {
  ip?: unknown;
  as_number?: unknown;
  isp_name?: unknown;
  country_code?: unknown;
  city_name?: unknown;
  time_zone?: unknown;
}

function text(value: unknown) {
  return typeof value === "string" && value ? value : null;
}

function network(data: Lookup, ip: string): Network {
  return {
    ip: text(data.ip) ?? ip,
    asn: typeof data.as_number === "number" ? data.as_number : null,
    isp: text(data.isp_name),
    countryCode: text(data.country_code),
    city: text(data.city_name),
    timeZone: text(data.time_zone),
  };
}

// Адрес и сеть, с которыми браузер выходит в интернет по одному протоколу; null — такого выхода нет.
async function exitNetwork(url: string, signal: AbortSignal) {
  try {
    const data = await outside<Lookup>(url, signal);
    const ip = text(data.ip);
    return ip && isValidPublicIP(ip) ? network(data, ip) : null;
  } catch {
    return null;
  }
}

async function browserExits(signal: AbortSignal) {
  const exits = await Promise.all([exitNetwork(ipv4Service, signal), exitNetwork(ipv6Service, signal)]);
  signal.throwIfAborted();
  return exits;
}

async function lookup(ip: string, signal: AbortSignal): Promise<Network> {
  const unknown = { ip, asn: null, isp: null, countryCode: null, city: null, timeZone: null };
  if (!addressPattern.test(ip)) return unknown;
  try {
    // Источник адреса — DNS/STUN-наблюдение. Справка может уточнить только сеть,
    // но её поле ip не должно подменять фактически обнаруженный адрес.
    return { ...network(await outside<Lookup>(lookupService + ip, signal), ip), ip };
  } catch {
    return { ...unknown, metadataUnavailable: true };
  }
}

// ipleak.net — авторитетный DNS-сервер своих поддоменов. Каждое имя ниже новое, поэтому DNS-сервер,
// которым пользуется браузер, спрашивает его у ipleak.net, а ответ на запрос называет всех, кто
// спрашивал имена этой сессии. Сохраняем число ответивших проб: частичный ответ
// внешнего сервиса нельзя выдавать за законченную проверку.
async function dnsServers(signal: AbortSignal) {
  const session = Array.from(crypto.getRandomValues(new Uint8Array(20)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
  const answers = await Promise.all(
    Array.from({ length: 10 }, (_, index) =>
      outside<{ ip?: unknown }>(`https://${session}-${index}.ipleak.net/dnsdetection/`, signal).catch(() => null),
    ),
  );
  const found = new Set<string>();
  let answered = 0;
  for (const answer of answers) {
    if (!answer || typeof answer !== "object" || !answer.ip ||
        typeof answer.ip !== "object" || Array.isArray(answer.ip)) continue;
    answered++;
    for (const address of Object.keys(answer.ip)) if (addressPattern.test(address)) found.add(address);
  }
  return { addresses: [...found], answered, total: answers.length };
}

// Собирает кандидатов WebRTC так же, как это сделал бы сайт: STUN-сервер сообщает браузеру его
// внешний адрес. Имена mDNS (*.local) уже скрывают локальные адреса и пропускаются.
function webrtcAddresses(signal: AbortSignal, timeout = 4_000): Promise<{ public: string[]; local: string[] } | null> {
  signal.throwIfAborted();
  if (typeof RTCPeerConnection === "undefined") return Promise.resolve(null);
  return new Promise((resolve) => {
    const found = { public: new Set<string>(), local: new Set<string>() };
    let connection: RTCPeerConnection;
    try {
      connection = new RTCPeerConnection({ iceServers: [{ urls: stunServers }] });
    } catch {
      resolve(null);
      return;
    }
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", finish);
      connection.close();
      resolve({ public: [...found.public], local: [...found.local] });
    };
    const timer = setTimeout(finish, timeout);
    signal.addEventListener("abort", finish, { once: true });
    connection.onicecandidate = (event) => {
      if (!event.candidate) return finish();
      const fields = event.candidate.candidate.split(" ");
      const candidate = fields[4];
      const type = fields[fields.indexOf("typ") + 1];
      if (!candidate || candidate.endsWith(".local")) return;
      (type === "host" ? found.local : found.public).add(candidate);
    };
    try {
      connection.createDataChannel("check");
      connection
        .createOffer()
        .then((offer) => { if (!finished) return connection.setLocalDescription(offer); })
        .catch(finish);
    } catch {
      finish();
    }
  });
}

async function browserFacts(signal: AbortSignal, step: (label: string) => void, exits: (Network | null)[]): Promise<BrowserFacts> {
  const probe = async <T,>(promise: Promise<T>): Promise<T> => {
    const value = await promise;
    signal.throwIfAborted();
    step("Проверяем IP, DNS и WebRTC…");
    return value;
  };
  const [ipv4, ipv6, webrtc, dns] = await Promise.all([
    probe(Promise.resolve(exits[0])),
    probe(Promise.resolve(exits[1])),
    probe(webrtcAddresses(signal)),
    probe(dnsServers(signal)),
  ]);
  // Один запрос на адрес: сети выходов уже известны, а DNS-серверы повторяются.
  const known = new Map<string, Promise<Network>>();
  for (const exit of [ipv4, ipv6]) if (exit) known.set(exit.ip.toLowerCase(), Promise.resolve(exit));
  const find = (address: string) => {
    const key = address.toLowerCase();
    let found = known.get(key);
    if (!found) {
      found = lookup(address, signal);
      known.set(key, found);
    }
    return found;
  };
  const outsideRTC = webrtc
    ? [...webrtc.public, ...webrtc.local.filter((address) => !isPrivateAddress(address))]
    : [];
  const [rtc, servers] = await Promise.all([
    Promise.all(outsideRTC.map(find)),
    // Все адреса остаются в результате. Для остальных после 12 запросов к справке
    // оператор остаётся неизвестным, а неполнота справки отмечается отдельно.
    dns.answered ? Promise.all(dns.addresses.map((address, index) => index < 12
      ? find(address)
      : Promise.resolve({ ip: address, asn: null, isp: null, countryCode: null, city: null, timeZone: null,
        metadataUnavailable: true }))) : null,
  ]);
  signal.throwIfAborted();
  step("Готовим результат…");
  return {
    ipv4,
    ipv6,
    webrtc: webrtc && { public: rtc, local: webrtc.local.filter(isPrivateAddress) },
    dns: servers,
    dnsProgress: { answered: dns.answered, total: dns.total },
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  };
}

async function runChecks(token: string, nodeHost: string | undefined, targetIP: string, requireCurrent: boolean,
  signal: AbortSignal, onProgress: (stage: DiagnosticStage) => void): Promise<PrivacyCheckRun> {
  signal.throwIfAborted();
  onProgress({ label: "Определяем текущее подключение…", percent: 5 });
  const exits = await browserExits(signal);
  const isCurrent = exits.some((exit) => exit && samePublicIP(exit.ip, targetIP));
  if (!isCurrent) {
    if (requireCurrent) {
      throw new Error(exits.some(Boolean)
        ? `IP подключения изменился. Адрес ${targetIP} больше не является текущим. Нажмите «Мой IP» и повторите проверку.`
        : "Не удалось подтвердить текущий IP браузера. Нажмите «Мой IP» и повторите проверку или введите адрес вручную для справки об IP.");
    }
    onProgress({ label: `Запрашиваем сведения об IP ${targetIP}…`, percent: 40 });
    const ipLookup = await lookupIP(token, targetIP, signal);
    signal.throwIfAborted();
    return { targetIP, ipLookup, checkedAt: Date.now() };
  }
  let completed = 0;
  const step = (label: string) => {
    signal.throwIfAborted();
    completed++;
    onProgress({ label: completed === 5 ? "Уточняем данные сетей…" : label, percent: Math.floor(completed / 7 * 100) });
  };
  const device = collectDeviceDiagnostics();
  step("Проверяем IP, DNS и WebRTC…");
  const facts = await browserFacts(signal, step, exits);
  signal.throwIfAborted();
  onProgress({ label: "Уточняем сведения об IP…", percent: 85 });
  const measuredIPs = [...new Set([facts.ipv4?.ip, facts.ipv6?.ip].filter((ip): ip is string => !!ip))];
  const ipLookups = await Promise.all(measuredIPs.map((ip) => lookupIP(token, ip, signal)));
  onProgress({ label: "Сверяем IP подключения…", percent: 95 });
  const finalExits = await browserExits(signal);
  if (!exits.every((exit, index) => exit
    ? !!finalExits[index] && samePublicIP(exit.ip, finalExits[index]!.ip)
    : !finalExits[index])) {
    throw new Error("IP подключения изменился во время проверки или не подтвердился повторно. Результаты не сохранены. Нажмите «Мой IP» и повторите проверку.");
  }
  signal.throwIfAborted();
  const serverIP = nodeHost && isIPAddress(nodeHost) ? nodeHost : null;
  const result = { targetIP, browser: browserVerdicts(facts, serverIP), browserFacts: facts, device, ipLookups, checkedAt: Date.now() };
  step("Проверка завершена.");
  return result;
}

// Отчёт безопасности не включает замеры скорости, параметры VPS или сеанс входа.
export function privacyDiagnosticReport(result: CheckRun, panelOrigin: string, manualIPLookup?: IPLookupResult | null) {
  return {
    format: "private-gateway-privacy-diagnostics",
    version: 1,
    exportedAt: new Date().toISOString(),
    checkedAt: new Date(result.checkedAt).toISOString(),
    targetIP: result.targetIP ?? result.browserFacts.ipv4?.ip ?? result.browserFacts.ipv6?.ip ?? null,
    panelOrigin,
    browser: result.browser,
    browserFacts: result.browserFacts,
    device: result.device,
    ipLookups: result.ipLookups ?? [],
    manualIPLookup: manualIPLookup ?? null,
  };
}

export function Checks({ token, nodeHost }: { token: string; nodeHost?: string }) {
  const client = useQueryClient();
  const inputID = useId();
  const [input, setInput] = useState("");
  const inputEdited = useRef(false);
  const inputRevision = useRef(0);
  const detection = useRef<AbortController | null>(null);
  const [detecting, setDetecting] = useState(true);
  const [detectionError, setDetectionError] = useState<Error | null>(null);
  const [validationError, setValidationError] = useState<Error | null>(null);
  const submitted = useRef({ targetIP: "", requireCurrent: false });
  const [stopped, setStopped] = useState(false);
  const [progress, setProgress] = useState<DiagnosticStage>({ label: "Начинаем проверку…", percent: 0 });
  const run = useQuery<PrivacyCheckRun>({
    queryKey: ["privacy-checks"],
    queryFn: ({ signal }) => runChecks(token, nodeHost, submitted.current.targetIP, submitted.current.requireCurrent, signal, setProgress),
    enabled: false,
    retry: false,
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  });
  const detect = async (replace: boolean) => {
    detection.current?.abort();
    const controller = new AbortController();
    const revision = inputRevision.current;
    detection.current = controller;
    setDetecting(true);
    setDetectionError(null);
    try {
      const exits = await browserExits(controller.signal);
      const ip = exits.find(Boolean)?.ip;
      if (!ip) throw new Error("Не удалось определить IP браузера. Введите внешний адрес вручную или нажмите «Мой IP», чтобы повторить.");
      if (revision === inputRevision.current && (replace || !inputEdited.current)) {
        inputEdited.current = false;
        setInput(ip);
        setValidationError(null);
      }
    } catch (error) {
      if (!controller.signal.aborted) setDetectionError(error as Error);
    } finally {
      if (!controller.signal.aborted) setDetecting(false);
    }
  };
  useEffect(() => {
    void detect(false);
    return () => detection.current?.abort();
  }, []);
  // Результат относится к неизменному адресу запуска. При редактировании поля
  // прежние карточки и отчёт скрываются, даже если старый запрос уже завершился.
  const data = run.data;
  const resultIP = data && (data.targetIP ?? ("browserFacts" in data ? data.browserFacts.ipv4?.ip ?? data.browserFacts.ipv6?.ip : null));
  const selected = resultIP && samePublicIP(resultIP, input.trim()) ? data : undefined;
  const result = selected && "browserFacts" in selected ? selected : undefined;
  const addressResult = selected && "ipLookup" in selected ? selected : undefined;
  const currentAttempt = samePublicIP(submitted.current.targetIP, input.trim());
  const problems = result?.browser.filter((check) => check.status === "fail").length ?? 0;
  const warnings = result?.browser.filter((check) => check.status === "warn").length ?? 0;
  const dnsIncomplete = result?.browser.some((check) => check.id === "dns" && check.status === "skip") ?? false;
  const previousResult = run.isFetching || stopped || run.isError;
  return (
    <>
      <section className="card check-run">
        <div className="check-run-text">
          <div className="diagnostics-title-with-help">
            <h2>Проверить безопасность</h2>
            <HelpTip label="О проверке безопасности">
              <p>Проверяем IP, DNS, IPv6, WebRTC, часовой пояс и сведения об устройстве, доступные сайтам. Скорость и состояние VPS — в разделе «Сервер».</p>
              <p>Браузер обращается к ipleak.net (AirVPN) при открытии страницы, чтобы подставить текущий IP. Полная проверка запускается по кнопке и также использует STUN-серверы Google и Cloudflare. Внешние сервисы увидят IP; токен входа и VPN-профиль им не передаются.</p>
              <p>DNS-результат показывает резолверы этого браузера, но не доказывает маршрут DNS-запросов и не описывает все программы устройства.</p>
              <p>Измеренные IPv4 и IPv6 передаются через API панели в IP2Location.io для справки о сети и репутации. Ключ сервиса хранится на сервере.</p>
              <p>Можно ввести любой публичный IPv4 или IPv6. Для чужого адреса запрашиваются только сведения об IP: DNS, WebRTC и параметры чужого браузера по адресу определить нельзя.</p>
              <p>Процент показывает завершённые этапы, а не оставшееся время. 100% означает, что проверка закончена; её результат смотрите ниже.</p>
            </HelpTip>
          </div>
          <p>Проверьте текущий IP или введите другой адрес.</p>
        </div>
        <form className="privacy-ip-form" onSubmit={(event) => {
          event.preventDefault();
          if (run.isFetching) return;
          const targetIP = input.trim();
          if (!isValidPublicIP(targetIP)) {
            setValidationError(new Error("Введите корректный внешний IPv4 или IPv6 без порта и ссылки. Локальные и служебные адреса не проверяются."));
            return;
          }
          detection.current?.abort();
          setDetecting(false);
          submitted.current = { targetIP, requireCurrent: !inputEdited.current };
          setValidationError(null);
          setDetectionError(null);
          setStopped(false);
          setProgress({ label: "Начинаем проверку…", percent: 0 });
          void run.refetch();
        }}>
          <div className="ip-lookup-field">
            <label htmlFor={inputID}>IP-адрес</label>
            <input id={inputID} type="text" value={input} onChange={(event) => {
              inputEdited.current = true;
              inputRevision.current++;
              setInput(event.target.value);
              setValidationError(null);
            }} placeholder={detecting ? "Определяем ваш IP…" : "Введите IPv4 или IPv6"}
            autoComplete="off" spellCheck={false} maxLength={64} disabled={run.isFetching}
            aria-invalid={!!validationError} />
          </div>
          <button type="button" className="button secondary-button" disabled={run.isFetching || detecting}
            onClick={() => { void detect(true); }}>Мой IP</button>
          {run.isFetching ? (
            <button type="button" className="button" onClick={() => {
              void client.cancelQueries({ queryKey: ["privacy-checks"] });
              setStopped(true);
            }}>
              <Icon name="close" size={18} />Остановить
            </button>
          ) : (
            <button type="submit" className="button primary">
              <Icon name="shield" size={18} />
              {selected ? "Проверить снова" : "Проверить IP"}
            </button>
          )}
        </form>
        {detecting && <p className="hint privacy-ip-hint" role="status">Определяем текущий IP браузера…</p>}
        <ErrorNotice error={validationError || detectionError} />
        {result && (
          <div
            className={`notice check-summary ${previousResult ? "" : problems ? "error" : warnings || dnsIncomplete ? "warning-notice" : "success-notice"}`}
            role="status"
          >
            <Icon name={previousResult ? "info" : problems ? "close" : warnings || dnsIncomplete ? "alert" : "check"} size={17} />
            <span>
              {previousResult && "Предыдущая проверка: "}
              {problems
                ? `Найдено проблем: ${problems}${warnings ? `, замечаний: ${warnings}` : ""}. Подробности и что сделать — ниже.`
                : warnings
                  ? `Найдено замечаний: ${warnings}. Посмотрите подробности ниже.`
                  : dnsIncomplete
                    ? "Данных DNS-проверки недостаточно. Посмотрите причину ниже."
                  : "По выполненным проверкам IP, IPv6 и WebRTC проблем не найдено."}{" "}
              Маршрут DNS-запросов не подтверждён. {" "}
              Проверено в {new Date(result.checkedAt).toLocaleTimeString("ru-RU", { timeStyle: "short" })}.
            </span>
          </div>
        )}
        <ErrorNotice error={currentAttempt ? run.error : null} />
        {addressResult && <p className="notice check-summary" role="status">{previousResult && "Предыдущая проверка: "}Проверен IP {addressResult.targetIP}. Это справка об адресе, а не проверка текущего подключения. Проверено в {new Date(addressResult.checkedAt).toLocaleTimeString("ru-RU", { timeStyle: "short" })}.</p>}
        <DiagnosticProgress stage={progress} busy={run.isFetching} stopped={currentAttempt && stopped} failed={currentAttempt && run.isError}
          complete={!!selected} label="Ход проверки безопасности" stoppedLabel="Проверка безопасности остановлена." />
      </section>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Что видят сайты</h2>
            <p>Проверки этого браузера на этом устройстве, не всех программ компьютера.</p>
          </div>
          <Icon name="browser" size={23} />
        </div>
        {result ? <CheckList checks={result.browser} /> : addressResult ? <CheckList checks={[
          { id: "exit", title: "Выход в интернет", status: "skip", detail: "Введённый IP не подтверждён как текущее подключение браузера. Сведения об адресе показаны ниже." },
          ...["DNS", "IPv6", "WebRTC", "Часовой пояс"].map((title) => ({ id: title, title, status: "skip" as const,
            detail: "Относится к текущему браузеру. По чужому IP эта проверка недоступна. Нажмите «Мой IP» и запустите проверку." })),
        ]} /> : <div className="loading">{run.isFetching ? "Проверяем…" : "Укажите IP и нажмите «Проверить IP»."}</div>}
        {result?.browserFacts.dns && result.browserFacts.dns.length > 0 && (
          <details className="check-fingerprint">
            <summary>Адреса DNS-резолверов ({result.browserFacts.dns.length})</summary>
            <dl>
              {dnsGroups(result.browserFacts.dns).map((group) => (
                <div key={group.operator}>
                  <dt>{group.operator} — {dnsCount(group.addresses.length)}</dt>
                  <dd>{group.addresses.join(", ")}</dd>
                </div>
              ))}
            </dl>
            <p className="hint">Эти адреса увидел внешний сервис; путь DNS-запросов и DNS других программ здесь не проверяются.</p>
          </details>
        )}
        {result && (
          <details className="check-fingerprint">
            <summary>Что ещё видит любой сайт</summary>
            <dl>
              {result.device.facts.map(({ id, label, value }) => (
                <div key={id}>
                  <dt>{label}</dt>
                  <dd>{value}</dd>
                </div>
              ))}
            </dl>
            {result.device.notes.map((note) => <p className="hint" key={note}>{note}</p>)}
          </details>
        )}
      </section>
      {result?.ipLookups?.map((lookup) => (
        <IPReputation key={lookup.ip} result={lookup} title="IP и репутация"
          description={`Текущий выход браузера по ${lookup.ip.includes(":") ? "IPv6" : "IPv4"}.`} />
      ))}
      {result && !result.ipLookups?.length && (
        <IPReputation result={null} description="Текущий выход браузера не определён. Повторите проверку безопасности." />
      )}
      {addressResult && <IPReputation result={addressResult.ipLookup} title="IP и репутация"
        description={`Проверяемый IP: ${addressResult.targetIP}. Сведения относятся только к этому адресу.`} />}
      {selected && <DiagnosticReportDownload
        kind="privacy"
        report={() => result
          ? privacyDiagnosticReport(result, window.location.origin)
          : manualIPDiagnosticReport("privacy", addressResult!.ipLookup, addressResult!.checkedAt, window.location.origin)}
        checkedAt={selected.checkedAt}
        busy={run.isFetching}
      />}
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>{sites.length} {siteWord} для проверки</h2>
            <p>Сведения об IP и проверки текущего подключения собраны отдельно.</p>
          </div>
        </div>
        {siteGroups.map((group) => (
          <div className="check-site-group" key={group.title}>
            <h3>{group.title}</h3>
            <p>{group.description}</p>
            <ul className="check-sites">
              {group.sites.map((site) => (
                <li key={site.name}>
                  <div>
                    <a className="button secondary-button" href={site.url} target="_blank" rel="noopener noreferrer">
                      {site.name}
                      <Icon name="external" size={14} />
                    </a>
                    <span>{site.checks}</span>
                  </div>
                  <p>
                    <strong>Ожидается:</strong> {site.expect}
                  </p>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </section>
    </>
  );
}
