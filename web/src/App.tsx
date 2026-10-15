import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  request,
  errorMessage,
  type AuditEvent,
  type BrowserStatus,
  type Device,
  type Node,
  type ProfileExport,
  type Status,
} from "./api";

type Page = "overview" | "devices" | "nodes" | "audit";
type IconName =
  | "grid"
  | "devices"
  | "server"
  | "history"
  | "arrow"
  | "lock"
  | "plus"
  | "copy"
  | "close"
  | "logout"
  | "check"
  | "browser";
const icons: Record<IconName, ReactNode> = {
  grid: (
    <>
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    </>
  ),
  devices: (
    <>
      <rect x="3" y="4" width="13" height="12" rx="2" />
      <path d="M6 20h7M10 16v4" />
      <rect x="17" y="10" width="5" height="11" rx="1" />
    </>
  ),
  server: (
    <>
      <rect x="3" y="3" width="18" height="7" rx="2" />
      <rect x="3" y="14" width="18" height="7" rx="2" />
      <path d="M7 6.5h.01M7 17.5h.01M12 6.5h5M12 17.5h5" />
    </>
  ),
  history: (
    <>
      <path d="M3 11a9 9 0 1 1 3 7M3 4v7h7" />
      <path d="M12 7v5l3 2" />
    </>
  ),
  arrow: (
    <>
      <path d="M5 12h14M13 6l6 6-6 6" />
    </>
  ),
  lock: (
    <>
      <rect x="5" y="10" width="14" height="11" rx="2" />
      <path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" />
    </>
  ),
  plus: <path d="M12 5v14M5 12h14" />,
  copy: (
    <>
      <rect x="8" y="8" width="12" height="13" rx="2" />
      <path d="M16 8V3H3v13h5" />
    </>
  ),
  close: <path d="m6 6 12 12M18 6 6 18" />,
  logout: (
    <>
      <path d="M9 4H4v16h5M10 12h11M17 8l4 4-4 4" />
    </>
  ),
  check: <path d="m5 12 4 4L19 6" />,
  browser: (
    <>
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <path d="M3 8h18M7 5.5h.01M10 5.5h.01" />
    </>
  ),
};
const Icon = ({ name, size = 20 }: { name: IconName; size?: number }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.6"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
  >
    {icons[name]}
  </svg>
);
const date = (value: string) =>
  new Date(value).toLocaleString("ru-RU", {
    dateStyle: "short",
    timeStyle: "short",
  });
const nav: { page: Page; label: string; icon: IconName }[] = [
  { page: "overview", label: "Обзор", icon: "grid" },
  { page: "devices", label: "Устройства", icon: "devices" },
  { page: "nodes", label: "Узлы", icon: "server" },
  { page: "audit", label: "Журнал действий", icon: "history" },
];
const titles: Record<Page, [string, string]> = {
  overview: [
    "Ваш приватный шлюз",
    "Доступ, конфигурация и удалённый браузер — в одном месте.",
  ],
  devices: [
    "Устройства",
    "Отдельный профиль для каждого устройства. Доступ можно отозвать в любой момент.",
  ],
  nodes: [
    "Узлы подключения",
    "Параметры серверов для подключения по VLESS + REALITY.",
  ],
  audit: ["Журнал действий", "История изменений доступа и конфигурации шлюза."],
};

