import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { request, type EntryProxy, type Node } from "./api";
import { ErrorNotice, Modal, date } from "./ui";

type NodeDraft = Pick<Node, "name" | "host" | "port" | "server_name" | "public_key" | "short_id"> & {
  xhttp?: Node["xhttp"] | null; h3?: Node["h3"] | null;
};
const blankNode: NodeDraft = { name: "", host: "", port: 8443, server_name: "", public_key: "", short_id: "" };
type ProxyDraft = Omit<EntryProxy, "id"> & { username: string; password: string; replace_credentials: boolean };
const blankProxy: ProxyDraft = { name: "", host: "", port: 1080, auth_type: "password", enabled: true, supports_udp: false,
  username: "", password: "", replace_credentials: true };

export function NodesManager({ token, nodes }: { token: string; nodes: Node[] }) {
  const cache = useQueryClient();
  const proxies = useQuery({ queryKey: ["entry-proxies"], queryFn: ({ signal }) =>
    request<{ entry_proxies: EntryProxy[] }>(token, "/entry-proxies", { signal }) });
  const [nodeForm, setNodeForm] = useState<{ id?: string; draft: NodeDraft } | null>(null);
  const [proxyForm, setProxyForm] = useState<{ id?: string; draft: ProxyDraft } | null>(null);
  const [enrollment, setEnrollment] = useState<{ node: Node; join_token: string } | null>(null);
  const [reenrolling, setReenrolling] = useState<Node | null>(null);
  const [removing, setRemoving] = useState<{ path: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [message, setMessage] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);

  // Пароли и токен регистрации остаются в состоянии формы, без кеша запросов и localStorage.
  async function perform(action: (signal: AbortSignal) => Promise<void>) {
    if (busy) return;
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true); setError(null); setMessage("");
    try {
      await action(abort.signal);
      abort.signal.throwIfAborted();
      await Promise.all([cache.invalidateQueries({ queryKey: ["nodes"] }),
        cache.invalidateQueries({ queryKey: ["entry-proxies"] }), cache.invalidateQueries({ queryKey: ["status"] })]);
    } catch (err) {
      if (!abort.signal.aborted) setError(err);
    } finally { if (!abort.signal.aborted) setBusy(false); }
  }
  function saveNode(event: FormEvent) {
    event.preventDefault();
    if (!nodeForm) return;
    void perform(async (signal) => {
      const result = await request<{ node: Node; join_token: string }>(token,
        nodeForm.id ? `/nodes/${encodeURIComponent(nodeForm.id)}` : "/nodes", {
          method: nodeForm.id ? "PATCH" : "POST", body: JSON.stringify(nodeForm.draft), signal,
        });
      if (!nodeForm.id) setEnrollment(result);
      setNodeForm(null);
      setMessage("Параметры VPS сохранены. Готовность подключения определяется после применения конфигурации на сервере.");
    });
  }
  function saveProxy(event: FormEvent) {
    event.preventDefault();
    if (!proxyForm) return;
    void perform(async (signal) => {
      const { username, password, replace_credentials, ...settings } = proxyForm.draft;
      const credentials = settings.auth_type === "password" && (!proxyForm.id || replace_credentials) ? { username, password } : {};
      await request(token, proxyForm.id ? `/entry-proxies/${encodeURIComponent(proxyForm.id)}` : "/entry-proxies", {
        method: proxyForm.id ? "PATCH" : "POST", body: JSON.stringify({ ...settings, ...credentials }), signal,
      });
      setProxyForm(null);
      setMessage("Входной прокси сохранён. Назначьте его нужному VPS и обновите профиль на устройстве.");
    });
  }
  const proxyList = proxies.data?.entry_proxies ?? [];
  function openNode(node?: Node) {
    setError(null);
    setNodeForm(node ? { id: node.id, draft: { name: node.name, host: node.host, port: node.port,
      server_name: node.server_name, public_key: node.public_key, short_id: node.short_id,
      xhttp: node.xhttp ?? null, h3: node.h3 ?? null } } : { draft: { ...blankNode } });
  }
  return <>
    <div className="fleet-actions">
      <p>Устройство → входной прокси → выбранный VPS → интернет. Сайты видят выходной IP VPS.</p>
      <button className="button primary" onClick={() => openNode()}>Добавить VPS</button>
    </div>
    {message && <div className="notice" role="status">{message}</div>}
    {!nodeForm && !proxyForm && !removing && !reenrolling && <ErrorNotice error={error || proxies.error} />}
    <div className="nodes-grid">
      {nodes.map((node) => {
        const pending = node.desired_revision !== node.applied_revision;
        const entry = proxyList.find((proxy) => proxy.id === node.entry_proxy_id);
        return <section className="card node-card" key={node.id}>
          <div className="card-heading"><div><h2>{node.name}</h2><p className="mono">{node.host}:{node.port}</p></div>
            <span className={`badge ${pending ? "warning" : "neutral"}`}>{pending ? "Ожидает применения" : "VPS"}</span></div>
          <div className="card-body">
            <p>{node.kind === "remote" ? `Последняя связь агента: ${node.last_seen_at ? date(node.last_seen_at) : "ещё не подключался"}` : "Сервер панели"}</p>
            <p>Применённая / нужная версия: {node.applied_revision ?? "—"} / {node.desired_revision ?? "—"}</p>
            <p>TCP + REALITY + Vision{node.xhttp ? " · XHTTP/HTTP2" : ""}{node.h3 ? " · XHTTP/HTTP3" : ""}</p>
            {node.agent_error && <p className="notice error" role="alert">Агент не применил конфигурацию: {node.agent_error}</p>}
            <label htmlFor={`entry-${node.id}`}>Входной прокси</label>
            <select id={`entry-${node.id}`} value={node.entry_proxy_id ?? ""} disabled={busy || proxies.isPending || proxies.isError}
              onChange={(event) => {
                const entryID = event.target.value;
                void perform(async (signal) => {
                  await request(token, `/nodes/${encodeURIComponent(node.id)}/entry-proxy`, { method: "PUT",
                    body: JSON.stringify({ entry_proxy_id: entryID || null }), signal });
                  setMessage("Способ подключения сохранён. Обновите профиль на устройстве; уже открытые соединения используют прежние настройки.");
                });
              }}>
              <option value="">Напрямую к VPS</option>
              {proxyList.map((proxy) => <option key={proxy.id} value={proxy.id} disabled={!proxy.enabled && proxy.id !== node.entry_proxy_id}>
                {proxy.name}{proxy.enabled ? "" : " (выключен)"}</option>)}
              {node.entry_proxy_id && !entry && <option value={node.entry_proxy_id}>Назначенный прокси недоступен</option>}
            </select>
            {node.entry_proxy_id && <p className="hint">{entry?.enabled === false ? "Прокси выключен. Подключение через него недоступно; прямой путь автоматически не включается." : "Профиль включает входной прокси. Отдельного подключения с выходом через IP прокси нет."}</p>}
            <div className="button-group">
              <button className="button secondary-button" disabled={busy} onClick={() => void perform(async (signal) => {
                await request(token, `/nodes/${encodeURIComponent(node.id)}/apply`, { method: "POST", signal });
                setMessage("Запрос принят. Дождитесь подтверждения применённой версии от VPS.");
              })}>Применить</button>
              {node.kind === "remote" && <>
                <button className="button secondary-button" disabled={busy} onClick={() => openNode(node)}>Изменить VPS</button>
                <button className="button secondary-button" disabled={busy} onClick={() => { setError(null); setReenrolling(node); }}>Переподключить агент</button>
                <button className="button secondary-button danger-text" disabled={busy} onClick={() => { setError(null); setRemoving({ path: `/nodes/${encodeURIComponent(node.id)}`, name: node.name }); }}>Удалить VPS</button>
              </>}
            </div>
          </div>
        </section>;
      })}
    </div>
    <section className="card">
      <div className="card-heading"><div><h2>Входные прокси</h2><p>Доступ до VPS. Меняйте реквизиты здесь, затем обновляйте профиль на устройствах.</p></div>
        <button className="button secondary-button" onClick={() => { setError(null); setProxyForm({ draft: { ...blankProxy } }); }}>Добавить прокси</button></div>
      <div className="card-body">
        {proxies.isPending ? <p>Загрузка прокси…</p> : proxyList.length === 0 ? <p>Входные прокси ещё не добавлены.</p> : proxyList.map((proxy) =>
          <div key={proxy.id} className="entry-proxy-row"><div><strong>{proxy.name}</strong><p className="mono">{proxy.host}:{proxy.port}</p>
            <span className="secondary small">SOCKS5 · {proxy.enabled ? "Включён" : "Выключен"} · {proxy.supports_udp ? "Заявлена поддержка UDP" : "TCP"}</span></div>
            <div className="button-group"><button className="button secondary-button" disabled={busy} onClick={() => {
              setError(null); setProxyForm({ id: proxy.id, draft: { ...proxy, username: "", password: "", replace_credentials: false } });
            }}>Изменить</button><button className="button secondary-button danger-text" disabled={busy || nodes.some((node) => node.entry_proxy_id === proxy.id)}
              title="Перед удалением снимите назначение с VPS" onClick={() => { setError(null); setRemoving({ path: `/entry-proxies/${encodeURIComponent(proxy.id)}`, name: proxy.name }); }}>Удалить</button></div>
          </div>)}
      </div>
    </section>
    {nodeForm && <Modal title={nodeForm.id ? "Изменить VPS" : "Добавить VPS"} onClose={() => !busy && setNodeForm(null)}>
      <form onSubmit={saveNode}>
        <p className="modal-description">Введите публичные параметры VPN-сервера. Для нового VPS панель выдаст одноразовый код подключения агента.</p>
        {([['name', 'Название'], ['host', 'Адрес VPS'], ['port', 'Порт TCP/Vision'], ['server_name', 'Server name REALITY'], ['public_key', 'Открытый ключ REALITY'], ['short_id', 'Short ID']] as const).map(([key, label]) =>
          <label className="fleet-field" key={key}>{label}<input required maxLength={key === "name" ? 64 : 255} type={key === "port" ? "number" : "text"}
            min={key === "port" ? 1 : undefined} max={key === "port" ? 65535 : undefined} value={nodeForm.draft[key]}
            onChange={(event) => setNodeForm({ ...nodeForm, draft: { ...nodeForm.draft, [key]: key === "port" ? Number(event.target.value) : event.target.value } })} /></label>)}
        <details><summary>Дополнительные транспорты VPS</summary>
          <label className="fleet-check"><input type="checkbox" checked={Boolean(nodeForm.draft.xhttp)} onChange={(event) => setNodeForm({ ...nodeForm,
            draft: { ...nodeForm.draft, xhttp: event.target.checked ? { port: 8444, public_key: "", short_id: "", path: "", mode: "auto" } : null } })} />XHTTP + REALITY (HTTP/2)</label>
          {nodeForm.draft.xhttp && ([['port', 'Порт XHTTP'], ['public_key', 'Открытый ключ XHTTP'], ['short_id', 'Short ID XHTTP'], ['path', 'Путь XHTTP']] as const).map(([key, label]) =>
            <label className="fleet-field" key={key}>{label}<input required type={key === "port" ? "number" : "text"} min={key === "port" ? 1 : undefined} max={key === "port" ? 65535 : undefined}
              value={nodeForm.draft.xhttp![key]} onChange={(event) => setNodeForm({ ...nodeForm, draft: { ...nodeForm.draft,
                xhttp: { ...nodeForm.draft.xhttp!, [key]: key === "port" ? Number(event.target.value) : event.target.value } } })} /></label>)}
          <label className="fleet-check"><input type="checkbox" checked={Boolean(nodeForm.draft.h3)} onChange={(event) => setNodeForm({ ...nodeForm,
            draft: { ...nodeForm.draft, h3: event.target.checked ? { port: 8444, server_name: "", path: "", mode: "auto" } : null } })} />XHTTP + TLS (HTTP/3, UDP)</label>
          {nodeForm.draft.h3 && ([['port', 'Порт HTTP/3'], ['server_name', 'Домен сертификата TLS'], ['path', 'Путь HTTP/3']] as const).map(([key, label]) =>
            <label className="fleet-field" key={key}>{label}<input required type={key === "port" ? "number" : "text"} min={key === "port" ? 1 : undefined} max={key === "port" ? 65535 : undefined}
              value={nodeForm.draft.h3![key]} onChange={(event) => setNodeForm({ ...nodeForm, draft: { ...nodeForm.draft,
                h3: { ...nodeForm.draft.h3!, [key]: key === "port" ? Number(event.target.value) : event.target.value } } })} /></label>)}
          <p className="hint">Входы и сертификат должны быть настроены на VPS. Поля описывают их для профилей клиентов; сервер подтвердит готовность после применения конфигурации.</p>
        </details>
        <ErrorNotice error={error} /><div className="modal-actions"><button type="button" className="button secondary-button" disabled={busy} onClick={() => setNodeForm(null)}>Отмена</button>
          <button className="button primary" disabled={busy}>{busy ? "Сохраняем…" : "Сохранить VPS"}</button></div>
      </form>
    </Modal>}
    {proxyForm && <Modal title={proxyForm.id ? "Изменить входной прокси" : "Добавить входной прокси"} onClose={() => !busy && setProxyForm(null)}>
      <form onSubmit={saveProxy} autoComplete="off">
        {([['name', 'Название'], ['host', 'Адрес SOCKS5'], ['port', 'Порт SOCKS5']] as const).map(([key, label]) =>
          <label className="fleet-field" key={key}>{label}<input required maxLength={key === "name" ? 64 : 255} type={key === "port" ? "number" : "text"}
            min={key === "port" ? 1 : undefined} max={key === "port" ? 65535 : undefined} value={proxyForm.draft[key]}
            onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, [key]: key === "port" ? Number(event.target.value) : event.target.value } })} /></label>)}
        <label className="fleet-field">Авторизация<select value={proxyForm.draft.auth_type} onChange={(event) => setProxyForm({ ...proxyForm,
          draft: { ...proxyForm.draft, auth_type: event.target.value as ProxyDraft["auth_type"], replace_credentials: true } })}>
          <option value="password">Логин и пароль</option><option value="none">Без логина и пароля</option></select></label>
        {proxyForm.id && proxyForm.draft.auth_type === "password" && <label className="fleet-check"><input type="checkbox" checked={proxyForm.draft.replace_credentials}
          onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, replace_credentials: event.target.checked, username: "", password: "" } })} />Заменить логин и пароль</label>}
        {proxyForm.draft.auth_type === "password" && proxyForm.draft.replace_credentials && <>
          <label className="fleet-field">Логин SOCKS5<input required maxLength={255} autoComplete="off" value={proxyForm.draft.username}
            onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, username: event.target.value } })} /></label>
          <label className="fleet-field">Пароль SOCKS5<input required maxLength={255} type="password" autoComplete="new-password" value={proxyForm.draft.password}
            onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, password: event.target.value } })} /></label>
        </>}
        <label className="fleet-check"><input type="checkbox" checked={proxyForm.draft.enabled} onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, enabled: event.target.checked } })} />Прокси включён</label>
        <label className="fleet-check"><input type="checkbox" checked={proxyForm.draft.supports_udp} onChange={(event) => setProxyForm({ ...proxyForm, draft: { ...proxyForm.draft, supports_udp: event.target.checked } })} />Провайдер прокси поддерживает UDP ASSOCIATE</label>
        <p className="hint">Для XHTTP/HTTP3 нужен рабочий UDP relay. Эта отметка задаёт возможность прокси; успешное подключение проверяется отдельно.</p>
        <ErrorNotice error={error} /><div className="modal-actions"><button type="button" className="button secondary-button" disabled={busy} onClick={() => setProxyForm(null)}>Отмена</button>
          <button className="button primary" disabled={busy}>{busy ? "Сохраняем…" : "Сохранить прокси"}</button></div>
      </form>
    </Modal>}
    {enrollment && <Modal title="Подключить агент VPS" onClose={() => setEnrollment(null)}>
      <p>Сохраните данные подключения агента для VPS «{enrollment.node.name}». Код действует 15 минут и после закрытия окна больше не показывается.</p>
      <label className="fleet-field">Идентификатор VPS<input readOnly className="mono" value={enrollment.node.id} onFocus={(event) => event.target.select()} /></label>
      <label className="fleet-field">Код подключения<textarea readOnly className="mono" value={enrollment.join_token} onFocus={(event) => event.target.select()} /></label>
      <p>После сохранения кода нажмите «Применить» в карточке VPS и запустите установку агента на этом сервере. Если код истёк, получите новый через «Переподключить агент».</p>
      <p className="hint"><a href="https://github.com/rvburov/private-gateway/blob/master/docs/managed-vps.md#несколько-vps" target="_blank" rel="noreferrer">Инструкция установки агента</a>. Пока агент не подключён и конфигурация не применена, профиль не готов к использованию.</p>
      <div className="modal-actions"><button className="button primary" onClick={() => setEnrollment(null)}>Код сохранён</button></div>
    </Modal>}
    {reenrolling && <Modal title={`Переподключить агент «${reenrolling.name}»?`} onClose={() => !busy && setReenrolling(null)}>
      <p>Будет выдан новый одноразовый код. Сохраните его и зарегистрируйте агент на VPS заново. Прежние данные регистрации больше не должны использоваться.</p>
      <ErrorNotice error={error} /><div className="modal-actions"><button className="button secondary-button" disabled={busy} onClick={() => setReenrolling(null)}>Отмена</button>
        <button className="button primary" disabled={busy} onClick={() => void perform(async (signal) => {
          const result = await request<{ join_token: string }>(token, `/nodes/${encodeURIComponent(reenrolling.id)}/rotate-join`, { method: "POST", signal });
          setEnrollment({ node: reenrolling, join_token: result.join_token }); setReenrolling(null);
        })}>Выдать новый код</button></div>
    </Modal>}
    {removing && <Modal title={`Удалить «${removing.name}»?`} onClose={() => !busy && setRemoving(null)}>
      <p>Для удаления VPS сначала отзовите, примените отзыв и удалите его устройства. Для прокси снимите назначения с серверов.</p>
      <ErrorNotice error={error} /><div className="modal-actions"><button className="button secondary-button" disabled={busy} onClick={() => setRemoving(null)}>Отмена</button>
        <button className="button danger" disabled={busy} onClick={() => void perform(async (signal) => {
          await request(token, removing.path, { method: "DELETE", signal }); setRemoving(null); setMessage("Запись удалена.");
        })}>Удалить</button></div>
    </Modal>}
  </>;
}
