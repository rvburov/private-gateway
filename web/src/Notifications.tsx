import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { HelpTip } from "./HelpTip";
import { Icon } from "./icons";
import {
  deletePushSubscription, loadNotifications, loadPushSubscriptions, notificationCategories, notificationKeys, notificationRequest as request,
  type GatewayPushSubscription, type NotificationPreferences,
} from "./notifications";
import { browserPushState, disableDevicePush, enableDevicePush, inspectDevicePush, type DevicePush } from "./push";
import { date, ErrorNotice } from "./ui";

function useInbox(token: string) {
  return useQuery({ queryKey: notificationKeys.inbox, queryFn: ({ signal }) => loadNotifications(token, signal), refetchInterval: 30_000, enabled: !!token });
}

export function NotificationBell({ token, onOpen }: { token: string; onOpen: () => void }) {
  const inbox = useInbox(token);
  const count = inbox.data?.unread ?? 0;
  return (
    <button type="button" className="icon-button notifications-bell" onClick={onOpen}
      aria-label={count ? `Уведомления: непрочитанных ${count}` : "Уведомления"}>
      <svg width="21" height="21" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4" />
      </svg>
      {count > 0 && <span className="notifications-count" aria-hidden="true">{count > 99 ? "99+" : count}</span>}
    </button>
  );
}

const pushDescriptions = {
  off: "Уведомления на этом устройстве выключены.",
  on: "Уведомления на этом устройстве включены.",
  denied: "Уведомления запрещены в настройках браузера.",
  ios: "На iPhone и iPad откройте приложение с экрана «Домой».",
  unsupported: "Этот браузер не поддерживает push-уведомления.",
};

