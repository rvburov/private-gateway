import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { request, type Bridge, type Node } from "./api";
import { ErrorNotice, Modal, date } from "./ui";

type NodeDraft = Pick<Node, "name" | "host" | "port" | "server_name" | "public_key" | "short_id"> & {
  xhttp?: Node["xhttp"] | null; h3?: Node["h3"] | null;
};
const blankNode: NodeDraft = { name: "", host: "", port: 8443, server_name: "", public_key: "", short_id: "" };
type ConnectionDraft = { mode: "direct" | "bridge"; bridge: Bridge | null };

function NodeConnectionSettings({ node, busy, onSave, onDownload }: {
  node: Node; busy: boolean; onSave: (draft: ConnectionDraft) => void; onDownload: () => void;
}) {
  const savedMode = node.connection_mode ?? "direct";
  const [mode, setMode] = useState(savedMode);
  const [bridge, setBridge] = useState<Bridge>(node.bridge ?? { host: "", tcp_port: 443, xhttp_port: 8444 });
  useEffect(() => {
    setMode(node.connection_mode ?? "direct");
    setBridge(node.bridge ?? { host: "", tcp_port: 443, xhttp_port: 8444 });
  }, [node.connection_mode, node.bridge?.host, node.bridge?.tcp_port, node.bridge?.xhttp_port]);
  const selectedBridge: Bridge | null = mode === "bridge" ? { host: bridge.host.trim(), tcp_port: bridge.tcp_port,
    ...(node.xhttp ? { xhttp_port: bridge.xhttp_port } : {}) } : null;
  const validPort = (port: number | undefined) => Number.isInteger(port) && port! >= 1 && port! <= 65535;
  const validBridge = selectedBridge && selectedBridge.host && !/[\s/?#@]/.test(selectedBridge.host) &&
    validPort(selectedBridge.tcp_port) && (!node.xhttp || (validPort(selectedBridge.xhttp_port) && selectedBridge.xhttp_port !== selectedBridge.tcp_port));
  const changed = mode !== savedMode || (mode === "bridge" && (selectedBridge?.host !== node.bridge?.host ||
    selectedBridge?.tcp_port !== node.bridge?.tcp_port || selectedBridge?.xhttp_port !== node.bridge?.xhttp_port));
  const canSave = changed && !busy && (mode === "direct" || (mode === "bridge" && Boolean(validBridge)));
  return <form aria-label={`Подключение к ${node.name}`} onSubmit={(event) => {
    event.preventDefault();
    if (canSave) onSave({ mode, bridge: selectedBridge });
  }}>
    <p><strong>Сохранённый маршрут:</strong> {savedMode === "bridge"
      ? `Устройство → ${node.bridge?.host} → VPS → интернет`
      : savedMode === "unconfigured" ? "Не выбран — выдача профилей приостановлена" : "Устройство → VPS → интернет"}</p>
    {savedMode === "unconfigured" && <p className="notice warning-notice">Прежняя SOCKS5-цепочка удалена. Выберите прямой вход или настройте VPS-мост, затем получите обычную VPN-ссылку.</p>}
    <label className="fleet-field" htmlFor={`connection-mode-${node.id}`}>Способ подключения
      <select id={`connection-mode-${node.id}`} value={mode} disabled={busy} onChange={(event) => setMode(event.target.value as typeof mode)}>
        {savedMode === "unconfigured" && <option value="unconfigured" disabled>Выберите способ подключения</option>}
        <option value="direct">Напрямую к VPS</option>
        <option value="bridge">Через VPS-мост</option>
      </select>
    </label>
    {mode === "bridge" && <>
      <label className="fleet-field" htmlFor={`bridge-host-${node.id}`}>Адрес VPS-моста
        <input id={`bridge-host-${node.id}`} required maxLength={253} value={bridge.host} disabled={busy}
          placeholder="IP или домен без https://" onChange={(event) => setBridge({ ...bridge, host: event.target.value })} />
      </label>
      <label className="fleet-field" htmlFor={`bridge-tcp-${node.id}`}>Порт TCP/Vision на мосту
        <input id={`bridge-tcp-${node.id}`} required type="number" min={1} max={65535} value={bridge.tcp_port} disabled={busy}
          onChange={(event) => setBridge({ ...bridge, tcp_port: Number(event.target.value) })} />
      </label>
      {node.xhttp && <label className="fleet-field" htmlFor={`bridge-xhttp-${node.id}`}>Порт XHTTP на мосту
        <input id={`bridge-xhttp-${node.id}`} required type="number" min={1} max={65535} value={bridge.xhttp_port ?? ""} disabled={busy}
          onChange={(event) => setBridge({ ...bridge, xhttp_port: Number(event.target.value) })} />
      </label>}
      {node.xhttp && bridge.tcp_port === bridge.xhttp_port && <p className="hint">Для TCP/Vision и XHTTP нужны разные порты моста.</p>}
      <p className="hint">Сохранение адреса не устанавливает мост. Скачайте его настройку, установите её на входном VPS по инструкции и проверьте подключение. Сайты будут видеть IP выходного VPS.</p>
      {node.h3 && <p className="hint">Через этот TCP-мост выдаются TCP/Vision и XHTTP/REALITY. Вариант HTTP/3 (UDP) не выдаётся.</p>}
    </>}
    {mode === "direct" && <p className="hint">Устройство подключается к адресу выходного VPS.</p>}
    <p className="hint">После сохранения обновите обычную подписку в VPN-клиенте или замените ранее скопированную ссылку. Открытое соединение использует прежний маршрут до переподключения.</p>
    <div className="button-group">
      <button className="button secondary-button" disabled={!canSave}>Сохранить способ подключения</button>
      {savedMode === "bridge" && <button type="button" className="button secondary-button" disabled={busy || changed} onClick={onDownload}>Скачать настройку моста</button>}
      <a className="button secondary-button" href="https://github.com/rvburov/private-gateway/blob/master/docs/bridge.md" target="_blank" rel="noreferrer">Инструкция VPS-моста</a>
    </div>
  </form>;
}

export function NodesManager({ token, nodes }: { token: string; nodes: Node[] }) {
  const cache = useQueryClient();
  const [nodeForm, setNodeForm] = useState<{ id?: string; draft: NodeDraft } | null>(null);
  const [enrollment, setEnrollment] = useState<{ node: Node; join_token: string } | null>(null);
  const [reenrolling, setReenrolling] = useState<Node | null>(null);
  const [removing, setRemoving] = useState<{ path: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [message, setMessage] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);

  // Токен регистрации остаётся в состоянии формы, без кеша запросов и localStorage.
  async function perform(action: (signal: AbortSignal) => Promise<void>) {
    if (busy) return;
    const abort = new AbortController();
    controller.current = abort;
    setBusy(true); setError(null); setMessage("");
    try {
      await action(abort.signal);
      abort.signal.throwIfAborted();
      await Promise.all([cache.invalidateQueries({ queryKey: ["nodes"] }),
        cache.invalidateQueries({ queryKey: ["status"] })]);
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
  function openNode(node?: Node) {
    setError(null);
    setNodeForm(node ? { id: node.id, draft: { name: node.name, host: node.host, port: node.port,
      server_name: node.server_name, public_key: node.public_key, short_id: node.short_id,
      xhttp: node.xhttp ?? null, h3: node.h3 ?? null } } : { draft: { ...blankNode } });
  }
  return <>
    <div className="fleet-actions">
      <p>Подключайтесь к каждому VPS напрямую или через отдельный VPS-мост. Используйте обычные VPN-ссылки; сайты видят IP выходного VPS.</p>
      <button className="button primary" onClick={() => openNode()}>Добавить VPS</button>
    </div>
    {message && <div className="notice" role="status">{message}</div>}
    {!nodeForm && !removing && !reenrolling && <ErrorNotice error={error} />}
    <div className="nodes-grid">
      {nodes.map((node) => {
        const pending = node.desired_revision !== node.applied_revision;
        return <section className="card node-card" key={node.id}>
          <div className="card-heading"><div><h2>{node.name}</h2><p className="mono">{node.host}:{node.port}</p></div>
            <span className={`badge ${pending ? "warning" : "neutral"}`}>{pending ? "Ожидает применения" : "VPS"}</span></div>
          <div className="card-body">
            <p>{node.kind === "remote" ? `Последняя связь агента: ${node.last_seen_at ? date(node.last_seen_at) : "ещё не подключался"}` : "Сервер панели"}</p>
            <p>Применённая / нужная версия: {node.applied_revision ?? "—"} / {node.desired_revision ?? "—"}</p>
            <p>TCP + REALITY + Vision{node.xhttp ? " · XHTTP/HTTP2" : ""}{node.h3 ? " · XHTTP/HTTP3" : ""}</p>
            {node.agent_error && <p className="notice error" role="alert">Агент не применил конфигурацию: {node.agent_error}</p>}
            <NodeConnectionSettings node={node} busy={busy} onSave={(draft) => void perform(async (signal) => {
              await request(token, `/nodes/${encodeURIComponent(node.id)}/connection`, { method: "PUT",
                body: JSON.stringify(draft), signal });
              setMessage("Способ подключения сохранён. Настройте входной VPS, если выбран мост, затем обновите подписку или получите новую ссылку в «Устройствах» и переподключитесь.");
            })} onDownload={() => void perform(async (signal) => {
              const config = await request<Record<string, unknown>>(token, `/nodes/${encodeURIComponent(node.id)}/bridge-config`, { signal });
              const url = URL.createObjectURL(new Blob([JSON.stringify(config, null, 2) + "\n"], { type: "application/json" }));
              const link = document.createElement("a");
              link.href = url; link.download = `bridge-${node.id}.json`;
              document.body.append(link); link.click(); link.remove();
              window.setTimeout(() => URL.revokeObjectURL(url), 1000);
              setMessage("Настройка скачана. Установите её на VPS-мост по инструкции. После изменения адресов или портов скачайте и примените настройку заново.");
            })} />
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
      <p>Для удаления VPS сначала отзовите, примените отзыв и удалите его устройства.</p>
      <ErrorNotice error={error} /><div className="modal-actions"><button className="button secondary-button" disabled={busy} onClick={() => setRemoving(null)}>Отмена</button>
        <button className="button danger" disabled={busy} onClick={() => void perform(async (signal) => {
          await request(token, removing.path, { method: "DELETE", signal }); setRemoving(null); setMessage("Запись удалена.");
        })}>Удалить</button></div>
    </Modal>}
  </>;
}
