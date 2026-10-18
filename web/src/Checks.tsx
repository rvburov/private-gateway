import { useQuery } from "@tanstack/react-query";
import { errorMessage, request } from "./api";
import { Icon, type IconName } from "./icons";
import { ErrorNotice } from "./ui";

export type CheckStatus = "ok" | "warn" | "fail" | "skip" | "info";

export interface CheckResult {
  id: string;
  title: string;
  status: CheckStatus;
  detail: string;
  // Только у проверки трафика сервера: адрес, который видят сайты за шлюзом.
  ip?: string;
}

// Адрес и сеть, которой он принадлежит; сеть неизвестна, если её не удалось узнать.
export interface Network {
  ip: string;
  asn: number | null;
  isp: string | null;
  countryCode: string | null;
  city: string | null;
  timeZone: string | null;
}

// Что браузер узнаёт о себе по нажатию кнопки.
export interface BrowserFacts {
  ipv4: Network | null;
  ipv6: Network | null;
  // Внешние адреса, которые раскрывает WebRTC, и локальные адреса сети; null — WebRTC выключен.
  webrtc: { public: Network[]; local: string[] } | null;
  // DNS-серверы, которые разрешали имена для этого браузера; null — проверка DNS не ответила.
  dns: Network[] | null;
  timeZone: string;
}

interface CheckRun {
  browser: CheckResult[];
  server: CheckResult[] | null;
  serverError: string | null;
  fingerprint: [string, string][];
  checkedAt: number;
}

// Внешние сервисы проверки. ipleak.net сообщает адрес и сеть по IPv4 и по IPv6, узнаёт сеть
// любого адреса и видит, какие DNS-серверы спрашивают у него уникальные имена его поддоменов.
// Content-Security-Policy панели разрешает ровно ipleak.net и его поддомены.
export const ipv4Service = "https://ipv4.ipleak.net/json/";
export const ipv6Service = "https://ipv6.ipleak.net/json/";
const lookupService = "https://ipleak.net/json/";
const stunServers = ["stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"];
// Публичные DNS-сервисы: они видят запросы, но это не интернет-провайдер владельца.
const publicDNS = /google|cloudflare|quad9|opendns|cisco|adguard|nextdns|control ?d|mullvad|yandex/i;
// Так выглядят IPv4- и IPv6-адреса; всё остальное в запрос к ipleak.net не подставляется.
const addressPattern = /^[0-9a-f:.]+$/i;

