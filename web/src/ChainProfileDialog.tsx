import { useEffect, useRef, useState } from "react";
import { request, type Device } from "./api";
import { ErrorNotice, Modal } from "./ui";

// Секретный ответ хранится только пока открыт диалог, не в TanStack Query.
export function ChainProfileDialog({ token, device, bundle, onClose }: {
  token: string; device: Device; bundle: Record<string, unknown>; onClose: () => void;
}) {
  const initialSubscription = typeof bundle.subscription_url === "string"
    ? new URL(bundle.subscription_url, window.location.origin).toString() : "";
  const [subscription, setSubscription] = useState(initialSubscription);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [copied, setCopied] = useState(false);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  const prepared = { ...bundle };
  if (subscription) prepared.subscription_url = subscription;
  if (typeof prepared.subscription_url === "string" && prepared.subscription_url.startsWith("/sub/entry/")) {
    prepared.subscription_url = new URL(prepared.subscription_url, window.location.origin).toString();
  }
  const json = JSON.stringify(prepared, null, 2);
  async function copy(value: string) {
    try { await navigator.clipboard.writeText(value); setCopied(true); }
    catch { setError(new Error("Буфер обмена недоступен. Выделите и скопируйте текст вручную.")); }
  }
  function download() {
    const url = URL.createObjectURL(new Blob([json], { type: "application/json" }));
    const anchor = document.createElement("a");
    anchor.href = url; anchor.download = `private-gateway-${device.id}.json`;
    document.body.append(anchor); anchor.click(); anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  async function rotate() {
    const abort = new AbortController(); controller.current = abort;
    setBusy(true); setError(null);
    try {
      const result = await request<{ subscription_path: string; subscription_url?: string }>(token,
        `/devices/${encodeURIComponent(device.id)}/chain-subscription`, { method: "POST", signal: abort.signal });
      abort.signal.throwIfAborted();
      setSubscription(new URL(result.subscription_url || result.subscription_path, window.location.origin).toString());
      setConfirmRotate(false); setCopied(false);
    } catch (err) { if (!abort.signal.aborted) setError(err); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  return <Modal title={`Профиль VPS: ${device.name}`} onClose={onClose}>
    <p className="modal-description">Устройство подключается через назначенный SOCKS5 к VPS. В приложении появятся варианты VPN этого VPS; отдельного профиля с выходом через IP прокси нет.</p>
    <div className="notice warning-notice">Файл содержит ключ устройства и данные входного прокси. Передавайте его только владельцу устройства.</div>
    <p>Импортируйте файл в Private Gateway с поддержкой цепочек. Обычная VLESS-ссылка не содержит входной прокси. Для другого приложения нужна подтверждённая поддержка такого формата.</p>
    <label htmlFor="chain-content">Профиль с входным прокси</label>
    <textarea id="chain-content" className="profile-uri mono" readOnly rows={10} value={json} spellCheck={false} onFocus={(event) => event.target.select()} />
    <div className="button-group">
      <button className="button primary" onClick={download}>Скачать профиль VPS</button>
      <button className="button secondary-button" onClick={() => void copy(json)}>{copied ? "Скопировано" : "Скопировать профиль"}</button>
    </div>
    <h3>Обновление профиля</h3>
    <p className="hint">После смены входного прокси обновите профиль на устройстве. Если прежний прокси уже недоступен, заново импортируйте файл через доступное соединение.</p>
    {subscription && <><label htmlFor="chain-subscription">Подписка с входным прокси</label>
      <textarea id="chain-subscription" className="profile-uri mono" readOnly rows={3} value={subscription} onFocus={(event) => event.target.select()} />
      <p className="hint">Ссылка передаёт также реквизиты входного прокси. Первую загрузку выполняйте через доступное соединение; сама ссылка не открывает доступ к заблокированному адресу панели.</p></>}
    {confirmRotate ? <div className="notice warning-notice"><p>Прежняя подписка цепочки перестанет работать. Новую ссылку нужно импортировать в клиент.</p>
      <div className="button-group"><button className="button secondary-button" disabled={busy} onClick={() => setConfirmRotate(false)}>Отмена</button>
        <button className="button primary" disabled={busy} onClick={() => void rotate()}>{busy ? "Создаём…" : "Создать новую подписку"}</button></div></div>
      : <button className="button secondary-button" onClick={() => setConfirmRotate(true)}>Новая подписка цепочки</button>}
    <ErrorNotice error={error} />
    <div className="modal-actions"><button className="button secondary-button" onClick={onClose}>Скрыть профиль</button></div>
  </Modal>;
}
