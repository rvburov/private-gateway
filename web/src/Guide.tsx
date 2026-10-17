import { Fragment, useState } from "react";
import { Icon } from "./icons";

// Инструкции для каждой системы владельца. Названия пунктов меню отличаются от версии к версии и
// от приложения к приложению, поэтому шаги называют настройку и её обычные имена. Проверенным
// помечено только то, что пробовали на настоящем устройстве; остальное следует возможностям приложений.
export type PlatformId = "android" | "ios" | "windows" | "macos" | "linux";
type ClientApp = { name: string; note: string; source: string; link?: string };
type Step = { title: string; text: string };
type Platform = {
  id: PlatformId;
  label: string;
  checked?: string;
  // Тройка по популярности и надёжности: первым — самый распространённый и проверенный временем.
  top: ClientApp[];
  // Тоже подходят, без места в рейтинге.
  others: ClientApp[];
  steps: Step[];
};

const links = {
  v2rayNG: "https://github.com/2dust/v2rayNG/releases",
  v2rayN: "https://github.com/2dust/v2rayN/releases",
  happAndroid: "https://github.com/Happ-proxy/happ-android/releases",
  happDesktop: "https://github.com/Happ-proxy/happ-desktop/releases",
  hiddify: "https://github.com/hiddify/hiddify-app/releases",
  throne: "https://github.com/throneproj/Throne/releases",
  clashVerge: "https://github.com/clash-verge-rev/clash-verge-rev/releases",
  clashAndroid: "https://github.com/MetaCubeX/ClashMetaForAndroid/releases",
  nekoBox: "https://github.com/MatsuriDayo/NekoBoxForAndroid/releases",
  karing: "https://github.com/KaringX/karing/releases",
  singBox: "https://sing-box.sagernet.org/clients/",
};

const linkStep = {
  title: "Возьмите ссылку-подписку",
  text: "В панели: «Устройства» → «Профиль» нужного устройства → «Скопировать ссылку». Для каждого телефона и компьютера заведите своё устройство в панели: тогда его можно отозвать отдельно.",
};
const phoneSubscriptionStep = {
  title: "Добавьте подписку",
  text: "Откройте ссылку в браузере на телефоне: страница предложит кнопки импорта в популярные приложения. Или скопируйте ссылку и в приложении нажмите «+» → «Импорт из буфера обмена» (в некоторых — «Подписки» → «Добавить»), затем обновите подписку.",
};
const desktopSubscriptionStep = {
  title: "Добавьте подписку",
  text: "Скопируйте ссылку и добавьте подписку в приложении: «+» → «Импорт из буфера обмена» или «Подписки» → «Добавить», затем «Обновить подписку». Если приложению нужен файл, в окне профиля есть форматы sing-box и Mihomo.",
};
const allTrafficStep = {
  title: "Весь трафик через сервер",
  text: "В настройках маршрутизации выберите вариант без обхода: «Global», «Весь трафик» или регион «Other». Иначе сайты вашей страны открываются напрямую и видят ваш настоящий IP.",
};
const ipv6Step = {
  title: "Отключите IPv6",
  text: "Выключите IPv6 в настройках приложения, а если такого пункта нет — в системе: сервер работает только по IPv4, и IPv6 может пойти мимо VPN.",
};
const checkStep = {
  title: "Проверьте подключение",
  text: "Включите VPN, откройте на этом устройстве панель, раздел «Проверка», и нажмите «Проверить»: вы должны выходить в интернет через свой шлюз, а IPv6, WebRTC и DNS — не показывать вашего провайдера.",
};
const desktopKillSwitchStep = {
  title: "Kill switch",
  text: "У большинства клиентов полноценного kill switch нет: при обрыве VPN программы выходят в интернет напрямую. Держите режим TUN включённым и следите за значком подключения.",
};