const sites = [
  {
    name: "ipleak.net",
    url: "https://ipleak.net/",
    checks: "IP, DNS, WebRTC и IPv6 на одной странице.",
    expect: "IP сервера; DNS-серверы не вашего провайдера; в WebRTC нет вашего адреса.",
  },
  {
    name: "BrowserLeaks",
    url: "https://browserleaks.com/webrtc",
    checks: "Подробно WebRTC, а в соседних разделах — отпечаток браузера.",
    expect: "Public IP пуст или это IP сервера; локальные адреса скрыты (.local).",
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
    expect: "IPv4 — адрес сервера; IPv6 не найден.",
  },
  {
    name: "Whoer",
    url: "https://whoer.net/",
    checks: "Сводка: IP, DNS, часовой пояс, язык, признаки прокси.",
    expect: "IP сервера; замечания о поясе и языке — подсказки, а не утечка.",
  },
  {
    name: "2ip.ru",
    url: "https://2ip.ru/",
    checks: "IP, провайдер и подозрение на прокси — по-русски.",
    expect: "IP и хостинг-провайдер сервера.",
  },
  {
    name: "IPinfo",
    url: "https://ipinfo.io/",
    checks: "Кому принадлежит адрес: страна, сеть, отметки VPN и hosting.",
    expect: "Сеть хостера сервера; отметка hosting для VPS — нормально.",
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

const labels: Record<CheckStatus, string> = {
  ok: "В порядке",
  warn: "Внимание",
  fail: "Проблема",
  skip: "Пропущено",
  info: "Сведения",
};
const marks: Record<CheckStatus, IconName> = {
  ok: "check",
  warn: "alert",
  fail: "close",
  skip: "minus",
  info: "info",
};
const badges: Record<CheckStatus, string> = {
  ok: "success",
  warn: "warning",
  fail: "danger",
  skip: "neutral",
  info: "neutral",
};

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

// Превращает увиденное браузером в результаты. Основной выход в интернет — по IPv4; утечка —
// адрес или DNS-сервер другой сети. serverIP — адрес шлюза, который видят сайты.
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
      status: "warn",
      detail: `Сайты видят ${main.ip} — ${owner(main)}. Это не ваш шлюз (${serverIP}): если сейчас включён другой VPN, так и должно быть, а если это ваш домашний провайдер — VPN на этом устройстве не работает.`,
    });
  } else {
    results.push({
      ...exit,
      status: "info",
      detail: `Сайты видят ${main.ip} — ${owner(main)}. Адрес шлюза узнать не удалось.`,
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
    results.push({ ...dns, status: "skip", detail: "Не удалось проверить: ipleak.net не ответил." });
  } else if (!facts.dns.length) {
    results.push({
      ...dns,
      status: "skip",
      detail: "ipleak.net не увидел DNS-запросов этого браузера — повторите проверку.",
    });
  } else {
    const role = (network: Network) =>
      main && sameNetwork(network, main) ? "сеть вашего выхода" : publicDNS.test(network.isp ?? "") ? "публичный DNS" : null;
    const owners = (networks: Network[]) =>
      [...new Set(networks.map((network) => owner(network) + (role(network) ? ` — ${role(network)}` : "")))].join("; ");
    const foreign = facts.dns.filter((network) => !role(network));
    results.push(
      foreign.length
        ? {
            ...dns,
            status: "warn",
            detail: `Среди DNS-серверов обнаружены ${owners(foreign)}. Они отличаются от сети выхода; по одному списку нельзя установить, шли ли запросы мимо прокси или VPN. Если здесь ваш домашний провайдер, проверьте маршрут DNS в настройках клиента.`,
          }
        : { ...dns, status: "ok", detail: `Ваши DNS-запросы видит ${owners(facts.dns)}, а не ваш провайдер.` },
    );
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
    return ip ? network(data, ip) : null;
  } catch {
    return null;
  }
}

async function lookup(ip: string, signal: AbortSignal): Promise<Network> {
  const unknown = { ip, asn: null, isp: null, countryCode: null, city: null, timeZone: null };
  if (!addressPattern.test(ip)) return unknown;
  try {
    return network(await outside<Lookup>(lookupService + ip, signal), ip);
  } catch {
    return unknown;
  }
}

// ipleak.net — авторитетный DNS-сервер своих поддоменов. Каждое имя ниже новое, поэтому DNS-сервер,
// которым пользуется браузер, спрашивает его у ipleak.net, а ответ на запрос называет всех, кто
// спрашивал имена этой сессии. null — ни одного ответа.
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
  let answered = false;
  for (const answer of answers) {
    if (!answer) continue;
    answered = true;
    if (answer.ip && typeof answer.ip === "object")
      for (const address of Object.keys(answer.ip)) if (addressPattern.test(address)) found.add(address);
  }
  return answered ? [...found] : null;
}

// Собирает кандидатов WebRTC так же, как это сделал бы сайт: STUN-сервер сообщает браузеру его
// внешний адрес. Имена mDNS (*.local) уже скрывают локальные адреса и пропускаются.
function webrtcAddresses(timeout = 4_000): Promise<{ public: string[]; local: string[] } | null> {
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
    const finish = () => {
      clearTimeout(timer);
      connection.close();
      resolve({ public: [...found.public], local: [...found.local] });
    };
    const timer = setTimeout(finish, timeout);
    connection.onicecandidate = (event) => {
      if (!event.candidate) return finish();
      const fields = event.candidate.candidate.split(" ");
      const candidate = fields[4];
      const type = fields[fields.indexOf("typ") + 1];
      if (!candidate || candidate.endsWith(".local")) return;
      (type === "host" ? found.local : found.public).add(candidate);
    };
    connection.createDataChannel("check");
    connection
      .createOffer()
      .then((offer) => connection.setLocalDescription(offer))
      .catch(finish);
  });
}

