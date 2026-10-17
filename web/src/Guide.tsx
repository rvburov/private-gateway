import { useState } from "react";
import { Icon } from "./icons";

// Setup instructions for every system the owner uses. Menu names differ between app versions, so
// steps name the setting and say where it usually is. Only what was tried on a real device is
// marked as checked; everything else follows the app's documented features.
export type PlatformId = "android" | "ios" | "windows" | "macos" | "linux";
type ClientApp = { name: string; note: string; source: string; link?: string };
type Step = { title: string; text: string };
type Platform = { id: PlatformId; label: string; checked?: string; apps: ClientApp[]; steps: Step[] };

const linkStep = {
  title: "Возьмите ссылку-подписку",
  text: "В панели: «Устройства» → «Профиль» нужного устройства → «Скопировать ссылку». Для каждого телефона и компьютера заведите своё устройство в панели: тогда его можно отозвать отдельно.",
};
const checkStep = {
  title: "Проверьте подключение",
  text: "Включите VPN и откройте ipleak.net: сайты должны видеть IP сервера, IPv6 — не ваш или никакой, WebRTC — без вашего адреса, DNS — не вашего провайдера.",
};
const hiddify = "https://github.com/hiddify/hiddify-app/releases";

export const platforms: Platform[] = [
  {
    id: "android",
    label: "Android",
    apps: [
      { name: "Hiddify", note: "Рекомендуем: подписка, режим VPN и настройки в одном месте.", source: "Google Play или GitHub", link: hiddify },
      { name: "v2rayNG", note: "Клиент на ядре Xray.", source: "Google Play или GitHub", link: "https://github.com/2dust/v2rayNG/releases" },
      { name: "NekoBox", note: "Клиент на ядре sing-box.", source: "GitHub", link: "https://github.com/MatsuriDayo/NekoBoxForAndroid/releases" },
      { name: "Clash Meta for Android", note: "Если нужен формат Mihomo (Clash).", source: "GitHub", link: "https://github.com/MetaCubeX/ClashMetaForAndroid/releases" },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Hiddify — из Google Play или с GitHub." },
      {
        title: "Добавьте подписку",
        text: "Откройте ссылку на телефоне в браузере и нажмите «Открыть в Hiddify». Или скопируйте её и в Hiddify нажмите «+» → «Добавить из буфера обмена». В v2rayNG ссылку добавляют в настройках группы подписок, затем нажимают «Обновить подписку».",
      },
      {
        title: "Режим VPN, а не прокси",
        text: "В настройках Hiddify выберите режим работы «VPN» (Service mode). Тогда через сервер идут все приложения, а не только браузер. v2rayNG работает как VPN по умолчанию.",
      },
      {
        title: "Весь трафик через сервер",
        text: "В Hiddify выберите регион «Other»: иначе местные сайты открываются напрямую и видят ваш настоящий IP. В v2rayNG выберите маршрутизацию без обхода — «Global».",
      },
      { title: "Отключите IPv6", text: "В настройках приложения выключите IPv6: сервер работает только по IPv4." },
      {
        title: "Включите kill switch",
        text: "Настройки Android → «Сеть и интернет» → «VPN» → шестерёнка у приложения → «Постоянная VPN» и «Блокировать подключения без VPN». Если VPN отключится, интернет не пойдёт напрямую.",
      },
      checkStep,
    ],
  },
  {
    id: "ios",
    label: "iPhone и iPad",
    checked: "Проверено на iPhone с Hiddify по LTE: сайты видят IP сервера, WebRTC — без вашего адреса.",
    apps: [
      { name: "Hiddify", note: "Рекомендуем; проверен с этим сервером.", source: "App Store" },
      { name: "Streisand", note: "Бесплатный клиент с поддержкой подписок.", source: "App Store" },
      { name: "Shadowrocket", note: "Платный клиент с поддержкой подписок.", source: "App Store" },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Hiddify — из App Store." },
      {
        title: "Добавьте подписку",
        text: "Откройте ссылку в Safari и нажмите «Открыть в Hiddify». Или скопируйте её и в Hiddify нажмите «+» → «Добавить из буфера обмена». При первом подключении разрешите добавить конфигурацию VPN.",
      },
      { title: "Весь трафик через сервер", text: "В настройках Hiddify выберите регион «Other», чтобы местные сайты не открывались напрямую." },
      { title: "Отключите IPv6", text: "В настройках Hiddify выключите IPv6: сервер работает только по IPv4." },
      {
        title: "Kill switch",
        text: "Системного переключателя в iOS нет. Если VPN отключился, трафик идёт напрямую: следите за значком VPN в строке состояния и включайте подключение снова.",
      },
      checkStep,
    ],
  },
  {
    id: "windows",
    label: "Windows",
    apps: [
      { name: "Hiddify", note: "Рекомендуем: простой интерфейс и режим VPN.", source: "GitHub", link: hiddify },
      { name: "v2rayN", note: "Клиент на ядре Xray с режимом TUN.", source: "GitHub", link: "https://github.com/2dust/v2rayN/releases" },
      { name: "Clash Verge Rev", note: "Если нужен формат Mihomo (Clash).", source: "GitHub", link: "https://github.com/clash-verge-rev/clash-verge-rev/releases" },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Hiddify или v2rayN — со страницы выпусков на GitHub." },
      {
        title: "Добавьте подписку",
        text: "Hiddify: «+» → «Добавить из буфера обмена». v2rayN: «Подписки» → «Настройки подписок» → «Добавить», вставьте ссылку, затем «Обновить подписки».",
      },
      {
        title: "Режим VPN (TUN)",
        text: "Hiddify: режим работы «VPN» — Windows попросит права администратора. v2rayN: включите «Режим TUN» внизу окна. Без этого через сервер идут только программы, которые используют системный прокси.",
      },
      { title: "Весь трафик через сервер", text: "Hiddify: регион «Other». v2rayN: маршрутизация «Global» (без обхода)." },
      { title: "Отключите IPv6", text: "Выключите IPv6 в настройках приложения: сервер работает только по IPv4." },
      {
        title: "Kill switch",
        text: "Полноценного kill switch в этих приложениях нет: при обрыве VPN программы выходят напрямую. Держите режим VPN включённым и проверяйте значок подключения.",
      },
      checkStep,
    ],
  },
  {
    id: "macos",
    label: "macOS",
    apps: [
      { name: "Hiddify", note: "Рекомендуем: режим VPN и подписки.", source: "App Store или GitHub", link: hiddify },
      { name: "Clash Verge Rev", note: "Если нужен формат Mihomo (Clash).", source: "GitHub", link: "https://github.com/clash-verge-rev/clash-verge-rev/releases" },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Hiddify — из App Store или с GitHub." },
      { title: "Добавьте подписку", text: "В Hiddify: «+» → «Добавить из буфера обмена»." },
      {
        title: "Режим VPN",
        text: "Выберите режим работы «VPN». macOS попросит разрешить сетевое расширение — разрешите в «Системных настройках».",
      },
      { title: "Весь трафик через сервер", text: "В Hiddify выберите регион «Other»." },
      { title: "Отключите IPv6", text: "Выключите IPv6 в настройках приложения." },
      {
        title: "Kill switch",
        text: "Системного переключателя нет: при обрыве VPN трафик идёт напрямую. Следите за значком подключения в строке меню.",
      },
      checkStep,
    ],
  },
  {
    id: "linux",
    label: "Linux",
    apps: [
      { name: "Hiddify", note: "AppImage или deb, режим VPN.", source: "GitHub", link: hiddify },
      { name: "Clash Verge Rev", note: "deb, rpm или AppImage, формат Mihomo.", source: "GitHub", link: "https://github.com/clash-verge-rev/clash-verge-rev/releases" },
      { name: "sing-box", note: "Консольный клиент: JSON из профиля в панели работает как локальный прокси.", source: "sing-box.sagernet.org", link: "https://sing-box.sagernet.org/clients/" },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Hiddify — AppImage или deb-пакет с GitHub." },
      { title: "Добавьте подписку", text: "В Hiddify: «+» → «Добавить из буфера обмена»." },
      {
        title: "Режим VPN (TUN)",
        text: "Выберите режим «VPN»; для него нужны права администратора, и приложение их запросит. Без него через сервер идут только программы с настроенным прокси.",
      },
      { title: "Весь трафик через сервер", text: "В Hiddify выберите регион «Other»." },
      { title: "Отключите IPv6", text: "Выключите IPv6 в настройках приложения." },
      {
        title: "Kill switch",
        text: "Встроенного нет. Продвинутый вариант — запретить исходящий трафик мимо VPN правилами файрвола.",
      },
      checkStep,
    ],
  },
];

