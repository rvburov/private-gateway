import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Icon, type IconName } from "./icons";
import type { ThemePreference } from "./theme";
import { connectionLabels, type ConnectionState } from "./ConnectionStatus";

export type Page = "overview" | "devices" | "nodes" | "audit" | "guide" | "checks" | "notifications";
type NavItem = { page: Page; label: string; short: string; icon: IconName };

const items: Record<Page, NavItem> = {
  overview: { page: "overview", label: "Обзор", short: "Обзор", icon: "grid" },
  devices: { page: "devices", label: "Устройства", short: "Устройства", icon: "devices" },
  nodes: { page: "nodes", label: "Сервер", short: "Сервер", icon: "server" },
  audit: { page: "audit", label: "Журнал действий", short: "Журнал", icon: "history" },
  guide: { page: "guide", label: "Инструкции", short: "Инструкции", icon: "book" },
  checks: { page: "checks", label: "Проверка", short: "Проверка", icon: "shield" },
  notifications: { page: "notifications", label: "Уведомления", short: "Уведомления", icon: "bell" },
};
const sidebarGroups = [
  { label: "Управление", items: [items.overview, items.devices, items.nodes, items.audit] },
  { label: "Подключение", items: [items.guide, items.checks] },
  { label: "Приложение", items: [items.notifications] },
];
// Нижние вкладки на телефоне, как в ai_agent_capper: круглая кнопка посередине добавляет
// устройство, а разделы, которые не поместились, уходят в шторку «Ещё».
const tabItems = [items.overview, items.devices];
const tabItemsAfterAdd = [items.checks];
const moreItems = [items.guide, items.audit, items.nodes, items.notifications];
const themes: { value: ThemePreference; label: string; icon: IconName }[] = [
  { value: "system", label: "Авто", icon: "monitor" },
  { value: "light", label: "Светлая", icon: "sun" },
  { value: "dark", label: "Тёмная", icon: "moon" },
];

export function ThemeSwitch({
  theme,
  onTheme,
}: {
  theme: ThemePreference;
  onTheme: (theme: ThemePreference) => void;
}) {
  return (
    <div className="theme-switch" role="group" aria-label="Тема оформления">
      {themes.map((item) => (
        <button
          key={item.value}
          type="button"
          aria-pressed={theme === item.value}
          onClick={() => onTheme(item.value)}
        >
          <Icon name={item.icon} size={15} />
          {item.label}
        </button>
      ))}
    </div>
  );
}

