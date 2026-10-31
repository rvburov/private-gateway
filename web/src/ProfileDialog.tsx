import { useState } from "react";
import { request, type Device, type ProfileExport } from "./api";
import { Icon } from "./icons";
import { ErrorNotice, Modal, ProfileQRCode } from "./ui";

type ProfileFormat = "subscription" | "uri" | "xhttp_uri" | "sing_box_json" | "mihomo_yaml";
export type RevealedProfile = ProfileExport & { device: Device };

// Профиль и его ссылки живут только в состоянии компонента и никогда — в кеше запросов.
export function ProfileDialog({
  token,
  profile,
  onChange,
  onClose,
  onGuide,
}: {
  token: string;
  profile: RevealedProfile;
  onChange: (profile: RevealedProfile) => void;
  onClose: () => void;
  onGuide: () => void;
}) {
  const [format, setFormat] = useState<ProfileFormat>("subscription");
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [showQR, setShowQR] = useState(false);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [rotating, setRotating] = useState(false);
  const [rotated, setRotated] = useState(false);

  const subscriptionURL = new URL(profile.subscription_path, window.location.origin);
  if (profile.port) subscriptionURL.port = String(profile.port);
  const subscriptionLink = subscriptionURL.toString();
  // После обновления профиля выключенный вход не должен оставлять пустую ссылку для копирования.
  const activeFormat = format === "xhttp_uri" && !profile.xhttp_uri ? "uri" : format;
  const text = activeFormat === "subscription" ? subscriptionLink
    : activeFormat === "xhttp_uri" ? profile.xhttp_uri ?? "" : profile[activeFormat];
  const linkFormat = activeFormat === "subscription" || activeFormat === "uri" || activeFormat === "xhttp_uri";

  async function rotateSubscription() {
    setRotating(true);
    setError(null);
    try {
      const result = await request<{ subscription_path: string }>(
        token,
        `/devices/${encodeURIComponent(profile.device.id)}/subscription`,
        { method: "POST" },
      );
      onChange({ ...profile, subscription_path: result.subscription_path });
      setConfirmRotate(false);
      setRotated(true);
      setCopied(false);
      setShowQR(false);
    } catch (err) {
      setError(err);
    } finally {
      setRotating(false);
    }
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setError(null);
    } catch {
      setError(new Error("Буфер обмена недоступен. Выделите и скопируйте профиль вручную."));
    }
  }
  function download() {
    if (activeFormat !== "sing_box_json" && activeFormat !== "mihomo_yaml") return;
    try {
      const file = new Blob([profile[activeFormat]], {
        type: activeFormat === "sing_box_json" ? "application/json" : "application/yaml",
      });
      const location = URL.createObjectURL(file);
      const link = document.createElement("a");
      link.href = location;
      link.download = `private-gateway-${profile.device.id}.${activeFormat === "sing_box_json" ? "json" : "yaml"}`;
      document.body.append(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(location), 1000);
      setError(null);
    } catch {
      setError(new Error("Не удалось скачать файл. Скопируйте конфигурацию вручную."));
    }
  }

  return (
    <Modal title={`Профиль: ${profile.device.name}`} onClose={onClose}>
      <div className="notice warning-notice">
        <Icon name="lock" size={18} />
        Профиль содержит секрет доступа. Передавайте его только владельцу устройства.
      </div>
      <p className="modal-description">
        Ссылку-подписку добавляют в совместимое приложение VPN: приложение само получит
        профиль и будет его обновлять. VLESS URI вставляют или сканируют вручную; sing-box и Mihomo
        могут использовать собственный файл конфигурации. Для нового устройства сначала примените
        конфигурацию.
      </p>
      <label htmlFor="profile-format">Формат профиля</label>
      <select
        id="profile-format"
        className="profile-format-picker"
        value={activeFormat}
        onChange={(event) => {
          setFormat(event.target.value as ProfileFormat);
          setCopied(false);
          setShowQR(false);
        }}
      >
        <option value="subscription">Ссылка-подписка</option>
        {profile.xhttp_uri && <option value="xhttp_uri">VLESS · XHTTP + REALITY (URI и QR)</option>}
        <option value="uri">VLESS · TCP + REALITY + Vision (URI и QR)</option>
        <option value="sing_box_json">sing-box (JSON-файл)</option>
        <option value="mihomo_yaml">Mihomo (YAML-файл)</option>
      </select>
      <ProfileParameters format={activeFormat} profile={profile} />
      <label htmlFor="profile-content">
        {activeFormat === "subscription"
          ? "Ссылка-подписка"
          : linkFormat
            ? "Ссылка подключения"
            : "Конфигурация клиента"}
      </label>
      <textarea
        id="profile-content"
        className="profile-uri mono"
        value={text}
        readOnly
        rows={activeFormat === "subscription" ? 3 : linkFormat ? 5 : 12}
        spellCheck={false}
        onFocus={(event) => event.target.select()}
      />
      {linkFormat ? (
        <button
          className="button secondary-button profile-qr-toggle"
          onClick={() => setShowQR((visible) => !visible)}
          disabled={rotating}
          aria-expanded={showQR}
          aria-controls="profile-qr"
        >
          {showQR ? "Скрыть QR-код" : "Показать QR-код"}
        </button>
      ) : (
        <p className="profile-format-help">
          Файл содержит ключ устройства. После импорта удалите его из папки загрузок.
        </p>
      )}
      {showQR && activeFormat === "subscription" && (
        <ProfileQRCode
          uri={subscriptionLink}
          alt="QR-код ссылки-подписки"
          caption="Отсканируйте в приложении VPN или камерой телефона: в браузере ссылка откроет страницу с кнопками импорта."
          failure="Не удалось создать QR-код. Скопируйте ссылку вручную."
        />
      )}
      {showQR && (activeFormat === "uri" || activeFormat === "xhttp_uri") && <ProfileQRCode uri={text} />}
      {activeFormat === "subscription" && (
        <div className="subscription-help">
          <p className="profile-format-help">
            Ссылку можно передать человеку, который будет подключаться. «Отозвать» отключает и
            ссылку, и доступ.
          </p>
          <button type="button" className="button secondary-button" onClick={onGuide}>
            Как добавить ссылку в приложение
            <Icon name="arrow" size={16} />
          </button>
          {rotated && (
            <p className="notice" role="status">
              Ссылка заменена: прежняя больше не работает.
            </p>
          )}
          {confirmRotate ? (
            <div className="notice warning-notice subscription-rotate">
              <p>
                Прежняя ссылка сразу перестанет работать: в приложениях, где она добавлена, подписку
                придётся заменить.
              </p>
              <div className="modal-actions">
                <button
                  className="button secondary-button"
                  onClick={() => setConfirmRotate(false)}
                  disabled={rotating}
                >
                  Отмена
                </button>
                <button
                  className="button danger"
                  onClick={() => void rotateSubscription()}
                  disabled={rotating}
                >
                  {rotating ? "Заменяем…" : "Заменить ссылку"}
                </button>
              </div>
            </div>
          ) : (
            <button
              className="button secondary-button"
              disabled={rotating}
              onClick={() => {
                setConfirmRotate(true);
                setRotated(false);
              }}
            >
              Новая ссылка
            </button>
          )}
        </div>
      )}
      <ErrorNotice error={error} />
      <div className="modal-actions">
        <button className="button secondary-button" onClick={onClose}>
          Скрыть профиль
        </button>
        {!linkFormat && (
          <button className="button secondary-button" onClick={download} disabled={rotating}>
            Скачать файл
          </button>
        )}
        <button className="button primary" onClick={() => void copy()} disabled={rotating}>
          <Icon name={copied ? "check" : "copy"} size={17} />
          {copied ? "Скопировано" : linkFormat ? "Скопировать ссылку" : "Скопировать конфигурацию"}
        </button>
      </div>
    </Modal>
  );
}