const browsers: { name: string; items: [string, string][] }[] = [
  {
    name: "Chrome, Edge, Яндекс Браузер",
    items: [
      [
        "WebRTC",
        "В режиме VPN (TUN) WebRTC показывает адрес сервера — так и должно быть. Встроенной настройки нет; если в режиме прокси адрес утекает, пользуйтесь Firefox или Brave.",
      ],
      ["Геолокация", "Настройки → «Конфиденциальность и безопасность» → «Настройки сайтов» → «Геоданные» → запретить сайтам доступ."],
      ["Аккаунты и cookies", "Для приватного — окно инкогнито или отдельный профиль без входа в аккаунты Google, Яндекса и других."],
    ],
  },
  {
    name: "Firefox",
    items: [
      ["WebRTC", "about:config → media.peerconnection.enabled = false. Звонки в браузере после этого не работают."],
      [
        "Отпечаток и часовой пояс",
        "about:config → privacy.resistFingerprinting = true: Firefox скрывает многие признаки браузера и показывает часовой пояс UTC. Часть сайтов может работать хуже.",
      ],
      ["Геолокация", "Настройки → «Приватность и защита» → «Разрешения» → «Местоположение» → блокировать новые запросы."],
    ],
  },
  {
    name: "Safari (iPhone, iPad, Mac)",
    items: [
      ["WebRTC", "По умолчанию не раскрывает ваши адреса; отдельной настройки не нужно."],
      [
        "Геолокация",
        "iPhone: «Настройки» → «Конфиденциальность» → «Службы геолокации» → Safari → «Никогда». Mac: Safari → «Настройки» → «Веб-сайты» → «Геопозиция» → «Отклонять».",
      ],
      ["Трекеры", "Включите «Скрывать IP-адрес от трекеров» и защиту от межсайтового отслеживания."],
    ],
  },
  {
    name: "Brave",
    items: [
      ["WebRTC", "Настройки → «Конфиденциальность и безопасность» → WebRTC IP handling policy → «Disable non-proxied UDP»."],
      ["Защита", "Brave Shields блокирует трекеры и защищает от отпечатка по умолчанию — не выключайте."],
    ],
  },
  {
    name: "Tor Browser",
    items: [
      [
        "Когда нужен",
        "Для настоящей анонимности: тысячи пользователей выглядят одинаково, а IP меняется. Работает и поверх этого VPN, и без него.",
      ],
    ],
  },
];