export const platforms: Platform[] = [
  {
    id: "android",
    label: "Android",
    top: [
      { name: "v2rayNG", note: "Самый распространённый клиент на ядре Xray — том же, что на сервере. Бесплатный, открытый код.", source: "GitHub", link: links.v2rayNG },
      { name: "Happ", note: "Очень популярный и простой, ядро Xray. Бесплатный, код закрыт.", source: "Google Play или GitHub", link: links.happAndroid },
      { name: "Hiddify", note: "Понятный интерфейс, ядро sing-box. Бесплатный, открытый код.", source: "Google Play или GitHub", link: links.hiddify },
    ],
    others: [
      { name: "v2RayTun", note: "Google Play", source: "Google Play" },
      { name: "NekoBox", note: "только с GitHub: в Google Play чужая сборка", source: "GitHub", link: links.nekoBox },
      { name: "Clash Meta for Android", note: "формат Mihomo", source: "GitHub", link: links.clashAndroid },
      { name: "Karing", note: "ядро sing-box", source: "GitHub", link: links.karing },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Поставьте одно из трёх приложений выше — с GitHub или из Google Play от его разработчика." },
      phoneSubscriptionStep,
      {
        title: "Режим VPN, а не прокси",
        text: "При первом подключении разрешите приложению создать VPN. Если есть выбор «Прокси» или «VPN», выберите VPN: тогда через сервер идут все приложения, а не только браузер.",
      },
      allTrafficStep,
      ipv6Step,
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
    top: [
      { name: "Happ", note: "Очень популярный и простой, ядро Xray, импорт подписки по кнопке. Бесплатный.", source: "App Store" },
      { name: "Hiddify", note: "Бесплатный, открытый код. Проверен с этим сервером на iPhone по LTE.", source: "App Store" },
      { name: "Streisand", note: "Бесплатный, давно популярный клиент с подписками.", source: "App Store" },
    ],
    others: [
      { name: "Shadowrocket", note: "платный", source: "App Store" },
      { name: "V2Box", note: "бесплатный, с рекламой", source: "App Store" },
      { name: "Karing", note: "ядро sing-box", source: "App Store" },
    ],
    steps: [
      linkStep,
      {
        title: "Установите приложение",
        text: "Установите одно из трёх приложений выше из App Store. В российском App Store многих VPN-клиентов нет — понадобится Apple ID другой страны.",
      },
      phoneSubscriptionStep,
      { title: "Разрешите VPN", text: "При первом подключении iOS попросит добавить конфигурацию VPN — разрешите." },
      allTrafficStep,
      ipv6Step,
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
    top: [
      { name: "v2rayN", note: "Самый популярный клиент для Windows: ядро Xray или sing-box, режим TUN. Бесплатный, открытый код.", source: "GitHub", link: links.v2rayN },
      { name: "Happ", note: "Простой, ядро Xray. Бесплатный, код закрыт.", source: "GitHub", link: links.happDesktop },
      { name: "Hiddify", note: "Понятный интерфейс, ядро sing-box. Бесплатный, открытый код.", source: "GitHub", link: links.hiddify },
    ],
    others: [
      { name: "Throne", note: "бывший NekoRay", source: "GitHub", link: links.throne },
      { name: "Clash Verge Rev", note: "формат Mihomo", source: "GitHub", link: links.clashVerge },
      { name: "Karing", note: "ядро sing-box", source: "GitHub", link: links.karing },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Скачайте одно из трёх приложений выше со страницы выпусков на GitHub." },
      desktopSubscriptionStep,
      {
        title: "Режим VPN (TUN)",
        text: "Включите режим TUN — в разных приложениях он называется «TUN», «Режим TUN» или «VPN»; Windows попросит права администратора. В режиме системного прокси через сервер идут только программы, которые используют прокси.",
      },
      allTrafficStep,
      ipv6Step,
      desktopKillSwitchStep,
      checkStep,
    ],
  },
  {
    id: "macos",
    label: "macOS",
    top: [
      { name: "Happ", note: "Простой, ядро Xray. Бесплатный, код закрыт.", source: "App Store или GitHub", link: links.happDesktop },
      { name: "Hiddify", note: "Понятный интерфейс, ядро sing-box. Бесплатный, открытый код.", source: "App Store или GitHub", link: links.hiddify },
      { name: "v2rayN", note: "Ядро Xray или sing-box, открытый код; интерфейс сложнее, чем у первых двух.", source: "GitHub", link: links.v2rayN },
    ],
    others: [
      { name: "V2Box", note: "App Store", source: "App Store" },
      { name: "Streisand", note: "App Store, только Mac на Apple Silicon", source: "App Store" },
      { name: "Clash Verge Rev", note: "формат Mihomo", source: "GitHub", link: links.clashVerge },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Установите одно из трёх приложений выше из App Store или со страницы выпусков на GitHub." },
      desktopSubscriptionStep,
      {
        title: "Режим VPN (TUN)",
        text: "Включите режим VPN (TUN). macOS попросит разрешить сетевое расширение — разрешите в «Системных настройках».",
      },
      allTrafficStep,
      ipv6Step,
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
    top: [
      { name: "v2rayN", note: "Ядро Xray или sing-box, открытый код; пакеты для x64 и arm64.", source: "GitHub", link: links.v2rayN },
      { name: "Hiddify", note: "AppImage или deb, режим VPN. Открытый код.", source: "GitHub", link: links.hiddify },
      { name: "Throne", note: "Бывший NekoRay: режим TUN, открытый код.", source: "GitHub", link: links.throne },
    ],
    others: [
      { name: "Happ", note: "deb, rpm", source: "GitHub", link: links.happDesktop },
      { name: "Clash Verge Rev", note: "формат Mihomo", source: "GitHub", link: links.clashVerge },
      { name: "sing-box", note: "консольный; JSON из профиля работает как локальный прокси", source: "sing-box.sagernet.org", link: links.singBox },
    ],
    steps: [
      linkStep,
      { title: "Установите приложение", text: "Установите одно из трёх приложений выше: deb, rpm или AppImage со страницы выпусков на GitHub." },
      desktopSubscriptionStep,
      {
        title: "Режим VPN (TUN)",
        text: "Включите режим TUN; для него нужны права администратора, и приложение их запросит. Без него через сервер идут только программы с настроенным прокси.",
      },
      allTrafficStep,
      ipv6Step,
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

function Source({ app }: { app: ClientApp }) {
  return app.link ? (
    <a className="text-button" href={app.link} target="_blank" rel="noopener noreferrer">
      {app.source}
      <Icon name="external" size={15} />
    </a>
  ) : (
    <span className="secondary small">{app.source}</span>
  );
}

export function Guide({ onCheck }: { onCheck?: () => void }) {
  const [platformId, setPlatformId] = useState<PlatformId>(() => detectPlatform());
  const platform = platforms.find((item) => item.id === platformId) ?? platforms[0];
  return (
    <>
      <section className="card">
        <div className="card-heading">
          <div>
            <h2>Перед началом</h2>
            <p>Что делает подключение надёжным.</p>
          </div>
          <Icon name="book" size={23} />
        </div>
        <ul className="guide-points">
          <li>
            Подойдёт любое приложение с поддержкой VLESS, REALITY и Vision. Ниже для каждой системы — тройка самых
            популярных и надёжных.
          </li>
          <li>Одно устройство в панели — один телефон или компьютер. Тогда потерянное устройство отключается отдельно.</li>
          <li>После добавления устройства нажмите «Применить конфигурацию» на «Обзоре».</li>
          <li>Названия пунктов в приложениях зависят от приложения и его версии: ищите похожие слова.</li>
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
          <h3 className="guide-subtitle">Приложения: тройка по популярности и надёжности</h3>
          <ol className="guide-apps" aria-label={`Приложения для ${platform.label}`}>
            {platform.top.map((app, index) => (
              <li key={app.name}>
                <span className="guide-rank" aria-hidden="true">
                  {index + 1}
                </span>
                <div>
                  <strong>{app.name}</strong>
                  <span>{app.note}</span>
                </div>
                <Source app={app} />
              </li>
            ))}
          </ol>
          <p className="guide-others">
            Тоже подходят:{" "}
            {platform.others.map((app, index) => (
              <Fragment key={app.name}>
                {index > 0 && ", "}
                {app.link ? (
                  <a href={app.link} target="_blank" rel="noopener noreferrer">
                    {app.name}
                  </a>
                ) : (
                  app.name
                )}{" "}
                ({app.note})
              </Fragment>
            ))}
            .
          </p>
          <h3 className="guide-subtitle">Шаги</h3>
          <ol className="guide-steps">
            {platform.steps.map((step) => (
              <li key={step.title}>
                <strong>{step.title}</strong>
                <p>{step.text}</p>
              </li>
            ))}
          </ol>
          {onCheck && (
            <div className="button-group">
              <button type="button" className="button secondary-button" onClick={onCheck}>
                <Icon name="shield" size={18} />
                Перейти к проверке
              </button>
            </div>
          )}
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
