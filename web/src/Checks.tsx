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
  // The traffic check of the server: the address sites see behind the gateway.
  ip?: string;
}

export interface Geo {
  ip?: string;
  city?: string;
  country?: string;
  org?: string;
  timezone?: string;
}

// What the browser learns about itself when the owner presses the button.
export interface BrowserFacts {
  ipv4: string | null;
  ipv6: string | null;
  geo: Geo | null;
  // Addresses from WebRTC candidates; null when the browser has WebRTC off.
  webrtc: { public: string[]; local: string[] } | null;
  timeZone: string;
}

interface CheckRun {
  browser: CheckResult[];
  server: CheckResult[] | null;
  serverError: string | null;
  fingerprint: [string, string][];
  checkedAt: number;
}

// The outside services of the check; the API's Content-Security-Policy allows exactly these.
export const ipv4Service = "https://api.ipify.org?format=json";
export const ipv6Service = "https://api6.ipify.org?format=json";
export const geoService = "https://ipinfo.io/json";
const stunServers = ["stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"];

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

// Addresses that mean nothing outside the device's own network.
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

function countryName(code?: string) {
  if (!code) return "";
  try {
    return new Intl.DisplayNames(["ru"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

// Turns what the browser saw into results. serverIP is the address sites see behind the gateway.
export function browserVerdicts(facts: BrowserFacts, serverIP: string | null, now = new Date()): CheckResult[] {
  const results: CheckResult[] = [];
  const ip = facts.ipv4;
  if (!ip) {
    results.push({
      id: "ip",
      title: "IP-адрес для сайтов",
      status: "warn",
      detail: "Не удалось узнать адрес: api.ipify.org не ответил. Проверьте вручную на ipleak.net из списка ниже.",
    });
  } else if (!serverIP) {
    results.push({
      id: "ip",
      title: "IP-адрес для сайтов",
      status: "info",
      detail: `Сайты видят ${ip}. Адрес сервера узнать не удалось — сравните сами с адресом в разделе «Сервер».`,
    });
  } else if (same(ip, serverIP)) {
    results.push({
      id: "ip",
      title: "IP-адрес для сайтов",
      status: "ok",
      detail: `Сайты видят ${ip} — это IP сервера: браузер выходит в интернет через шлюз.`,
    });
  } else {
    results.push({
      id: "ip",
      title: "IP-адрес для сайтов",
      status: "fail",
      detail: `Сайты видят ${ip}, а IP сервера — ${serverIP}: этот браузер выходит в интернет не через шлюз. Включите VPN и проверьте, что он пропускает через туннель весь трафик.`,
    });
  }

  results.push(
    facts.ipv6
      ? {
          id: "ipv6",
          title: "IPv6",
          status: "fail",
          detail: `Сайты видят IPv6-адрес ${facts.ipv6} — он идёт мимо VPN: шлюз работает только по IPv4. Отключите IPv6 в приложении VPN или в системе — шаг «IPv6» в «Инструкциях».`,
        }
      : {
          id: "ipv6",
          title: "IPv6",
          status: "ok",
          detail: "Сайты не видят IPv6-адреса. Шлюз работает по IPv4, так и должно быть.",
        },
  );

  const webrtc = facts.webrtc;
  if (!webrtc) {
    results.push({
      id: "webrtc",
      title: "WebRTC",
      status: "ok",
      detail: "WebRTC в этом браузере выключен — через него адрес не утечёт.",
    });
  } else {
    const local = webrtc.local.filter(isPrivateAddress);
    const leaked = [...webrtc.public, ...webrtc.local.filter((address) => !isPrivateAddress(address))].filter(
      (address) => !same(address, serverIP) && !same(address, ip),
    );
    if (leaked.length) {
      results.push({
        id: "webrtc",
        title: "WebRTC",
        status: "fail",
        detail: `WebRTC раскрывает ${leaked.join(", ")} — сайты узнают этот адрес в обход VPN. Ограничьте WebRTC в браузере (раздел «Браузеры» в «Инструкциях») или включите в приложении VPN режим TUN.`,
      });
    } else if (local.length) {
      results.push({
        id: "webrtc",
        title: "WebRTC",
        status: "warn",
        detail: `WebRTC показывает адрес в вашей локальной сети: ${local.join(", ")}. Внешний IP он не раскрывает, но делает браузер заметнее.`,
      });
    } else {
      results.push({
        id: "webrtc",
        title: "WebRTC",
        status: "ok",
        detail: webrtc.public.length
          ? "WebRTC показывает только адрес, который сайты и так видят."
          : "WebRTC не раскрывает ваших адресов.",
      });
    }
  }

  const geo = facts.geo;
  if (!geo) {
    results.push({
      id: "location",
      title: "Страна и провайдер",
      status: "skip",
      detail: "Не удалось узнать: ipinfo.io не ответил.",
    });
  } else {
    const place = [countryName(geo.country), geo.city].filter(Boolean).join(", ") || "Место не определено";
    const network = geo.org ? ` · ${geo.org}` : "";
    results.push(
      same(geo.ip, serverIP)
        ? {
            id: "location",
            title: "Страна и провайдер",
            status: "ok",
            detail: `${place}${network} — по адресу сервера сайты считают, что вы там.`,
          }
        : {
            id: "location",
            title: "Страна и провайдер",
            status: "info",
            detail: `${place}${network} — так сайты определяют ваше место по адресу ${geo.ip ?? "—"}.`,
          },
    );
  }

  const ipZone = geo?.timezone;
  const browserOffset = utcOffset(facts.timeZone, now);
  const ipOffset = ipZone ? utcOffset(ipZone, now) : null;
  if (!ipZone) {
    results.push({
      id: "timezone",
      title: "Часовой пояс",
      status: "info",
      detail: `Часовой пояс браузера — ${facts.timeZone}. Пояс IP узнать не удалось.`,
    });
  } else if (ipZone === facts.timeZone || (browserOffset !== null && browserOffset === ipOffset)) {
    results.push({
      id: "timezone",
      title: "Часовой пояс",
      status: "ok",
      detail: `Пояс браузера ${facts.timeZone} совпадает по времени с поясом IP (${ipZone}).`,
    });
  } else {
    results.push({
      id: "timezone",
      title: "Часовой пояс",
      status: "warn",
      detail: `Пояс браузера — ${facts.timeZone}${browserOffset ? ` (${browserOffset})` : ""}, а IP — из ${ipZone}${ipOffset ? ` (${ipOffset})` : ""}. Это не утечка адреса, но по несовпадению сайты могут понять, что вы за VPN.`,
    });
  }

  results.push({
    id: "dns",
    title: "DNS",
    status: "info",
    detail: "Из браузера DNS не проверить. Откройте DNS Leak Test из списка ниже и нажмите «Extended test»: в ответе не должно быть серверов вашего домашнего или мобильного провайдера.",
  });
  return results;
}

async function outside<T>(url: string, signal: AbortSignal): Promise<T> {
  // No cookies and no referrer: the service learns only the address, as any site would.
  const response = await fetch(url, {
    credentials: "omit",
    referrerPolicy: "no-referrer",
    cache: "no-store",
    signal: AbortSignal.any([signal, AbortSignal.timeout(8_000)]),
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return (await response.json()) as T;
}

async function address(url: string, signal: AbortSignal) {
  try {
    const data = await outside<{ ip?: unknown }>(url, signal);
    return typeof data.ip === "string" && data.ip ? data.ip : null;
  } catch {
    return null;
  }
}

// Collects WebRTC candidates the way a site would: a STUN server tells the browser its outside
// address. mDNS names (*.local) already hide local addresses and are skipped.
function webrtcAddresses(timeout = 4_000): Promise<BrowserFacts["webrtc"]> {
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
  const [ipv4, ipv6, geo, webrtc] = await Promise.all([
    address(ipv4Service, signal),
    address(ipv6Service, signal),
    outside<Geo>(geoService, signal).catch(() => null),
    webrtcAddresses(),
  ]);
  return { ipv4, ipv6, geo, webrtc, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone };
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
            Браузер узнает, какой адрес и место видят сайты и нет ли утечек через IPv6 и WebRTC, а сервер
            проверит себя и пропустит настоящий VLESS-трафик. Займёт до 20 секунд.
          </p>
          <p className="hint">
            Для этого браузер обратится к api.ipify.org, ipinfo.io и STUN-серверам Google и Cloudflare — они
            увидят тот же адрес, что и любой сайт. Панель им ничего не передаёт.
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
                  : "Всё в порядке: сайты видят IP сервера, утечек не найдено."}{" "}
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
