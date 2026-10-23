import { request } from "./api";

const notificationErrors: Record<string, string> = {
  "notifications unavailable": "Не удалось получить уведомления. Повторите позже.",
  "invalid notification IDs": "Не удалось отметить уведомления. Обновите список и повторите.",
  "all notification preferences are required": "Не удалось сохранить настройки. Обновите страницу и повторите.",
  "notification preferences unavailable": "Настройки уведомлений временно недоступны.",
  "push configuration unavailable": "Сервер пока не может включить push-уведомления. Повторите позже.",
  "invalid push subscription": "Браузер передал неверную подписку. Обновите приложение и повторите включение.",
  "push session unavailable": "Не удалось связать уведомления с вашим входом. Войдите снова.",
  "push subscription unavailable": "Не удалось изменить подписку уведомлений. Повторите позже.",
  "push subscriptions unavailable": "Не удалось получить устройства с уведомлениями. Повторите позже.",
  "invalid subscription ID": "Подписка устройства изменилась. Обновите список.",
  "push subscription not found": "Уведомления на этом устройстве уже отключены. Включите их снова.",
  "wait one minute before another test": "Повторите тест через минуту.",
  "test notification could not be delivered; check browser permission and try again": "Push-служба не приняла тест. Проверьте разрешение браузера и попробуйте включить уведомления заново.",
};

export async function notificationRequest<T>(token: string, path: string, options: RequestInit = {}): Promise<T> {
  try { return await request<T>(token, path, options); }
  catch (error) {
    if (error instanceof Error && notificationErrors[error.message]) throw new Error(notificationErrors[error.message]);
    throw error;
  }
}

export type NotificationCategory = "server" | "devices" | "configuration";
export interface GatewayNotification {
  id: string;
  category: NotificationCategory;
  title: string;
  body: string;
  created_at: string;
  read_at: string | null;
}
export interface NotificationInbox { items: GatewayNotification[]; unread: number }
export interface NotificationPreferences { server: boolean; devices: boolean; configuration: boolean }
export interface GatewayPushSubscription {
  id: string;
  endpoint_hash: string;
  device: string;
  created_at: string;
  last_sent_at: string | null;
}

export const notificationKeys = {
  inbox: ["notifications"] as const,
  preferences: ["notification-preferences"] as const,
  subscriptions: ["push-subscriptions"] as const,
};

export const notificationCategories: Array<{ value: NotificationCategory; label: string; description: string }> = [
  { value: "server", label: "Состояние сервера", description: "Проблемы служб и восстановление работы." },
  { value: "devices", label: "Устройства и доступ", description: "Добавление, отзыв и удаление устройств." },
  { value: "configuration", label: "Конфигурация", description: "Результат применения настроек Xray." },
];

export const loadNotifications = (token: string, signal?: AbortSignal) =>
  notificationRequest<NotificationInbox>(token, "/notifications", { signal });
export const loadPushSubscriptions = (token: string, signal?: AbortSignal) =>
  notificationRequest<{ items: GatewayPushSubscription[] }>(token, "/notifications/push-subscriptions", { signal });
export const deletePushSubscription = (token: string, id: string) =>
  notificationRequest<{ ok: boolean }>(token, `/notifications/push-subscriptions/${encodeURIComponent(id)}`, { method: "DELETE" });
