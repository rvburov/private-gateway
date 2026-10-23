import { useState } from "react";
import { HelpTip } from "./HelpTip";
import { currentPlatform } from "./pwa/platform";
import { promptInstall, useInstall } from "./pwa/runtime";
import { ErrorNotice, Modal } from "./ui";

export function InstallApp({ onClose }: { onClose: () => void }) {
  const { installed, canPrompt, kind } = useInstall();
  const platform = currentPlatform();
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState<unknown>(null);
  async function install() {
    setPending(true);
    setMessage("");
    setError(null);
    try {
      const outcome = await promptInstall();
      setMessage(outcome === "accepted" ? "Завершите установку в окне браузера." : outcome === "dismissed"
        ? "Установка отменена. Её можно повторить из меню браузера." : "Откройте установку из меню браузера.");
    } catch {
      setError(new Error("Не удалось открыть установку. Воспользуйтесь меню браузера."));
    } finally { setPending(false); }
  }
  return (
    <Modal title="Установить приложение" onClose={onClose}>
      <div className="install-app-body">
        <div className="install-app-brand">
          <img src="/icons/icon-192.png" width={56} height={56} alt="" />
          <div><h3>Private Gateway</h3><p className="hint">Панель управления на главном экране.</p></div>
        </div>
        {installed ? (
          <p role="status">Приложение установлено. Открывайте его с главного экрана или из списка приложений.</p>
        ) : kind === "ios" ? (
          <ol className="install-app-steps" aria-label="Установка на iPhone и iPad">
            <li>Откройте панель в Safari.</li>
            <li>Нажмите «Поделиться» → «На экран „Домой“».</li>
            <li>Нажмите «Добавить» и откройте Private Gateway с появившегося значка.</li>
          </ol>
        ) : kind === "android" ? (
          <p>{canPrompt ? "Нажмите «Установить» и подтвердите действие в браузере." : "В меню браузера выберите «Установить приложение» или «Добавить на главный экран»."}</p>
        ) : platform.browser === "safari" ? (
          <p>В Safari на Mac откройте «Файл» → «Добавить в Dock» (macOS Sonoma и новее).</p>
        ) : platform.browser === "firefox" ? (
          <p>Если в меню Firefox есть пункт установки сайта как приложения, выберите его. Иначе откройте панель в Chrome и выберите «Установить» в меню браузера.</p>
        ) : (
          <p>{canPrompt ? "Нажмите «Установить» и подтвердите действие в браузере." : "В меню Chrome выберите «Трансляция, сохранение и отправка» → «Установить страницу как приложение». Название пункта зависит от версии браузера."}</p>
        )}
        <div className="diagnostics-title-with-help">
          <p className="hint">{kind === "ios" ? "Для push нужен запуск со значка, iOS/iPadOS 16.4+ и разрешение." : "Уведомления включаются отдельно в настройках приложения."}</p>
          <HelpTip label="Об установленном приложении">
            <p>Устанавливается панель Private Gateway. Для VPN-подключения используйте клиент из раздела «Инструкции».</p>
            <p>Для управления сервером нужен интернет. Без сети приложение покажет только общедоступную оболочку; профили, токены входа и ответы API в офлайн-кэш не сохраняются.</p>
            <p>Установка работает по HTTPS в поддерживаемом браузере. Если её нет в меню, обновите браузер. Приватные окна могут не поддерживать установку.</p>
          </HelpTip>
        </div>
        {message && !installed && <p role="status" className="hint">{message}</p>}
        <ErrorNotice error={error} />
      </div>
      <div className="modal-actions">
        <button className="button" onClick={onClose}>{installed ? "Готово" : "Закрыть"}</button>
        {!installed && kind !== "ios" && canPrompt && <button className="button primary" disabled={pending} onClick={() => void install()}>{pending ? "Открываем…" : "Установить"}</button>}
      </div>
    </Modal>
  );
}
