import { useEffect, useRef, useState, type ReactNode } from "react";
import { errorMessage } from "./api";
import { Icon, type IconName } from "./icons";

export const date = (value: string) =>
  new Date(value).toLocaleString("ru-RU", {
    dateStyle: "short",
    timeStyle: "short",
  });

export const bytes = (value: number) => {
  if (value < 1024) return `${value} Б`;
  const units = ["КБ", "МБ", "ГБ", "ТБ"];
  let number = value;
  let unit = -1;
  do {
    number /= 1024;
    unit++;
  } while (number >= 1024 && unit < units.length - 1);
  return `${new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 }).format(number)} ${units[unit]}`;
};

export function actionLabel(action: string) {
  const labels: Record<string, string> = {
    "device.create": "Устройство добавлено",
    "device.created": "Устройство добавлено",
    "device.revoke": "Доступ отозван",
    "device.delete": "Устройство удалено",
    "device.revoked": "Доступ отозван",
    "device.profile": "Профиль выдан",
    "device.export": "Профиль выдан",
    "device.subscription_rotate": "Ссылка-подписка заменена",
    "profile.export": "Профиль выдан",
    "config.validate": "Конфигурация проверена",
    "config.apply": "Конфигурация применена",
    "browser.start": "Браузер запущен",
    "browser.stop": "Браузер остановлен",
  };
  return labels[action] || action;
}

export function ErrorNotice({ error }: { error: unknown }) {
  return error ? (
    <div className="notice error" role="alert">
      {errorMessage(error)}
    </div>
  ) : null;
}

export function Empty({
  icon,
  title,
  children,
}: {
  icon: IconName;
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-icon">
        <Icon name={icon} size={28} />
      </span>
      <h3>{title}</h3>
      <p>{children}</p>
    </div>
  );
}

export function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => ref.current?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="modal"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className="modal-heading">
        <h2>{title}</h2>
        <button className="icon-button" onClick={onClose} aria-label="Закрыть">
          <Icon name="close" />
        </button>
      </div>
      {children}
    </dialog>
  );
}

export function ProfileQRCode({
  uri,
  alt = "QR-код профиля подключения",
  caption = "В совместимом клиенте VLESS + REALITY выберите импорт через QR-код.",
  failure = "Не удалось создать QR-код. Скопируйте URI вручную.",
}: {
  uri: string;
  alt?: string;
  caption?: string;
  failure?: string;
}) {
  const [image, setImage] = useState("");
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;
    void import("qrcode")
      .then(({ toDataURL }) =>
        toDataURL(uri, {
          errorCorrectionLevel: "M",
          margin: 3,
          width: 288,
          color: { dark: "#101d2b", light: "#ffffff" },
        }),
      )
      .then((data) => {
        if (active) setImage(data);
      })
      .catch(() => {
        if (active) setFailed(true);
      });
    return () => {
      active = false;
    };
  }, [uri]);

  return (
    <div className="profile-qr" id="profile-qr">
      {image ? (
        <img src={image} alt={alt} width="288" height="288" />
      ) : failed ? (
        <p>{failure}</p>
      ) : (
        <p>Создаём QR-код…</p>
      )}
      <p>{caption}</p>
    </div>
  );
}
