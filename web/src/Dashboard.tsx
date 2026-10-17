import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  request,
  type AuditEvent,
  type BrowserStatus,
  type Device,
  type Node,
  type ProfileExport,
  type Status,
  type TrafficUsage,
} from "./api";
import { Checks } from "./Checks";
import { Guide } from "./Guide";
import { Icon } from "./icons";
import { ProfileDialog, type RevealedProfile } from "./ProfileDialog";
import { Shell, type Page } from "./Shell";
import { useTheme } from "./theme";
import { actionLabel, bytes, date, Empty, ErrorNotice, Modal } from "./ui";

const intros: Record<Page, [string, string]> = {
  overview: ["Ваш приватный шлюз", "Доступ, конфигурация и удалённый браузер — в одном месте."],
  devices: [
    "Профили устройств",
    "Отдельный профиль для каждого телефона и компьютера. Доступ можно отозвать в любой момент.",
  ],
  nodes: ["Сервер подключения", "Параметры сервера для подключения по VLESS + REALITY."],
  audit: [
    "История действий",
    "Изменения доступа и конфигурации шлюза. Секреты и ссылки в журнал не попадают.",
  ],
  guide: [
    "Как подключить устройство",
    "Пошагово для вашей системы: приложение, ссылка-подписка, режимы и проверка. А ещё — как настроить браузер и что может выдать вас и с VPN.",
  ],
  checks: [
    "Проверка подключения",
    "Что видят сайты, когда вы подключены, и всё ли в порядке на сервере. Проверка идёт только по кнопке, с того устройства, где открыта панель.",
  ],
};

function PageIntro({ page, action }: { page: Page; action?: ReactNode }) {
  return (
    <div className="page-intro">
      <div>
        <h2>{intros[page][0]}</h2>
        <p>{intros[page][1]}</p>
      </div>
      {action}
    </div>
  );
}

