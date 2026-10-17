import { useEffect, useState } from "react";

// Пока тема не выбрана, панель следует теме системы. Хранится только сам выбор и только после
// того, как администратор его сделал; данные сеанса в хранилище браузера не попадают никогда.
export type ThemePreference = "system" | "light" | "dark";
const storageKey = "private-gateway-theme";

export function readTheme(): ThemePreference {
  try {
    const value = window.localStorage.getItem(storageKey);
    if (value === "light" || value === "dark") return value;
  } catch {
    // Хранилище может быть недоступно, например в приватном окне.
  }
  return "system";
}

export function applyTheme(theme: ThemePreference) {
  const root = document.documentElement;
  if (theme === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", theme);
}

export function useTheme(): [ThemePreference, (theme: ThemePreference) => void] {
  const [theme, setTheme] = useState<ThemePreference>(readTheme);
  useEffect(() => applyTheme(theme), [theme]);
  function choose(next: ThemePreference) {
    setTheme(next);
    try {
      if (next === "system") window.localStorage.removeItem(storageKey);
      else window.localStorage.setItem(storageKey, next);
    } catch {
      // Выбор всё равно действует в этой вкладке.
    }
  }
  return [theme, choose];
}