const exposures: [string, string][] = [
  ["Аккаунты и cookies", "Вошли в аккаунт — сайт знает, что это вы, с любым IP."],
  ["Отпечаток браузера", "Модель устройства, система, экран, шрифты и видеокарта часто узнают вас и без входа."],
  ["Часовой пояс и язык", "Московское время и русский язык при иностранном IP сайт заметит."],
  ["Геолокация", "GPS, Wi-Fi-сети и вышки связи выдают место в обход VPN — не давайте доступ сайтам и приложениям."],
  ["Приложения на телефоне", "Видят рекламный ID, модель, оператора и страну SIM-карты, номер при входе по SMS и то, что VPN включён."],
  ["IP дата-центра", "Многие сайты понимают, что вы через VPN, и могут показать капчу или ограничить вход."],
];

export function detectPlatform(agent = navigator.userAgent, touchPoints = navigator.maxTouchPoints): PlatformId {
  if (/android/i.test(agent)) return "android";
  if (/iphone|ipad|ipod/i.test(agent) || (/macintosh/i.test(agent) && touchPoints > 1)) return "ios";
  if (/windows/i.test(agent)) return "windows";
  if (/macintosh|mac os x/i.test(agent)) return "macos";
  if (/linux|x11|cros/i.test(agent)) return "linux";
  return "windows";
}