function ProfileParameters({ format, profile }: { format: ProfileFormat; profile: RevealedProfile }) {
  const xhttp = format === "xhttp_uri";
  const subscription = format === "subscription";
  const both = subscription && Boolean(profile.xhttp_uri);
  const port = (value: number | undefined) => value ? String(value) : "Не указан";
  return (
    <section className="profile-parameters" aria-label="Параметры подключения">
      <h3>{subscription ? "Одна подписка для устройства" : xhttp ? "XHTTP · новые соединения" : "TCP + Vision · совместимость"}</h3>
      <p>
        {subscription
          ? both
            ? "Ссылка остаётся одной. Совместимые клиенты Xray могут получить XHTTP и TCP + Vision; форматы sing-box и Mihomo сохраняют TCP + Vision."
            : "Подписка содержит TCP + Vision. Приложение получает подходящий формат и обновляет профиль по этой же ссылке."
          : xhttp
            ? "Повторное использование соединения может уменьшить задержку новых запросов. Скорость требует замера в вашей сети."
            : "Вариант для совместимых клиентов VLESS + REALITY + Vision. Подходит, если приложение не поддерживает XHTTP."}
      </p>
      <dl>
        <div><dt>Транспорт</dt><dd>{both ? "XHTTP и TCP" : xhttp ? "XHTTP" : "TCP"}</dd></div>
        <div><dt>Защита</dt><dd>{xhttp ? "REALITY, без Vision" : both ? "REALITY; Vision для TCP" : "REALITY + Vision"}</dd></div>
        <div><dt>{both ? "Порты VPN" : "Порт VPN"}</dt><dd>{both
          ? `XHTTP: ${port(profile.xhttp_port)}; TCP: ${port(profile.port)}`
          : port(xhttp ? profile.xhttp_port : profile.port)}</dd></div>
        <div><dt>Задержка</dt><dd>Не измерено</dd></div>
      </dl>
      <p>{xhttp
        ? "Клиент должен поддерживать VLESS + XHTTP + REALITY и импорт параметров ссылки. Совместимость конкретной версии требует проверки."
        : subscription
          ? "Набор подключений зависит от поддерживаемого клиентом формата. Наличие двух профилей не включает автоматическое переключение."
          : "Поддержку VLESS + REALITY + Vision необходимо проверить в используемой версии клиента."}</p>
      {!subscription && <p>Этот выбор меняет ссылку и QR-код. Само VPN-подключение переключается в приложении клиента.</p>}
    </section>
  );
}
