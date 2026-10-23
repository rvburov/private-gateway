import { APIError, APIResponseError } from "./api";

export type ConnectionState = "ok" | "pending" | "auth" | "offline" | "network" | "timeout" | "http" | "invalid";

export const connectionLabels: Record<ConnectionState, string> = {
  ok: "API подключён",
  pending: "Подключение…",
  auth: "Нужен вход",
  offline: "Нет сети",
  network: "Нет связи",
  timeout: "Нет ответа",
  http: "Ошибка API",
  invalid: "Неверный ответ",
};

export function connectionState(error: unknown, pending: boolean, offline: boolean): ConnectionState {
  if (error instanceof APIError && error.status === 401) return "auth";
  if (offline) return "offline";
  if (error instanceof APIError) return "http";
  if (error instanceof APIResponseError) return "invalid";
  if ((error instanceof Error || error instanceof DOMException) && error.name === "TimeoutError") return "timeout";
  if (error) return "network";
  return pending ? "pending" : "ok";
}

const notices: Record<Exclude<ConnectionState, "ok" | "pending">, { title: string; detail: string }> = {
  auth: {
    title: "Нужно войти снова",
    detail: "Сервер ответил, но не принял сеанс входа (HTTP 401). Это не означает, что VPN остановлен.",
  },
  offline: {
    title: "Браузер сообщает, что устройство не в сети",
    detail: "Проверьте Wi-Fi или мобильное подключение. Состояние сервера сейчас неизвестно.",
  },
  network: {
    title: "Нет связи с панелью",
    detail: "Браузер не смог получить полный ответ. По этой ошибке нельзя определить, где сбой: на устройстве, в сети или на сервере.",
  },
  timeout: {
    title: "Панель не ответила вовремя",
    detail: "Истекло время ожидания ответа. Это ещё не подтверждает остановку сервера или VPN.",
  },
  http: {
    title: "API вернул ошибку",
    detail: "Ответ получен, но запрос не выполнен. Повторная проверка обновит состояние, не повторяя ваши действия.",
  },
  invalid: {
    title: "Не удалось прочитать ответ панели",
    detail: "Сервер ответил в неизвестном формате. Обновите страницу и проверьте связь снова.",
  },
};

const serviceErrors: Record<string, string> = {
  "database unavailable": "База данных панели недоступна.",
  "authentication unavailable": "Служба проверки входа недоступна.",
};

export function ConnectionNotice({ state, error, checking, onRetry, onSignIn }: {
  state: ConnectionState;
  error: unknown;
  checking: boolean;
  onRetry: () => void;
  onSignIn: () => void;
}) {
  if (state === "ok" || state === "pending") return null;
  const notice = notices[state];
  const detail = state === "http" && error instanceof APIError && serviceErrors[error.message]
    ? serviceErrors[error.message]
    : notice.detail;
  return (
    <section className={`notice connection-notice ${state === "auth" ? "warning-notice" : "error"}`} role="alert" aria-label={notice.title}>
      <div>
        <h2>{notice.title}{state === "http" && error instanceof APIError ? `: HTTP ${error.status}` : ""}</h2>
        <p>{detail}</p>
      </div>
      {state === "auth"
        ? <button className="button" onClick={onSignIn}>Войти снова</button>
        : <button className="button" disabled={checking || state === "offline"} onClick={onRetry}>{checking ? "Проверяем…" : "Проверить связь"}</button>}
    </section>
  );
}