async function browserFacts(signal: AbortSignal): Promise<BrowserFacts> {
  const [ipv4, ipv6, webrtc, dns] = await Promise.all([
    exitNetwork(ipv4Service, signal),
    exitNetwork(ipv6Service, signal),
    webrtcAddresses(),
    dnsServers(signal),
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
    dns ? Promise.all(dns.slice(0, 12).map(find)) : null,
  ]);
  return {
    ipv4,
    ipv6,
    webrtc: webrtc && { public: rtc, local: webrtc.local.filter(isPrivateAddress) },
    dns: servers,
    timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  };
}

function fingerprint(): [string, string][] {
  const agent = navigator as Navigator & { userAgentData?: { platform?: string } };
  return [
    ["Языки", navigator.languages?.join(", ") || navigator.language || "—"],
    ["Система", agent.userAgentData?.platform || navigator.platform || "—"],
    ["Экран", `${window.screen.width} × ${window.screen.height}, плотность ${window.devicePixelRatio}`],
    ["Ядер процессора", navigator.hardwareConcurrency ? String(navigator.hardwareConcurrency) : "—"],
    ["Do Not Track", navigator.doNotTrack === "1" ? "включён" : "выключен"],
  ];
}

async function runChecks(token: string, nodeHost: string | undefined, signal: AbortSignal): Promise<CheckRun> {
  const [facts, server] = await Promise.all([
    browserFacts(signal),
    request<{ checks: CheckResult[] }>(token, "/checks", { signal }).then(
      (data) => ({ checks: data.checks, error: null }),
      (error: unknown) => ({ checks: null, error: errorMessage(error) }),
    ),
  ]);
  const trafficIP = server.checks?.find((check) => check.id === "traffic")?.ip;
  const serverIP = trafficIP || (nodeHost && isIPAddress(nodeHost) ? nodeHost : null);
  return {
    browser: browserVerdicts(facts, serverIP),
    server: server.checks,
    serverError: server.error,
    fingerprint: fingerprint(),
    checkedAt: Date.now(),
  };
}

function CheckList({ checks }: { checks: CheckResult[] }) {
  return (
    <ul className="check-list">
      {checks.map((check) => (
        <li key={check.id}>
          <span className={`check-mark ${check.status}`}>
            <Icon name={marks[check.status]} size={16} />
          </span>
          <div>
            <div className="check-head">
              <strong>{check.title}</strong>
              <span className={`badge ${badges[check.status]}`}>{labels[check.status]}</span>
            </div>
            <p>{check.detail}</p>
          </div>
        </li>
      ))}
    </ul>
  );
}