export function Notifications({ token, onInstall }: { token: string; onInstall?: () => void }) {
  const client = useQueryClient();
  const inbox = useInbox(token);
  const preferences = useQuery({
    queryKey: notificationKeys.preferences,
    queryFn: ({ signal }) => request<NotificationPreferences>(token, "/notifications/preferences", { signal }),
  });
  const subscriptions = useQuery({ queryKey: notificationKeys.subscriptions, queryFn: ({ signal }) => loadPushSubscriptions(token, signal) });
  const [draft, setDraft] = useState<NotificationPreferences | null>(null);
  const [device, setDevice] = useState<DevicePush | null>(null);
  const [deviceError, setDeviceError] = useState<unknown>(null);
  const [feedback, setFeedback] = useState("");
  const [checking, setChecking] = useState(true);
  const [revision, setRevision] = useState(0);
  const known = subscriptions.data?.items ?? [];
  useEffect(() => {
    let active = true;
    setChecking(true);
    setDeviceError(null);
    void inspectDevicePush(subscriptions.data?.items ?? [])
      .then((value) => { if (active) setDevice(value); })
      .catch((error: unknown) => { if (active) setDeviceError(error); })
      .finally(() => { if (active) setChecking(false); });
    return () => { active = false; };
  }, [subscriptions.data, revision]);
  const refreshDevice = async () => {
    await client.invalidateQueries({ queryKey: notificationKeys.subscriptions });
    setRevision((value) => value + 1);
  };
  const read = useMutation({
    mutationFn: (ids?: string[]) => request(token, "/notifications/read", { method: "POST", body: JSON.stringify(ids ? { ids } : {}) }),
    onSuccess: () => client.invalidateQueries({ queryKey: notificationKeys.inbox }),
  });
  const save = useMutation({
    mutationFn: (value: NotificationPreferences) => request<NotificationPreferences>(token, "/notifications/preferences", { method: "PUT", body: JSON.stringify(value) }),
    onSuccess: (value) => {
      client.setQueryData(notificationKeys.preferences, value);
      setDraft(null);
    },
  });
  const pushAction = useMutation({
    mutationFn: async ({ action, item, enabling }: { action: "enable" | "disable" | "test" | "revoke"; item?: GatewayPushSubscription; enabling?: Promise<GatewayPushSubscription> }) => {
      if (action === "enable") await enabling;
      else if (action === "disable") await disableDevicePush(token, item, device?.subscription ?? null);
      else if (action === "revoke" && item) await deletePushSubscription(token, item.id);
      else if (action === "test" && item) await request(token, "/notifications/test", { method: "POST", body: JSON.stringify({ subscription_id: item.id }) });
      return action;
    },
    onMutate: () => { setFeedback(""); setDeviceError(null); },
    onSuccess: (action) => setFeedback(action === "enable" ? "Уведомления включены." : action === "test" ? "Тестовое уведомление отправлено в push-службу. Доставку проверьте на устройстве." : "Уведомления отключены."),
    onSettled: refreshDevice,
  });
  const state = device?.state ?? browserPushState();
  const current = known.find((item) => item.endpoint_hash === device?.hash);
  const values = draft ?? preferences.data;
  const busy = pushAction.isPending || checking || subscriptions.isFetching;
  return (
    <div className="notifications-layout">
      <section className="card" aria-label="Центр уведомлений">
        <div className="card-heading">
          <div><h2>Последние события</h2><p>{inbox.data?.unread ? `Непрочитанных: ${inbox.data.unread}` : "События вашего шлюза."}</p></div>
          <button className="button" disabled={read.isPending || !inbox.data?.unread} onClick={() => read.mutate(undefined)}>Прочитать все</button>
        </div>
        <ErrorNotice error={inbox.error || read.error} />
        {inbox.isPending ? <div className="loading">Получаем уведомления…</div> : inbox.data?.items.length ? (
          <ul className="notifications-list">
            {inbox.data.items.map((item) => (
              <li key={item.id} className={item.read_at ? "notification-item" : "notification-item notification-unread"}>
                <div className="notification-content"><h3>{item.title}</h3><p>{item.body}</p><time dateTime={item.created_at}>{date(item.created_at)}</time></div>
                {!item.read_at && <button className="icon-button" disabled={read.isPending} onClick={() => read.mutate([item.id])} aria-label={`Прочитано: ${item.title}`}><Icon name="check" size={18} /></button>}
              </li>
            ))}
          </ul>
        ) : !inbox.isError && <p className="card-body hint">Новых событий пока нет.</p>}
      </section>
      <section className="card" aria-label="Уведомления на этом устройстве">
        <div className="card-heading"><div>
          <div className="diagnostics-title-with-help"><h2>На этом устройстве</h2><HelpTip label="О push-уведомлениях">
            <p>Уведомления приходят через push-службу браузера, даже когда панель закрыта. На экране блокировки показано, что случилось, без имён устройств, профилей, ключей и IP-адресов.</p>
            <p>На iPhone и iPad нужны iOS / iPadOS 16.4 или новее и установленное приложение с экрана «Домой». Разрешение можно изменить в настройках сайта или системы.</p>
            <p>Подписка действует до окончания входа; с «Запомнить вход» — до 30 дней. Выход из панели отключает уведомления этого устройства.</p>
            <p>Полное отключение VPS требует внешнего мониторинга: выключенный сервер не может отправить уведомление.</p>
          </HelpTip></div>
          <p>{checking ? "Проверяем состояние…" : pushDescriptions[state]}</p>
        </div></div>
        <div className="card-body">
          <ErrorNotice error={subscriptions.error || deviceError || pushAction.error} />
          <div className="button-group push-device-actions">
            {state === "off" && <button className="button primary" disabled={busy || subscriptions.isError || !!deviceError} onClick={() => {
              const enabling = enableDevicePush(token, known);
              pushAction.mutate({ action: "enable", enabling });
            }}>Включить уведомления</button>}
            {state === "on" && current && <>
              <button className="button" disabled={busy} onClick={() => pushAction.mutate({ action: "test", item: current })}>Тестовое уведомление</button>
              <button className="button" disabled={busy} onClick={() => pushAction.mutate({ action: "disable", item: current })}>Выключить на этом устройстве</button>
            </>}
            {state === "off" && device?.subscription && <button className="button" disabled={busy} onClick={() => pushAction.mutate({ action: "disable", item: current })}>Отключить подписку браузера</button>}
            {state === "ios" && onInstall && <button className="button primary" onClick={onInstall}>Установить приложение</button>}
            {state === "denied" && <p className="hint">Разрешите уведомления для этого сайта в настройках браузера, затем обновите страницу.</p>}
          </div>
          {feedback && <p role="status" className="hint">{feedback}</p>}
          {(deviceError || subscriptions.isError) && <button className="button" disabled={checking || subscriptions.isFetching} onClick={() => void refreshDevice()}>Проверить состояние снова</button>}
        </div>
      </section>
      <section className="card" aria-label="Настройки уведомлений">
        <div className="card-heading"><div><h2>О чём сообщать</h2><p>В панели и на включённых устройствах.</p></div></div>
        <div className="card-body">
          <ErrorNotice error={preferences.error || save.error} />
          {values ? <form className="notification-preferences" onSubmit={(event) => { event.preventDefault(); setFeedback(""); save.mutate(values); }}>
            {notificationCategories.map((category) => <label className="notification-preference" key={category.value}>
              <input type="checkbox" checked={values[category.value]} disabled={save.isPending} onChange={(event) => { save.reset(); setFeedback(""); setDraft({ ...values, [category.value]: event.target.checked }); }} />
              <span><strong>{category.label}</strong><span>{category.description}</span></span>
            </label>)}
            <button className="button primary" type="submit" disabled={save.isPending || draft === null}>{save.isPending ? "Сохраняем…" : "Сохранить настройки"}</button>
            {save.isSuccess && draft === null && <p className="hint" role="status">Настройки сохранены.</p>}
          </form> : preferences.isPending && <p className="hint">Получаем настройки…</p>}
        </div>
      </section>
      <section className="card" aria-label="Устройства с уведомлениями">
        <div className="card-heading"><div><h2>Подключённые устройства</h2><p>Push можно отключить отдельно.</p></div></div>
        <ErrorNotice error={subscriptions.error} />
        {known.length ? <ul className="notifications-list">{known.map((item) => <li className="notification-item" key={item.id}>
          <div className="notification-content"><h3>{item.device || "Браузер"}{item.id === current?.id ? " · это устройство" : ""}</h3><p>Подключено {date(item.created_at)}</p></div>
          <button className="text-button" disabled={busy} onClick={() => pushAction.mutate({ action: item.id === current?.id ? "disable" : "revoke", item })} aria-label={`Отключить уведомления: ${item.device || "Браузер"}`}>Отключить</button>
        </li>)}</ul> : <p className="card-body hint">{subscriptions.isPending ? "Получаем устройства…" : "Уведомления ещё нигде не включены."}</p>}
      </section>
    </div>
  );
}