function ErrorNotice({ error }: { error: unknown }) {
  return error ? (
    <div className="notice error" role="alert">
      {errorMessage(error)}
    </div>
  ) : null;
}
function Empty({
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
function Modal({
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

function ProfileQRCode({ uri }: { uri: string }) {
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
        <img src={image} alt="QR-код профиля подключения" width="288" height="288" />
      ) : failed ? (
        <p>Не удалось создать QR-код. Скопируйте URI вручную.</p>
      ) : (
        <p>Создаём QR-код…</p>
      )}
      <p>В совместимом клиенте VLESS + REALITY выберите импорт через QR-код.</p>
    </div>
  );
}

export default function App() {
  const queryClient = useQueryClient();
  const [token, setToken] = useState("");
  const [input, setInput] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function login(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setPending(true);
    try {
      const credential = input.trim();
      await request<Status>(credential, "/status");
      queryClient.clear();
      setToken(credential);
      setInput("");
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }
  function logout() {
    queryClient.clear();
    setToken("");
    setError(null);
  }
  if (token) return <Dashboard token={token} onLogout={logout} />;
  return (
    <div className="login-page">
      <div className="login-intro">
        <div className="brand">
          <span className="brand-mark">
            <Icon name="lock" size={23} />
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
          <p>
            Управляйте устройствами и приватным подключением через собственный
            сервер.
          </p>
          <div className="intro-tags">
            <span>VLESS + REALITY</span>
            <span>Самостоятельный хостинг</span>
          </div>
        </div>
        <span className="login-footnote">
          Private Gateway · Панель управления
        </span>
      </div>
      <div className="login-form-wrap">
        <form onSubmit={login} className="login-form">
          <span className="login-lock">
            <Icon name="lock" size={26} />
          </span>
          <h2>Вход в панель</h2>
          <p>Введите токен администратора вашего шлюза.</p>
          <label htmlFor="token">Токен доступа</label>
          <input
            id="token"
            type="password"
            autoComplete="off"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            value={input}
            onChange={(event) => setInput(event.target.value)}
            placeholder="GATEWAY_API_TOKEN"
            required
            autoFocus
          />
          {input && (
            <span className="token-format">
              {/^[0-9a-f]{64}$/.test(input.trim())
                ? "Введено 64 символа, формат токена верный."
                : `Введено ${input.trim().length} символов. Токен содержит 64 латинских символа 0–9 и a–f.`}
            </span>
          )}
          <ErrorNotice error={error} />
          <button
            className="button primary full"
            disabled={pending || !input.trim()}
          >
            {pending ? "Проверяем доступ…" : "Открыть панель"}
            <Icon name="arrow" size={18} />
          </button>
          <div className="privacy-note">
            <Icon name="lock" size={14} />
            <span>
              Токен хранится только в памяти этой вкладки. После перезагрузки
              потребуется войти снова.
            </span>
          </div>
        </form>
      </div>
    </div>
  );
}

function Dashboard({
  token,
  onLogout,
}: {
  token: string;
  onLogout: () => void;
}) {
  const queryClient = useQueryClient();
  const [page, setPage] = useState<Page>("overview");
  const [adding, setAdding] = useState(false);
  const [revoking, setRevoking] = useState<Device | null>(null);
  const [profile, setProfile] = useState<(ProfileExport & { device: Device }) | null>(null);
  const [profileFormat, setProfileFormat] = useState<keyof ProfileExport>("uri");
  const [profilePending, setProfilePending] = useState<string | null>(null);
  const [profileError, setProfileError] = useState<unknown>(null);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<unknown>(null);
  const [showQR, setShowQR] = useState(false);
  const [name, setName] = useState("");
  const [nodeID, setNodeID] = useState("");
  const [notice, setNotice] = useState("");
  const status = useQuery({
    queryKey: ["status"],
    queryFn: ({ signal }) => request<Status>(token, "/status", { signal }),
    refetchInterval: 10_000,
  });
  const devices = useQuery({
    queryKey: ["devices"],
    queryFn: ({ signal }) =>
      request<{ devices: Device[] }>(token, "/devices", { signal }),
  });
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: ({ signal }) =>
      request<{ nodes: Node[] }>(token, "/nodes", { signal }),
  });
  const audit = useQuery({
    queryKey: ["audit"],
    queryFn: ({ signal }) =>
      request<{ events: AuditEvent[] }>(token, "/audit", { signal }),
    enabled: page === "audit",
  });
  const browser = useQuery({
    queryKey: ["browser"],
    queryFn: ({ signal }) =>
      request<BrowserStatus>(token, "/browser", { signal }),
    refetchInterval: page === "overview" ? 10_000 : false,
  });
  async function refresh() {
    await queryClient.invalidateQueries();
  }
  const create = useMutation({
    mutationFn: () =>
      request<Device>(token, "/devices", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          node_id: nodeID || nodes.data?.nodes?.[0]?.id,
        }),
      }),
    onSuccess: async () => {
      setAdding(false);
      setName("");
      setNodeID("");
      setNotice(
        "Устройство добавлено. Покажите профиль, чтобы настроить клиент.",
      );
      await refresh();
    },
  });
  const revoke = useMutation({
    mutationFn: (device: Device) =>
      request<Device>(
        token,
        `/devices/${encodeURIComponent(device.id)}/revoke`,
        { method: "POST" },
      ),
    onSuccess: async () => {
      setRevoking(null);
      setNotice("Доступ отозван. Примените конфигурацию, чтобы обновить Xray.");
      await refresh();
    },
  });
  const config = useMutation({
    mutationFn: (action: "validate" | "apply") =>
      request<{ status: string }>(token, `/config/${action}`, {
        method: "POST",
      }),
    onSuccess: async (_, action) => {
      setNotice(
        action === "apply"
          ? "Конфигурация применена."
          : "Конфигурация прошла проверку.",
      );
      await refresh();
    },
  });
  const browserAction = useMutation({
    mutationFn: (action: "start" | "stop") =>
      request<BrowserStatus>(token, `/browser/${action}`, { method: "POST" }),
    onSuccess: async (_, action) => {
      setNotice(
        action === "start" ? "Браузер запущен." : "Браузер остановлен.",
      );
      await refresh();
    },
  });
  async function reveal(device: Device) {
    setProfileError(null);
    setProfilePending(device.id);
    setCopyError(null);
    setCopied(false);
    setShowQR(false);
    setProfileFormat("uri");
    try {
      const result = await request<ProfileExport>(
        token,
        `/devices/${encodeURIComponent(device.id)}/profile`,
      );
      setProfile({ device, ...result });
    } catch (err) {
      setProfileError(err);
    } finally {
      setProfilePending(null);
    }
  }
  async function copyProfile() {
    if (!profile) return;
    try {
      await navigator.clipboard.writeText(profile[profileFormat]);
      setCopied(true);
      setCopyError(null);
    } catch {
      setCopyError(
        new Error(
          "Буфер обмена недоступен. Выделите и скопируйте профиль вручную.",
        ),
      );
    }
  }
  function downloadProfile() {
    if (!profile || profileFormat === "uri") return;
    try {
      const file = new Blob([profile[profileFormat]], {
        type: profileFormat === "sing_box_json" ? "application/json" : "application/yaml",
      });
      const location = URL.createObjectURL(file);
      const link = document.createElement("a");
      link.href = location;
      link.download = `private-gateway-${profile.device.id}.${profileFormat === "sing_box_json" ? "json" : "yaml"}`;
      document.body.append(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(location), 1000);
      setCopyError(null);
    } catch {
      setCopyError(new Error("Не удалось скачать файл. Скопируйте конфигурацию вручную."));
    }
  }
  const list = devices.data?.devices ?? [];
  const nodeList = nodes.data?.nodes ?? [];
  const currentStatus = status.data;
  const pending =
    currentStatus &&
    currentStatus.desired_revision !== currentStatus.applied_revision;
  const browserURL =
    browser.data?.url && /^https?:\/\//.test(browser.data.url)
      ? browser.data.url
      : undefined;
  const publicBrowser = browserURL?.startsWith("https://") ?? false;
  const deviceTable = (items: Device[]) => (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            <th>Устройство</th>
            <th>Узел</th>
            <th>Создано</th>
            <th>Доступ</th>
            <th>
              <span className="sr-only">Действия</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((device) => (
            <tr key={device.id}>
              <td>
                <div className="device-name">
                  <span className="small-icon">
                    <Icon name="devices" size={18} />
                  </span>
                  <div>
                    <strong>{device.name}</strong>
                    <span className="mono secondary small">
                      {device.id.slice(0, 8)}
                    </span>
                  </div>
                </div>
              </td>
              <td>
                {nodeList.find((node) => node.id === device.node_id)?.name ||
                  device.node_id}
              </td>
              <td className="secondary">{date(device.created_at)}</td>
              <td>
                <span
                  className={`badge ${device.revoked_at ? "neutral" : "success"}`}
                >
                  <span className="dot" />
                  {device.revoked_at ? "Отозван" : "Активен"}
                </span>
              </td>
              <td>
                <div className="row-actions">
                  {!device.revoked_at && (
                    <>
                      <button
                        className="text-button"
                        onClick={() => void reveal(device)}
                        disabled={profilePending !== null}
                      >
                        {profilePending === device.id ? "Загрузка…" : "Профиль"}
                      </button>
                      <button
                        className="text-button danger-text"
                        onClick={() => {
                          revoke.reset();
                          setRevoking(device);
                        }}
                      >
                        Отозвать
                      </button>
                    </>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
  return (
    <div className="app-layout">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">
            <Icon name="lock" size={22} />
          </span>
          <span>
            private<span className="brand-light">gateway</span>
          </span>
        </div>
        <span className="sidebar-label">ПАНЕЛЬ УПРАВЛЕНИЯ</span>
        <nav aria-label="Главное меню">
          {nav.map((item) => (
            <button
              key={item.page}
              className={page === item.page ? "nav-item active" : "nav-item"}
              onClick={() => {
                setPage(item.page);
                setNotice("");
                setProfileError(null);
              }}
            >
              <Icon name={item.icon} />
              {item.label}
              {item.page === "devices" && currentStatus && (
                <span className="nav-count">{currentStatus.devices}</span>
              )}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="self-hosted">
            <span className="dot" />
            Собственная инфраструктура
          </div>
          <button className="nav-item logout" onClick={onLogout}>
            <Icon name="logout" />
            Выйти из панели
          </button>
          <span className="version">PRIVATE GATEWAY / 0.1</span>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <span>
            Рабочее пространство{" "}
            <span className="breadcrumb">
              / {nav.find((item) => item.page === page)?.label}
            </span>
          </span>
          <span className={`connection ${status.isError ? "offline" : ""}`}>
            <span className="dot" />
            {status.isError
              ? "API недоступен"
              : status.isPending
                ? "Подключение…"
                : "API подключён"}
          </span>
        </header>
        <main>
          <div className="page-heading">
            <div>
              <span className="eyebrow">PRIVATE GATEWAY</span>
              <h1>{titles[page][0]}</h1>
              <p>{titles[page][1]}</p>
            </div>
            {(page === "overview" || page === "devices") && (
              <button
                className="button primary"
                onClick={() => {
                  create.reset();
                  setAdding(true);
                }}
                disabled={!nodeList.length}
              >
                <Icon name="plus" size={18} />
                Добавить устройство
              </button>
            )}
          </div>
          {notice && (
            <div className="notice success-notice" role="status">
              <Icon name="check" size={17} />
              {notice}
              <button
                className="icon-button"
                onClick={() => setNotice("")}
                aria-label="Скрыть уведомление"
              >
                <Icon name="close" size={16} />
              </button>
            </div>
          )}
          <ErrorNotice
            error={status.error || devices.error || nodes.error || profileError}
          />
          {page === "overview" && (
            <>
              <div className="stats-grid">
                <div className="stat-card">
                  <span className="stat-label">
                    Активные устройства
                    <Icon name="devices" />
                  </span>
                  <strong>{currentStatus?.active_devices ?? "—"}</strong>
                  <span className="stat-foot">
                    Из {currentStatus?.devices ?? "—"} добавленных устройств
                  </span>
                </div>
                <div className="stat-card">
                  <span className="stat-label">
                    Узлы подключения
                    <Icon name="server" />
                  </span>
                  <strong>{nodes.isSuccess ? nodeList.length : "—"}</strong>
                  <span className="stat-foot">VLESS · TCP · REALITY</span>
                </div>
                <div className="stat-card">
                  <span className="stat-label">
                    Конфигурация
                    <Icon name="check" />
                  </span>
                  <strong className="stat-word">
                    {currentStatus ? (pending ? "Ожидает" : "Применена") : "—"}
                  </strong>
                  <span className="stat-foot">
                    {currentStatus
                      ? `Версия ${currentStatus.applied_revision} · нужна ${currentStatus.desired_revision}`
                      : "Получаем статус…"}
                  </span>
                </div>
              </div>
              <div className="overview-columns">
                <section className="card">
                  <div className="card-heading">
                    <div>
                      <h2>Конфигурация Xray</h2>
                      <p>Изменения устройств нужно применить к серверу.</p>
                    </div>
                    <span
                      className={`badge ${!currentStatus || !currentStatus.xray_configured ? "neutral" : pending ? "warning" : "success"}`}
                    >
                      {!currentStatus
                        ? "Статус неизвестен"
                        : !currentStatus.xray_configured
                          ? "Не настроен"
                          : pending
                            ? "Есть изменения"
                            : "Актуальна"}
                    </span>
                  </div>
                  <div className="config-body">
                    <div className="config-line">
                      <span>База данных</span>
                      <span
                        className={
                          currentStatus?.database === "ok"
                            ? "green-text"
                            : "secondary"
                        }
                      >
                        {currentStatus?.database === "ok"
                          ? "Подключена"
                          : (currentStatus?.database ?? "—")}
                      </span>
                    </div>
                    <div className="config-line">
                      <span>Xray</span>
                      <span>
                        {currentStatus
                          ? currentStatus.xray_configured
                            ? "Настроен"
                            : "Не настроен"
                          : "—"}
                      </span>
                    </div>
                    <div className="config-line">
                      <span>Применённая / новая версия</span>
                      <span className="mono">
                        {currentStatus?.applied_revision ?? "—"} /{" "}
                        {currentStatus?.desired_revision ?? "—"}
                      </span>
                    </div>
                    <p className="hint">
                      Проверка вызывает валидатор Xray. Применение обновляет
                      конфигурацию и перезапускает сервис.
                    </p>
                    <ErrorNotice error={config.error} />
                    <div className="button-group">
                      <button
                        className="button secondary-button"
                        onClick={() => config.mutate("validate")}
                        disabled={config.isPending}
                      >
                        Проверить
                      </button>
                      <button
                        className="button primary"
                        onClick={() => config.mutate("apply")}
                        disabled={config.isPending}
                      >
                        {config.isPending
                          ? "Выполняется…"
                          : "Применить конфигурацию"}
                      </button>
                    </div>
                  </div>
                </section>
                <section className="card">
                  <div className="card-heading">
                    <div>
                      <h2>Удалённый браузер</h2>
                      <p>Отдельный браузер на вашем сервере.</p>
                    </div>
                    <Icon name="browser" size={23} />
                  </div>
                  <div className="browser-body">
                    <span
                      className={`badge ${browser.data?.running ? "success" : "neutral"}`}
                    >
                      <span className="dot" />
                      {browser.isPending
                        ? "Получаем статус…"
                        : browser.isError
                          ? "Статус неизвестен"
                          : browser.data?.running
                            ? "Запущен"
                            : "Остановлен"}
                    </span>
                    {publicBrowser ? (
                      <p>
                        После запуска нажмите «Открыть». Для входа в браузер
                        используйте имя <strong>browser</strong> и тот же токен,
                        которым входите в панель.
                      </p>
                    ) : (
                      <p>Браузер доступен после установки Docker-стека на сервере.</p>
                    )}
                    <ErrorNotice error={browser.error || browserAction.error} />
                    <div className="button-group">
                      <button
                        className="button secondary-button"
                        onClick={() =>
                          browserAction.mutate(
                            browser.data?.running ? "stop" : "start",
                          )
                        }
                        disabled={
                          browserAction.isPending ||
                          browser.isPending ||
                          browser.isError
                        }
                      >
                        {browserAction.isPending
                          ? "Выполняется…"
                          : browser.data?.running
                            ? "Остановить"
                            : "Запустить браузер"}
                      </button>
                      {browser.data?.running && browserURL && (
                        <a
                          className="button primary"
                          href={browserURL}
                          target="_blank"
                          rel="noopener noreferrer"
                        >
                          Открыть
                          <Icon name="arrow" size={16} />
                        </a>
                      )}
                    </div>
                  </div>
                </section>
              </div>
              <section className="card">
                <div className="card-heading">
                  <div>
                    <h2>Устройства</h2>
                    <p>Последние добавленные подключения</p>
                  </div>
                  <button
                    className="text-button"
                    onClick={() => setPage("devices")}
                  >
                    Все устройства
                    <Icon name="arrow" size={16} />
                  </button>
                </div>
                {devices.isPending ? (
                  <div className="loading">Получаем устройства…</div>
                ) : list.length ? (
                  deviceTable(list.slice(0, 5))
                ) : (
                  <Empty icon="devices" title="Пока нет устройств">
                    Добавьте первое устройство, чтобы создать персональный
                    профиль подключения.
                  </Empty>
                )}
              </section>
            </>
          )}
          {page === "devices" && (
            <section className="card">
              <div className="card-heading">
                <div>
                  <h2>
                    Все устройства{" "}
                    <span className="count-badge">{list.length}</span>
                  </h2>
                  <p>Профили выдаются отдельно и содержат секрет доступа.</p>
                </div>
                <span className="badge neutral">
                  {list.filter((device) => !device.revoked_at).length} активных
                </span>
              </div>
              {devices.isPending ? (
                <div className="loading">Получаем устройства…</div>
              ) : list.length ? (
                deviceTable(list)
              ) : (
                <Empty icon="devices" title="Начните с первого устройства">
                  Добавьте телефон или компьютер и импортируйте профиль в
                  совместимый VLESS-клиент.
                </Empty>
              )}
              <div className="card-footer">
                <Icon name="lock" size={15} />
                <span>
                  После добавления или отзыва устройства примените конфигурацию
                  в разделе «Обзор».
                </span>
              </div>
            </section>
          )}
          {page === "nodes" && (
            <div className="nodes-grid">
              {nodes.isPending ? (
                <div className="loading">Получаем узлы…</div>
              ) : nodeList.length ? (
                nodeList.map((node) => (
                  <section className="card node-card" key={node.id}>
                    <div className="card-heading">
                      <div className="node-title">
                        <span className="node-icon">
                          <Icon name="server" size={26} />
                        </span>
                        <div>
                          <h2>{node.name}</h2>
                          <span className="mono small secondary">
                            {node.id}
                          </span>
                        </div>
                      </div>
                      <span className="badge neutral">REALITY</span>
                    </div>
                    <dl>
                      <div>
                        <dt>Адрес</dt>
                        <dd className="mono">
                          {node.host}:{node.port}
                        </dd>
                      </div>
                      <div>
                        <dt>Server name</dt>
                        <dd className="mono">{node.server_name}</dd>
                      </div>
                      <div>
                        <dt>Public key</dt>
                        <dd className="mono long-value">{node.public_key}</dd>
                      </div>
                      <div>
                        <dt>Short ID</dt>
                        <dd className="mono">{node.short_id}</dd>
                      </div>
                    </dl>
                    <div className="card-footer">
                      Публичные параметры узла. Секрет устройства выдаётся
                      только в его профиле.
                    </div>
                  </section>
                ))
              ) : (
                <section className="card">
                  <Empty icon="server" title="Узел не настроен">
                    Задайте параметры узла в конфигурации сервера и
                    перезапустите API.
                  </Empty>
                </section>
              )}
            </div>
          )}
          {page === "audit" && (
            <section className="card">
              <div className="card-heading">
                <div>
                  <h2>События шлюза</h2>
                  <p>Секреты и URI профилей не записываются в журнал.</p>
                </div>
                <button
                  className="button secondary-button"
                  onClick={() => void audit.refetch()}
                  disabled={audit.isFetching}
                >
                  Обновить
                </button>
              </div>
              <ErrorNotice error={audit.error} />
              {audit.isPending ? (
                <div className="loading">Получаем события…</div>
              ) : audit.data?.events?.length ? (
                <div className="table-scroll">
                  <table>
                    <thead>
                      <tr>
                        <th>Действие</th>
                        <th>Устройство</th>
                        <th>Время</th>
                      </tr>
                    </thead>
                    <tbody>
                      {audit.data.events.map((event) => (
                        <tr key={event.id}>
                          <td>
                            <span className="audit-action">
                              <Icon name="history" size={17} />
                              {actionLabel(event.action)}
                            </span>
                          </td>
                          <td className="mono small">
                            {event.device_id || "—"}
                          </td>
                          <td className="secondary">
                            {date(event.created_at)}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <Empty icon="history" title="Событий пока нет">
                  Здесь появятся действия с устройствами, конфигурацией и
                  браузером.
                </Empty>
              )}
            </section>
          )}
          <footer className="main-footer">
            <span>Private Gateway</span>
            <span>Управление собственной инфраструктурой</span>
          </footer>
        </main>
      </div>
      {adding && (
        <Modal
          title="Добавить устройство"
          onClose={() => !create.isPending && setAdding(false)}
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate();
            }}
          >
            <p className="modal-description">
              Создайте отдельный профиль для телефона или компьютера. После
              добавления примените конфигурацию Xray.
            </p>
            <label htmlFor="device-name">Название устройства</label>
            <input
              id="device-name"
              maxLength={128}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="Например, личный ноутбук"
              required
              autoFocus
            />
            <label htmlFor="device-node">Узел подключения</label>
            <select
              id="device-node"
              value={nodeID || nodeList[0]?.id || ""}
              onChange={(event) => setNodeID(event.target.value)}
              required
            >
              {nodeList.map((node) => (
                <option key={node.id} value={node.id}>
                  {node.name} — {node.host}
                </option>
              ))}
            </select>
            <ErrorNotice error={create.error} />
            <div className="modal-actions">
              <button
                type="button"
                className="button secondary-button"
                onClick={() => setAdding(false)}
                disabled={create.isPending}
              >
                Отмена
              </button>
              <button
                className="button primary"
                disabled={create.isPending || !name.trim() || !nodeList.length}
              >
                {create.isPending ? "Создаём…" : "Добавить устройство"}
              </button>
            </div>
          </form>
        </Modal>
      )}
      {revoking && (
        <Modal
          title="Отозвать доступ?"
          onClose={() => !revoke.isPending && setRevoking(null)}
        >
          <p className="modal-description">
            Устройство «{revoking.name}» больше не сможет получить профиль.
            Чтобы прекратить подключение к Xray, примените конфигурацию после
            отзыва.
          </p>
          <ErrorNotice error={revoke.error} />
          <div className="modal-actions">
            <button
              className="button secondary-button"
              onClick={() => setRevoking(null)}
              disabled={revoke.isPending}
            >
              Отмена
            </button>
            <button
              className="button danger"
              onClick={() => revoke.mutate(revoking)}
              disabled={revoke.isPending}
            >
              {revoke.isPending ? "Отзываем…" : "Отозвать доступ"}
            </button>
          </div>
        </Modal>
      )}
      {profile && (
        <Modal
          title={`Профиль: ${profile.device.name}`}
          onClose={() => {
            setProfile(null);
            setCopyError(null);
            setCopied(false);
            setShowQR(false);
          }}
        >
          <div className="notice warning-notice">
            <Icon name="lock" size={18} />
            Профиль содержит секрет доступа. Передавайте его только владельцу
            устройства.
          </div>
          <p className="modal-description">
            Для клиента с импортом ссылок выберите VLESS URI. sing-box и Mihomo
            могут использовать собственный файл конфигурации. Для нового
            устройства сначала примените конфигурацию.
          </p>
          <label htmlFor="profile-format">Формат профиля</label>
          <select
            id="profile-format"
            className="profile-format-picker"
            value={profileFormat}
            onChange={(event) => {
              setProfileFormat(event.target.value as keyof ProfileExport);
              setCopied(false);
              setShowQR(false);
            }}
          >
            <option value="uri">VLESS URI — вставить ссылку или сканировать QR</option>
            <option value="sing_box_json">sing-box — JSON-файл</option>
            <option value="mihomo_yaml">Mihomo — YAML-файл</option>
          </select>
          <label htmlFor="profile-content">
            {profileFormat === "uri" ? "Ссылка подключения" : "Конфигурация клиента"}
          </label>
          <textarea
            id="profile-content"
            className="profile-uri mono"
            value={profile[profileFormat]}
            readOnly
            rows={profileFormat === "uri" ? 5 : 12}
            spellCheck={false}
            onFocus={(event) => event.target.select()}
          />
          {profileFormat === "uri" ? (
            <button
              className="button secondary-button profile-qr-toggle"
              onClick={() => setShowQR((visible) => !visible)}
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
          {showQR && profileFormat === "uri" && <ProfileQRCode uri={profile.uri} />}
          <ErrorNotice error={copyError} />
          <div className="modal-actions">
            <button
              className="button secondary-button"
              onClick={() => {
                setProfile(null);
                setShowQR(false);
              }}
            >
              Скрыть профиль
            </button>
            {profileFormat !== "uri" && (
              <button className="button secondary-button" onClick={downloadProfile}>
                Скачать файл
              </button>
            )}
            <button
              className="button primary"
              onClick={() => void copyProfile()}
            >
              <Icon name={copied ? "check" : "copy"} size={17} />
              {copied ? "Скопировано" : profileFormat === "uri" ? "Скопировать ссылку" : "Скопировать конфигурацию"}
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}

function actionLabel(action: string) {
  const labels: Record<string, string> = {
    "device.create": "Устройство добавлено",
    "device.created": "Устройство добавлено",
    "device.revoke": "Доступ отозван",
    "device.revoked": "Доступ отозван",
    "device.profile": "Профиль выдан",
    "device.export": "Профиль выдан",
    "profile.export": "Профиль выдан",
    "config.validate": "Конфигурация проверена",
    "config.apply": "Конфигурация применена",
    "browser.start": "Браузер запущен",
    "browser.stop": "Браузер остановлен",
  };
  return labels[action] || action;
}