export function Checks({ token, nodeHost }: { token: string; nodeHost?: string }) {
  const run = useQuery({
    queryKey: ["checks"],
    queryFn: ({ signal }) => runChecks(token, nodeHost, signal),
    enabled: false,
    retry: false,
    staleTime: Infinity,
    gcTime: 30 * 60_000,
  });
  const result = run.isFetching ? undefined : run.data;
  const all = [...(result?.browser ?? []), ...(result?.server ?? [])];
  const problems = all.filter((check) => check.status === "fail").length;
  const warnings = all.filter((check) => check.status === "warn").length;
  const placeholder = (text: string) => <div className="loading">{run.isFetching ? "Проверяем…" : text}</div>;
  return (
    <>
      <section className="card check-run">
        <div className="check-run-text">
          <h2>Проверить сейчас</h2>
          <p>
            Браузер узнает, через чью сеть и какую страну он выходит в интернет по IPv4 и IPv6, чьи DNS-серверы
            видят его запросы и какие адреса раскрывает WebRTC. Другой внешний адрес в WebRTC может раскрыть
            обход прокси; сторонний DNS-сервер требует отдельной проверки маршрута. Сервер проверит себя и
            пропустит настоящий VLESS-трафик. Займёт до 20 секунд.
          </p>
          <p className="hint">
            Для этого браузер обратится к ipleak.net (его держит VPN-сервис AirVPN) и к STUN-серверам Google и
            Cloudflare — они увидят ваши адреса и DNS-серверы, как любой сайт проверки. Панель им ничего не
            передаёт.
          </p>
        </div>
        <button className="button primary" onClick={() => void run.refetch()} disabled={run.isFetching}>
          <Icon name="shield" size={18} />
          {run.isFetching ? "Проверяем…" : result ? "Проверить снова" : "Проверить"}
        </button>
        {result && (
          <div
            className={`notice check-summary ${problems ? "error" : warnings ? "warning-notice" : "success-notice"}`}
            role="status"
          >
            <Icon name={problems ? "close" : warnings ? "alert" : "check"} size={17} />
            <span>
              {problems
                ? `Найдено проблем: ${problems}${warnings ? `, замечаний: ${warnings}` : ""}. Подробности и что сделать — ниже.`
                : warnings
                  ? `Проблем нет, замечаний: ${warnings}. Посмотрите их ниже.`
                  : "Всё в порядке: вы выходите в интернет через свой шлюз, утечек не найдено."}{" "}
              Проверено в {new Date(result.checkedAt).toLocaleTimeString("ru-RU", { timeStyle: "short" })}.
            </span>
          </div>
        )}
        <ErrorNotice error={run.error} />
      </section>
      <div className="check-columns">
        <section className="card">
          <div className="card-heading">
            <div>
              <h2>Что видят сайты</h2>
              <p>Проверки этого браузера на этом устройстве.</p>
            </div>
            <Icon name="browser" size={23} />
          </div>
          {result ? <CheckList checks={result.browser} /> : placeholder("Нажмите «Проверить».")}
          {result && (
            <details className="check-fingerprint">
              <summary>Что ещё видит любой сайт</summary>
              <dl>
                {result.fingerprint.map(([term, value]) => (
                  <div key={term}>
                    <dt>{term}</dt>
                    <dd>{value}</dd>
                  </div>
                ))}
              </dl>
              <p className="hint">
                VPN этого не меняет. Вместе с часовым поясом и шрифтами это складывается в отпечаток, по
                которому сайты узнают браузер и без IP.
              </p>
            </details>
          )}
        </section>
        <section className="card">
          <div className="card-heading">
            <div>
              <h2>Сервер</h2>
              <p>Проверки, которые сервер выполняет сам.</p>
            </div>
            <Icon name="server" size={23} />
          </div>
          {result?.server ? (
            <CheckList checks={result.server} />
          ) : result?.serverError ? (
            <div className="card-body">
              <ErrorNotice error={new Error(result.serverError)} />
            </div>
          ) : (
            placeholder("Нажмите «Проверить».")
          )}
          <div className="card-footer">
            <Icon name="lock" size={15} />
            <span>
              Для проверки трафика сервер подключается к Xray с профилем самого старого активного устройства:
              его счётчик вырастет на несколько килобайт. Профиль никуда не передаётся.
            </span>
          </div>
        </section>
      </div>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>10 сайтов для проверки</h2>
            <p>Откройте с включённым VPN на устройстве, которое проверяете.</p>
          </div>
        </div>
        <ul className="check-sites">
          {sites.map((site) => (
            <li key={site.name}>
              <div>
                <a href={site.url} target="_blank" rel="noopener noreferrer">
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
      </section>
    </>
  );
}