export function Shell({
  page,
  onNavigate,
  deviceCount,
  api,
  canAdd,
  onAdd,
  onLogout,
  theme,
  onTheme,
  onInstall,
  notifications,
  children,
}: {
  page: Page;
  onNavigate: (page: Page) => void;
  deviceCount?: number;
  api: ConnectionState;
  canAdd: boolean;
  onAdd: () => void;
  onLogout: () => void;
  theme: ThemePreference;
  onTheme: (theme: ThemePreference) => void;
  onInstall: () => void;
  notifications: ReactNode;
  children: ReactNode;
}) {
  const [moreOpen, setMoreOpen] = useState(false);
  const moreButton = useRef<HTMLButtonElement>(null);
  const closeMore = useCallback(() => {
    setMoreOpen(false);
    moreButton.current?.focus();
  }, []);
  const tab = (item: NavItem) => (
    <button
      key={item.page}
      type="button"
      className="tab"
      aria-current={page === item.page ? "page" : undefined}
      onClick={() => onNavigate(item.page)}
    >
      <Icon name={item.icon} />
      {item.short}
    </button>
  );
  return (
    <div className="shell">
      <div className="sidebar-col">
        <aside className="sidebar">
          <div className="brand">
            <span className="brand-mark">
              <Icon name="lock" size={20} />
            </span>
            <span>
              private<span className="brand-light">gateway</span>
            </span>
          </div>
          <nav aria-label="Главное меню">
            {sidebarGroups.map((group) => (
              <div key={group.label}>
                <span className="nav-group-label">{group.label}</span>
                {group.items.map((item) => (
                  <button
                    key={item.page}
                    type="button"
                    className="nav-link"
                    aria-current={page === item.page ? "page" : undefined}
                    onClick={() => onNavigate(item.page)}
                  >
                    <Icon name={item.icon} />
                    {item.label}
                    {item.page === "devices" && deviceCount !== undefined && (
                      <span className="nav-count">{deviceCount}</span>
                    )}
                  </button>
                ))}
              </div>
            ))}
          </nav>
          <div className="sidebar-foot">
            <button type="button" className="nav-link" onClick={onInstall}>
              <Icon name="download" />Установить приложение
            </button>
            <ThemeSwitch theme={theme} onTheme={onTheme} />
            <button type="button" className="nav-link danger" onClick={onLogout}>
              <Icon name="logout" />
              Выйти из панели
            </button>
            <span className="version">Private Gateway · 0.1</span>
          </div>
        </aside>
      </div>
      <div className="main">
        <header className="topbar">
          <div className="topbar-title">
            <h1>{items[page].label}</h1>
          </div>
          <div className="topbar-actions">
            {notifications}
            <span className={`connection ${api === "auth" ? "warning" : api === "ok" || api === "pending" ? api : "offline"}`} role="status">
              <span className="dot" />
              {connectionLabels[api]}
            </span>
          </div>
        </header>
        <main className="view" id="view" tabIndex={-1}>
          {children}
        </main>
      </div>
      <nav className="tabbar" aria-label="Нижнее меню">
        {tabItems.map(tab)}
        <button type="button" className="tab tab-primary" onClick={onAdd} disabled={!canAdd}>
          <span className="tab-plus">
            <Icon name="plus" size={26} />
          </span>
          Добавить
        </button>
        {tabItemsAfterAdd.map(tab)}
        <button
          ref={moreButton}
          type="button"
          className="tab"
          aria-haspopup="dialog"
          aria-expanded={moreOpen}
          aria-current={moreItems.some((item) => item.page === page) ? "page" : undefined}
          onClick={() => setMoreOpen(true)}
        >
          <Icon name="more" />
          Ещё
        </button>
      </nav>
      {moreOpen && (
        <MoreSheet
          page={page}
          onNavigate={(next) => {
            onNavigate(next);
            setMoreOpen(false);
          }}
          onClose={closeMore}
          onLogout={onLogout}
          theme={theme}
          onTheme={onTheme}
          onInstall={() => { setMoreOpen(false); onInstall(); }}
        />
      )}
    </div>
  );
}

function MoreSheet({
  page,
  onNavigate,
  onClose,
  onLogout,
  theme,
  onTheme,
  onInstall,
}: {
  page: Page;
  onNavigate: (page: Page) => void;
  onClose: () => void;
  onLogout: () => void;
  theme: ThemePreference;
  onTheme: (theme: ThemePreference) => void;
  onInstall: () => void;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeButton.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="sheet-overlay" onClick={onClose}>
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-label="Ещё"
        onClick={(event) => event.stopPropagation()}
      >
        <span className="sheet-handle" aria-hidden="true" />
        <div className="sheet-head">
          <h2>Ещё</h2>
          <button ref={closeButton} type="button" className="icon-button" onClick={onClose} aria-label="Закрыть меню">
            <Icon name="close" />
          </button>
        </div>
        {moreItems.map((item) => (
          <button
            key={item.page}
            type="button"
            className="nav-link"
            aria-current={page === item.page ? "page" : undefined}
            onClick={() => onNavigate(item.page)}
          >
            <Icon name={item.icon} />
            {item.label}
          </button>
        ))}
        <button type="button" className="nav-link" onClick={onInstall}>
          <Icon name="download" />Установить приложение
        </button>
        <ThemeSwitch theme={theme} onTheme={onTheme} />
        <button type="button" className="nav-link danger" onClick={onLogout}>
          <Icon name="logout" />
          Выйти из панели
        </button>
      </div>
    </div>
  );
}
