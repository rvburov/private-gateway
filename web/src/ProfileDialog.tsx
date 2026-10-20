import { useState } from "react";
import { request, type Device, type ProfileExport } from "./api";
import { Icon } from "./icons";
import { ErrorNotice, Modal, ProfileQRCode } from "./ui";

type ProfileFormat = "subscription" | "uri" | "sing_box_json" | "mihomo_yaml";
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
  const [changingPort, setChangingPort] = useState(false);

  const selectedPort = profile.fallback_port && profile.port === profile.fallback_port
    ? String(profile.fallback_port) : "primary";
  const subscriptionURL = new URL(profile.subscription_path, window.location.origin);
  if (selectedPort !== "primary") subscriptionURL.port = selectedPort;
  const subscriptionLink = subscriptionURL.toString();
  const text = format === "subscription" ? subscriptionLink : profile[format];
  const linkFormat = format === "subscription" || format === "uri";

  async function changePort(port: string) {
    setChangingPort(true);
    setCopied(false);
    setShowQR(false);
    setError(null);
    try {
      const result = await request<ProfileExport>(
        token,
        `/devices/${encodeURIComponent(profile.device.id)}/profile?port=${port}`,
      );
      onChange({ ...result, device: profile.device });
    } catch (err) {
      setError(err);
    } finally {
      setChangingPort(false);
    }
  }

  async function rotateSubscription() {
    setRotating(true);
    setError(null);
    try {
      const result = await request<{ subscription_path: string }>(
        token,
        `/devices/${encodeURIComponent(profile.device.id)}/subscription${profile.fallback_port ? `?port=${selectedPort}` : ""}`,
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
    if (format === "subscription" || format === "uri") return;
    try {
      const file = new Blob([profile[format]], {
        type: format === "sing_box_json" ? "application/json" : "application/yaml",
      });
      const location = URL.createObjectURL(file);
      const link = document.createElement("a");
      link.href = location;
      link.download = `private-gateway-${profile.device.id}.${format === "sing_box_json" ? "json" : "yaml"}`;
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
        Ссылку-подписку добавляют в приложение VPN на любом устройстве: приложение само получит
        профиль и будет его обновлять. VLESS URI вставляют или сканируют вручную; sing-box и Mihomo
        могут использовать собственный файл конфигурации. Для нового устройства сначала примените
        конфигурацию.
      </p>
      {!!profile.fallback_port && (
        <>
          <label htmlFor="profile-port">Вход на сервер</label>
          <select
            id="profile-port"
            className="profile-format-picker"
            value={selectedPort}
            disabled={changingPort || rotating || confirmRotate}
            onChange={(event) => void changePort(event.target.value)}
          >
            <option value="primary">Основной вход</option>
            <option value={profile.fallback_port}>Резервный вход — {profile.fallback_port}</option>
          </select>
          <p className="profile-format-help" role="status">
            {changingPort ? "Получаем профиль…" :
              "Если основной вход недоступен, выберите резервный и заново импортируйте ссылку или QR-код в приложение."}
          </p>
        </>
      )}
      <label htmlFor="profile-format">Формат профиля</label>
      <select
        id="profile-format"
        className="profile-format-picker"
        value={format}
        onChange={(event) => {
          setFormat(event.target.value as ProfileFormat);
          setCopied(false);
          setShowQR(false);
        }}
      >
        <option value="subscription">Ссылка-подписка</option>
        <option value="uri">VLESS-ссылка (URI и QR)</option>
        <option value="sing_box_json">sing-box (JSON-файл)</option>
        <option value="mihomo_yaml">Mihomo (YAML-файл)</option>
      </select>
      <label htmlFor="profile-content">
        {format === "subscription"
          ? "Ссылка-подписка"
          : format === "uri"
            ? "Ссылка подключения"
            : "Конфигурация клиента"}
      </label>
      <textarea
        id="profile-content"
        className="profile-uri mono"
        value={text}
        readOnly
        rows={format === "subscription" ? 3 : format === "uri" ? 5 : 12}
        spellCheck={false}
        onFocus={(event) => event.target.select()}
      />
      {linkFormat ? (
        <button
          className="button secondary-button profile-qr-toggle"
          onClick={() => setShowQR((visible) => !visible)}
          disabled={changingPort || rotating}
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
      {showQR && format === "subscription" && (
        <ProfileQRCode
          uri={subscriptionLink}
          alt="QR-код ссылки-подписки"
          caption="Отсканируйте в приложении VPN или камерой телефона: в браузере ссылка откроет страницу с кнопками импорта."
          failure="Не удалось создать QR-код. Скопируйте ссылку вручную."
        />
      )}
      {showQR && format === "uri" && <ProfileQRCode uri={profile.uri} />}
      {format === "subscription" && (
        <div className="subscription-help">
          <p className="profile-format-help">
            Ссылку можно передать человеку, который будет подключаться. «Отозвать» отключает и
            ссылку, и доступ.
          </p>
          <button type="button" className="text-button" onClick={onGuide}>
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
              disabled={changingPort || rotating}
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
          <button className="button secondary-button" onClick={download} disabled={changingPort || rotating}>
            Скачать файл
          </button>
        )}
        <button className="button primary" onClick={() => void copy()} disabled={changingPort || rotating}>
          <Icon name={copied ? "check" : "copy"} size={17} />
          {copied ? "Скопировано" : linkFormat ? "Скопировать ссылку" : "Скопировать конфигурацию"}
        </button>
      </div>
    </Modal>
  );
}