export function Dashboard({ token, onLogout }: { token: string; onLogout: () => void }) {
  const queryClient = useQueryClient();
  const [theme, setTheme] = useTheme();
  const [page, setPage] = useState<Page>("overview");
  const [adding, setAdding] = useState(false);
  const [revoking, setRevoking] = useState<Device | null>(null);
  const [deleting, setDeleting] = useState<Device | null>(null);
  const [profile, setProfile] = useState<RevealedProfile | null>(null);
  const [profilePending, setProfilePending] = useState<string | null>(null);
  const [profileError, setProfileError] = useState<unknown>(null);
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
    queryFn: ({ signal }) => request<{ devices: Device[] }>(token, "/devices", { signal }),
  });
  const usage = useQuery({
    queryKey: ["device-traffic"],
    queryFn: ({ signal }) => request<TrafficUsage>(token, "/devices/traffic", { signal }),
    enabled: page === "devices",
    refetchInterval: page === "devices" ? 15_000 : false,
  });
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: ({ signal }) => request<{ nodes: Node[] }>(token, "/nodes", { signal }),
  });
  const audit = useQuery({
    queryKey: ["audit"],
    queryFn: ({ signal }) => request<{ events: AuditEvent[] }>(token, "/audit", { signal }),
    enabled: page === "audit",
  });
  const browser = useQuery({
    queryKey: ["browser"],
    queryFn: ({ signal }) => request<BrowserStatus>(token, "/browser", { signal }),
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
      setNotice("Устройство добавлено. Покажите профиль, чтобы настроить клиент.");
      await refresh();
    },
  });
  const revoke = useMutation({
    mutationFn: (device: Device) =>
      request<Device>(token, `/devices/${encodeURIComponent(device.id)}/revoke`, { method: "POST" }),
    onSuccess: async () => {
      setRevoking(null);
      setNotice("Доступ отозван. Примените конфигурацию, чтобы обновить Xray.");
      await refresh();
    },
  });
  const removeDevice = useMutation({
    mutationFn: (device: Device) =>
      request<{ status: string }>(token, `/devices/${encodeURIComponent(device.id)}`, {
        method: "DELETE",
      }),
    onSuccess: async () => {
      setDeleting(null);
      setNotice("Отозванное устройство удалено из списка. События остались в журнале.");
      await refresh();
    },
  });
  const config = useMutation({
    mutationFn: (action: "validate" | "apply") =>
      request<{ status: string }>(token, `/config/${action}`, { method: "POST" }),
    onSuccess: async (_, action) => {
      setNotice(action === "apply" ? "Конфигурация применена." : "Конфигурация прошла проверку.");
      await refresh();
    },
    onError: async (_, action) => {
      if (action === "apply") {
        // Собственный VPN администратора может оборвать ответ, пока Xray перезапускается.
        await queryClient.invalidateQueries({ queryKey: ["status"] });
      }
    },
  });
  const browserAction = useMutation({
    mutationFn: (action: "start" | "stop") =>
      request<BrowserStatus>(token, `/browser/${action}`, { method: "POST" }),
    onSuccess: async (_, action) => {
      setNotice(action === "start" ? "Браузер запущен." : "Браузер остановлен.");
      await refresh();
    },
  });
  async function reveal(device: Device) {
    setProfileError(null);
    setProfilePending(device.id);
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
  function navigate(next: Page) {
    setPage(next);
    setNotice("");
    setProfileError(null);
  }
  function startAdding() {
    create.reset();
    setAdding(true);
  }

  const list = devices.data?.devices ?? [];
  const nodeList = nodes.data?.nodes ?? [];
  const currentStatus = status.data;
  const pending = currentStatus && currentStatus.desired_revision !== currentStatus.applied_revision;
  const browserURL =
    browser.data?.url && /^https?:\/\//.test(browser.data.url) ? browser.data.url : undefined;
  const publicBrowser = browserURL?.startsWith("https://") ?? false;
  const addButton = (
    <button className="button primary" onClick={startAdding} disabled={!nodeList.length}>
      <Icon name="plus" size={18} />
      Добавить устройство
    </button>
  );
  const deviceList = (items: Device[], showTraffic: boolean) => (
    <ul className={`item-list device-list${showTraffic ? "" : " no-traffic"}`}>
      <li className="item-row item-head" aria-hidden="true">
        <span>Устройство</span>
        <span>Сервер</span>
        <span>Создано</span>
        <span>Доступ</span>
        {showTraffic && <span>Трафик ↑ / ↓</span>}
        <span />
      </li>
      {items.map((device) => (
        <li key={device.id} className="item-row">
          <div className="cell cell-main device-name">
            <span className="small-icon">
              <Icon name="devices" size={18} />
            </span>
            <div>
              <strong>{device.name}</strong>
              <span className="mono secondary small">{device.id.slice(0, 8)}</span>
            </div>
          </div>
          <div className="cell cell-meta small">
            <span className="cell-label">Сервер: </span>
            <span>{nodeList.find((node) => node.id === device.node_id)?.name || device.node_id}</span>
          </div>
          <div className="cell cell-meta small secondary">
            <span className="cell-label">Создано: </span>
            <span>{date(device.created_at)}</span>
          </div>
          <div className="cell cell-badge">
            <span className={`badge ${device.revoked_at ? "neutral" : "success"}`}>
              <span className="dot" />
              {device.revoked_at ? "Отозван" : "Активен"}
            </span>
          </div>
          {showTraffic && (
            <div className="cell cell-meta small secondary">
              <span className="cell-label">Трафик ↑ / ↓: </span>
              <span>
                {device.revoked_at
                  ? "—"
                  : usage.data
                    ? `${bytes(usage.data.devices[device.id]?.uplink_bytes ?? 0)} / ${bytes(usage.data.devices[device.id]?.downlink_bytes ?? 0)}`
                    : "…"}
              </span>
            </div>
          )}
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
            {device.revoked_at && (
              <button
                className="text-button danger-text"
                onClick={() => {
                  removeDevice.reset();
                  setDeleting(device);
                }}
                disabled={!!pending || status.isError || !currentStatus}
                title={pending ? "Сначала примените конфигурацию Xray" : undefined}
              >
                Удалить
              </button>
            )}
          </div>
        </li>
      ))}
    </ul>
  );

  return (
    <Shell
      page={page}
      onNavigate={navigate}
      deviceCount={currentStatus?.devices}
      api={status.isError ? "error" : status.isPending ? "pending" : "ok"}
      canAdd={nodeList.length > 0}
      onAdd={startAdding}
      onLogout={onLogout}
      theme={theme}
      onTheme={setTheme}
    >
      <PageIntro
        page={page}
        action={page === "overview" || page === "devices" ? addButton : undefined}
      />
      {notice && (
        <div className="notice success-notice" role="status">
          <Icon name="check" size={17} />
          {notice}
          <button className="icon-button" onClick={() => setNotice("")} aria-label="Скрыть уведомление">
            <Icon name="close" size={16} />
          </button>
        </div>
      )}
      <ErrorNotice error={status.error || devices.error || nodes.error || profileError} />
      {page === "overview" && (
        <>
          <div className="stats-grid">
            <div className="card stat-card">
              <span className="stat-label">
                Активные устройства
                <Icon name="devices" />
              </span>
              <strong>{currentStatus?.active_devices ?? "—"}</strong>
              <span className="stat-foot">Из {currentStatus?.devices ?? "—"} добавленных устройств</span>
            </div>
            <div className="card stat-card">
              <span className="stat-label">
                Серверы подключения
                <Icon name="server" />
              </span>
              <strong>{nodes.isSuccess ? nodeList.length : "—"}</strong>
              <span className="stat-foot">VLESS · TCP · REALITY</span>
            </div>
            <div className="card stat-card">
              <span className="stat-label">
                Конфигурация
                <Icon name="check" />
              </span>
              <strong>{currentStatus ? (pending ? "Ожидает" : "Применена") : "—"}</strong>
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
              <div className="card-body">
                <div className="config-line">
                  <span>База данных</span>
                  <span className={currentStatus?.database === "ok" ? "green-text" : "secondary"}>
                    {currentStatus?.database === "ok" ? "Подключена" : (currentStatus?.database ?? "—")}
                  </span>
                </div>
                <div className="config-line">
                  <span>Xray</span>
                  <span>
                    {currentStatus ? (currentStatus.xray_configured ? "Настроен" : "Не настроен") : "—"}
                  </span>
                </div>
                <div className="config-line">
                  <span>Применённая / новая версия</span>
                  <span className="mono">
                    {currentStatus?.applied_revision ?? "—"} / {currentStatus?.desired_revision ?? "—"}
                  </span>
                </div>
                <p className="hint">
                  Проверка вызывает валидатор Xray. Применение обновляет конфигурацию и перезапускает
                  сервис.
                </p>
                <ErrorNotice
                  error={config.variables === "apply" && !pending && currentStatus ? null : config.error}
                />
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
                    disabled={config.isPending || !pending || status.isError}
                  >
                    {config.isPending ? "Выполняется…" : "Применить конфигурацию"}
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
              <div className="card-body">
                <div>
                  <span className={`badge ${browser.data?.running ? "success" : "neutral"}`}>
                    <span className="dot" />
                    {browser.isPending
                      ? "Получаем статус…"
                      : browser.isError
                        ? "Статус неизвестен"
                        : browser.data?.running
                          ? "Запущен"
                          : "Остановлен"}
                  </span>
                </div>
                {publicBrowser ? (
                  <p className="hint">
                    После запуска нажмите «Открыть». Доступ использует ваш вход администратора в панель;
                    отдельный пароль не нужен.
                  </p>
                ) : (
                  <p className="hint">Браузер доступен после установки Docker-стека на сервере.</p>
                )}
                <ErrorNotice error={browser.error || browserAction.error} />
                <div className="button-group">
                  <button
                    className="button secondary-button"
                    onClick={() => browserAction.mutate(browser.data?.running ? "stop" : "start")}
                    disabled={browserAction.isPending || browser.isPending || browser.isError}
                  >
                    {browserAction.isPending
                      ? "Выполняется…"
                      : browser.data?.running
                        ? "Остановить"
                        : "Запустить браузер"}
                  </button>
                  {browser.data?.running && browserURL && (
                    <a className="button primary" href={browserURL} target="_blank" rel="noopener noreferrer">
                      Открыть
                      <Icon name="arrow" size={16} />
                    </a>
                  )}
                </div>
              </div>
            </section>
          </div>
          <section className="card guide-banner">
            <div>
              <span className="node-icon">
                <Icon name="book" size={22} />
              </span>
              <div>
                <h2 className="guide-subtitle">Как подключить телефон или компьютер</h2>
                <p>
                  Пошагово для Android, iPhone, Windows, macOS и Linux, и как не выдать себя в браузере. А
                  потом — проверка одной кнопкой.
                </p>
              </div>
            </div>
            <div className="button-group">
              <button className="button secondary-button" onClick={() => navigate("guide")}>
                Открыть инструкции
              </button>
              <button className="button secondary-button" onClick={() => navigate("checks")}>
                Проверить подключение
              </button>
            </div>
          </section>
          <section className="card">
            <div className="card-heading">
              <div>
                <h2>Устройства</h2>
                <p>Последние добавленные подключения</p>
              </div>
              <button className="text-button" onClick={() => navigate("devices")}>
                Все устройства
                <Icon name="arrow" size={16} />
              </button>
            </div>
            {devices.isPending ? (
              <div className="loading">Получаем устройства…</div>
            ) : list.length ? (
              deviceList(list.slice(0, 5), false)
            ) : (
              <Empty icon="devices" title="Пока нет устройств">
                Добавьте первое устройство, чтобы создать персональный профиль подключения.
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
                Все устройства <span className="count-badge">{list.length}</span>
              </h2>
              <p>
                Профили выдаются отдельно и содержат секрет доступа. Трафик ↑ / ↓ считается с последнего
                запуска Xray; после применения конфигурации счётчики обнуляются.
              </p>
            </div>
            <span className="badge neutral">
              {list.filter((device) => !device.revoked_at).length} активных
            </span>
          </div>
          {devices.isPending ? (
            <div className="loading">Получаем устройства…</div>
          ) : list.length ? (
            deviceList(list, true)
          ) : (
            <Empty icon="devices" title="Начните с первого устройства">
              Добавьте телефон или компьютер и импортируйте профиль в совместимый VLESS-клиент.
            </Empty>
          )}
          {usage.isError && (
            <div className="card-body">
              <div className="notice error" role="alert">
                Не удалось получить счётчики трафика Xray.
              </div>
            </div>
          )}
          <div className="card-footer">
            <Icon name="lock" size={15} />
            <span>После добавления или отзыва устройства примените конфигурацию в разделе «Обзор».</span>
          </div>
        </section>
      )}
      {page === "nodes" && (
        <div className="nodes-grid">
          {nodes.isPending ? (
            <div className="loading">Получаем параметры сервера…</div>
          ) : nodeList.length ? (
            nodeList.map((node) => (
              <section className="card node-card" key={node.id}>
                <div className="card-heading">
                  <div className="node-title">
                    <span className="node-icon">
                      <Icon name="server" size={24} />
                    </span>
                    <div>
                      <h2>{node.name}</h2>
                      <span className="mono small secondary">{node.id}</span>
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
                    <dd className="mono">{node.public_key}</dd>
                  </div>
                  <div>
                    <dt>Short ID</dt>
                    <dd className="mono">{node.short_id}</dd>
                  </div>
                </dl>
                <div className="card-footer">
                  Публичные параметры сервера. Секрет устройства выдаётся только в его профиле.
                </div>
              </section>
            ))
          ) : (
            <section className="card">
              <Empty icon="server" title="Сервер не настроен">
                Задайте параметры узла в конфигурации сервера и перезапустите API.
              </Empty>
            </section>
          )}
        </div>
      )}
      {page === "guide" && <Guide onCheck={() => navigate("checks")} />}
      {page === "checks" && <Checks token={token} nodeHost={nodeList[0]?.host} />}
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
            <ul className="item-list audit-list">
              <li className="item-row item-head" aria-hidden="true">
                <span>Действие</span>
                <span>Устройство</span>
                <span>Время</span>
              </li>
              {audit.data.events.map((event) => (
                <li key={event.id} className="item-row">
                  <div className="cell cell-main">
                    <span className="audit-action">
                      <Icon name="history" size={17} />
                      {actionLabel(event.action)}
                    </span>
                  </div>
                  <div className="cell cell-meta mono small secondary">
                    <span className="cell-label">Устройство: </span>
                    <span>{event.device_id || "—"}</span>
                  </div>
                  <div className="cell cell-badge small secondary">{date(event.created_at)}</div>
                </li>
              ))}
            </ul>
          ) : (
            <Empty icon="history" title="Событий пока нет">
              Здесь появятся действия с устройствами, конфигурацией и браузером.
            </Empty>
          )}
        </section>
      )}
      {adding && (
        <Modal title="Добавить устройство" onClose={() => !create.isPending && setAdding(false)}>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate();
            }}
          >
            <p className="modal-description">
              Создайте отдельный профиль для телефона или компьютера. После добавления примените
              конфигурацию Xray.
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
            <label htmlFor="device-node">Сервер подключения</label>
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
        <Modal title="Отозвать доступ?" onClose={() => !revoke.isPending && setRevoking(null)}>
          <p className="modal-description">
            Устройство «{revoking.name}» больше не сможет получить профиль. Чтобы прекратить
            подключение к Xray, примените конфигурацию после отзыва.
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
      {deleting && (
        <Modal title="Удалить устройство?" onClose={() => !removeDevice.isPending && setDeleting(null)}>
          <p className="modal-description">
            Отозванное устройство «{deleting.name}» будет удалено из списка. Его профиль восстановить
            нельзя. В журнале останется запись с идентификатором устройства.
          </p>
          <ErrorNotice error={removeDevice.error} />
          <div className="modal-actions">
            <button
              className="button secondary-button"
              onClick={() => setDeleting(null)}
              disabled={removeDevice.isPending}
            >
              Отмена
            </button>
            <button
              className="button danger"
              onClick={() => removeDevice.mutate(deleting)}
              disabled={removeDevice.isPending}
            >
              {removeDevice.isPending ? "Удаляем…" : "Удалить устройство"}
            </button>
          </div>
        </Modal>
      )}
      {profile && (
        <ProfileDialog
          token={token}
          profile={profile}
          onChange={setProfile}
          onClose={() => setProfile(null)}
          onGuide={() => {
            setProfile(null);
            navigate("guide");
          }}
        />
      )}
    </Shell>
  );
}
