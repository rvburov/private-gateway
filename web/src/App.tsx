import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { request, type Status } from "./api";
import { Dashboard } from "./Dashboard";
import { InstallApp } from "./InstallApp";
import { cleanupBrowserPush, currentPushHashForLogout } from "./push";
import { Icon } from "./icons";
import { ErrorNotice, ProfileQRCode } from "./ui";

export default function App() {
  const queryClient = useQueryClient();
  const [token, setToken] = useState("");
  const [restoring, setRestoring] = useState(true);
  const [remember, setRemember] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [loginStep, setLoginStep] = useState<"password" | "totp" | "setup">("password");
  const [pendingToken, setPendingToken] = useState("");
  const [setupSecret, setSetupSecret] = useState("");
  const [setupURI, setSetupURI] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [installOpen, setInstallOpen] = useState(false);
  useEffect(() => {
    const rememberDestination = (event: MessageEvent) => {
      if (event.data?.type !== "gateway-notification-open") return;
      const destination = new URL(window.location.href);
      destination.searchParams.set("page", "notifications");
      window.history.replaceState(null, "", destination.pathname + destination.search + destination.hash);
    };
    navigator.serviceWorker?.addEventListener("message", rememberDestination);
    return () => navigator.serviceWorker?.removeEventListener("message", rememberDestination);
  }, []);
  useEffect(() => {
    let active = true;
    void fetch("/api/v1/auth/restore", { credentials: "same-origin" })
      .then(async (response) => {
        if (response.status === 401) return;
        if (!response.ok) throw new Error("Не удалось проверить сохранённый вход. Повторите позже.");
        const credential = ((await response.json()) as { session: string }).session;
        await request<Status>(credential, "/status");
        if (active) setToken(credential);
      })
      .catch((err: unknown) => { if (active) setError(err); })
      .finally(() => { if (active) setRestoring(false); });
    return () => { active = false; };
  }, []);
  async function login(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setPending(true);
    try {
      if (loginStep === "password") {
        const response = await fetch("/api/v1/auth/login", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ username: username.trim(), password }),
        });
        if (!response.ok)
          throw new Error(
            response.status === 429
              ? "Слишком много попыток. Повторите через пять минут."
              : "Неверный логин или пароль.",
          );
        const result = (await response.json()) as { status: string; pending: string };
        if (!result.pending || !["totp_required", "totp_setup_required"].includes(result.status))
          throw new Error("Не удалось начать вход.");
        setPendingToken(result.pending);
        setPassword("");
        if (result.status === "totp_setup_required") {
          const setup = await fetch("/api/v1/auth/2fa/setup", {
            method: "POST",
            credentials: "same-origin",
            headers: { Authorization: `Bearer ${result.pending}` },
          });
          if (!setup.ok)
            throw new Error("Не удалось начать настройку аутентификатора. Повторите вход.");
          const details = (await setup.json()) as { secret: string; otpauth_uri: string };
          setSetupSecret(details.secret);
          setSetupURI(details.otpauth_uri);
          setLoginStep("setup");
        } else {
          setLoginStep("totp");
        }
        return;
      }
      const response = await fetch(
        loginStep === "setup" ? "/api/v1/auth/2fa/setup/confirm" : "/api/v1/auth/2fa",
        {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${pendingToken}` },
          body: JSON.stringify({ code: code.trim(), remember }),
        },
      );
      if (!response.ok)
        throw new Error(
          response.status === 401
            ? "Неверный код или время входа истекло. Проверьте код; при истечении времени начните вход заново."
            : "Не удалось проверить код. Повторите попытку.",
        );
      const credential = ((await response.json()) as { session: string }).session;
      await request<Status>(credential, "/status");
      queryClient.clear();
      setToken(credential);
      setPassword("");
      setCode("");
      setPendingToken("");
      setSetupSecret("");
      setSetupURI("");
      setLoginStep("password");
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }
  async function logout() {
    try {
      if (token) {
        const pushHash = await currentPushHashForLogout();
        const response = await fetch("/api/v1/auth/logout", {
          method: "POST",
          credentials: "same-origin",
          headers: { Authorization: `Bearer ${token}`, ...(pushHash ? { "X-Gateway-Push-Hash": pushHash } : {}) },
        });
        if (!response.ok) throw new Error("Не удалось завершить вход. Повторите попытку.");
      }
    } catch {
      setError(new Error("Не удалось завершить вход. Повторите попытку."));
      return;
    }
    // Сервер уже отозвал разрешение доставки; локальная отписка дополняет этот отзыв.
    void cleanupBrowserPush().catch(() => {});
    queryClient.clear();
    setToken("");
    setError(null);
  }
  function backToPassword() {
    setPendingToken("");
    setSetupSecret("");
    setSetupURI("");
    setCode("");
    setLoginStep("password");
    setError(null);
  }
  if (restoring) return <div className="login-page"><p>Проверяем сохранённый вход…</p></div>;
  if (token) return <>
    <ErrorNotice error={error} />
    <Dashboard token={token} onLogout={() => { void logout(); }} onInstall={() => setInstallOpen(true)} onSignIn={() => {
      void queryClient.cancelQueries();
      queryClient.clear();
      setToken("");
      setError(new Error("Сервер не принял сеанс входа. Введите логин, пароль и код аутентификации снова."));
    }} />
    {installOpen && <InstallApp onClose={() => setInstallOpen(false)} />}
  </>;
  return (
    <div className="login-page">
      <div className="login-intro">
        <div className="brand">
          <span className="brand-mark">
            <Icon name="lock" size={20} />
          </span>
          <span>
            private<span className="brand-light">gateway</span>
          </span>
        </div>
        <div className="login-message">
          <span className="eyebrow">ЛИЧНАЯ ИНФРАСТРУКТУРА</span>
          <h1>
            Ваш доступ.
            <br />
            Ваши правила.
          </h1>
          <p>Управляйте устройствами и приватным подключением через собственный сервер.</p>
          <div className="intro-tags">
            <span>VLESS + REALITY</span>
            <span>Самостоятельный хостинг</span>
          </div>
        </div>
        <span className="login-footnote">Private Gateway · Панель управления</span>
      </div>
      <div className="login-form-wrap">
        <form onSubmit={login} className="login-form">
          <span className="login-lock">
            <Icon name="lock" size={26} />
          </span>
          <h2>Вход в панель</h2>
          <p>
            {loginStep === "password"
              ? "Шаг 1 из 2: введите логин и пароль."
              : loginStep === "setup"
                ? "Шаг 2 из 2: подключите приложение-аутентификатор и подтвердите код."
                : "Шаг 2 из 2: введите код из приложения-аутентификатора."}
          </p>
          {loginStep === "password" ? (
            <>
              <label htmlFor="username">Логин</label>
              <input
                id="username"
                type="text"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                required
                autoFocus
              />
              <label htmlFor="password">Пароль</label>
              <input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
              <label className="remember-login" htmlFor="remember-login">
                <input id="remember-login" type="checkbox" checked={remember}
                  onChange={(event) => setRemember(event.target.checked)} />
                Запомнить вход на этом устройстве на 30 дней
              </label>
            </>
          ) : (
            <>
              <div className="login-identity">
                Входите как <strong>{username}</strong>
              </div>
              {loginStep === "setup" && setupURI && (
                <>
                  <ProfileQRCode
                    uri={setupURI}
                    alt="QR-код приложения-аутентификатора"
                    caption="Сканируйте QR-код в приложении-аутентификаторе на телефоне."
                    failure="Не удалось создать QR-код. Введите ключ ниже вручную."
                  />
                  <p className="setup-secret">
                    Не получается сканировать? Введите ключ вручную: <code>{setupSecret}</code>
                  </p>
                </>
              )}
              <label htmlFor="code">Код аутентификации</label>
              <input
                id="code"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                value={code}
                onChange={(event) => setCode(event.target.value)}
                required
              />
            </>
          )}
          <ErrorNotice error={error} />
          <button
            className="button primary full"
            disabled={
              pending ||
              (loginStep === "password"
                ? !username.trim() || !password
                : !/^[0-9]{6}$/.test(code.trim()))
            }
          >
            {pending
              ? "Проверяем доступ…"
              : loginStep === "password"
                ? "Продолжить"
                : loginStep === "setup"
                  ? "Подтвердить и войти"
                  : "Открыть панель"}
            <Icon name="arrow" size={18} />
          </button>
          {loginStep !== "password" && (
            <button type="button" className="login-back" onClick={backToPassword}>
              ← Назад к логину
            </button>
          )}
          <div className="privacy-note">
            <Icon name="lock" size={14} />
            <span>
              {remember
                ? "На этом устройстве вход восстановится после закрытия браузера. На чужом компьютере снимите галочку."
                : "Без сохранения входа сеанс хранится только в памяти вкладки."}
            </span>
          </div>
          <button type="button" className="button full install-login" onClick={() => setInstallOpen(true)}>
            <Icon name="download" size={18} />Установить приложение
          </button>
        </form>
      </div>
      {installOpen && <InstallApp onClose={() => setInstallOpen(false)} />}
    </div>
  );
}