export function Guide() {
  const [platformId, setPlatformId] = useState<PlatformId>(() => detectPlatform());
  const platform = platforms.find((item) => item.id === platformId) ?? platforms[0];
  return (
    <>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Перед началом</h2>
            <p>Три вещи, которые делают подключение надёжным.</p>
          </div>
          <Icon name="book" size={23} />
        </div>
        <ul className="guide-points">
          <li>Одно устройство в панели — один телефон или компьютер. Тогда потерянное устройство отключается отдельно.</li>
          <li>После добавления устройства нажмите «Применить конфигурацию» на «Обзоре».</li>
          <li>Названия пунктов в приложениях зависят от их версии: ищите похожие слова.</li>
        </ul>
      </section>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Подключение</h2>
            <p>Выберите систему устройства, которое подключаете.</p>
          </div>
        </div>
        <div className="card-body">
          <div className="choice-pills" role="group" aria-label="Система устройства">
            {platforms.map((item) => (
              <button
                key={item.id}
                type="button"
                aria-pressed={item.id === platform.id}
                onClick={() => setPlatformId(item.id)}
              >
                {item.label}
              </button>
            ))}
          </div>
          <div className={`notice ${platform.checked ? "success-notice" : ""}`}>
            <Icon name={platform.checked ? "check" : "lock"} size={17} />
            {platform.checked ?? `На ${platform.label} с этим сервером ещё не проверяли: шаги основаны на возможностях приложений.`}
          </div>
          <h3 className="guide-subtitle">Приложения</h3>
          <ul className="guide-apps">
            {platform.apps.map((app) => (
              <li key={app.name}>
                <div>
                  <strong>{app.name}</strong>
                  <span>{app.note}</span>
                </div>
                {app.link ? (
                  <a className="text-button" href={app.link} target="_blank" rel="noopener noreferrer">
                    {app.source}
                    <Icon name="external" size={15} />
                  </a>
                ) : (
                  <span className="secondary small">{app.source}</span>
                )}
              </li>
            ))}
          </ul>
          <h3 className="guide-subtitle">Шаги</h3>
          <ol className="guide-steps">
            {platform.steps.map((step) => (
              <li key={step.title}>
                <strong>{step.title}</strong>
                <p>{step.text}</p>
              </li>
            ))}
          </ol>
        </div>
      </section>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Браузеры</h2>
            <p>Что настроить, чтобы браузер не выдавал лишнего.</p>
          </div>
        </div>
        <div className="guide-browsers">
          {browsers.map((browser) => (
            <details key={browser.name}>
              <summary>{browser.name}</summary>
              <dl>
                {browser.items.map(([term, text]) => (
                  <div key={term}>
                    <dt>{term}</dt>
                    <dd>{text}</dd>
                  </div>
                ))}
              </dl>
            </details>
          ))}
        </div>
      </section>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Что может выдать вас и с VPN</h2>
            <p>VPN меняет IP и прячет трафик от провайдера. Остальное собирается в обход него.</p>
          </div>
        </div>
        <dl className="guide-exposures">
          {exposures.map(([term, text]) => (
            <div key={term}>
              <dt>{term}</dt>
              <dd>{text}</dd>
            </div>
          ))}
        </dl>
        <div className="card-footer">
          <Icon name="lock" size={15} />
          <span>
            Этот VPN даёт смену IP и защиту от провайдера, но не анонимность: хостер знает, кто арендовал
            сервер, а IP принадлежит только вам.
          </span>
        </div>
      </section>
    </>
  );
}
