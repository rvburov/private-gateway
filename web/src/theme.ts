import { useEffect, useState } from "react";

// Without a choice the panel follows the system theme. Only the choice itself is stored, and only
// when the administrator changes it; no session data ever goes to browser storage.
export type ThemePreference = "system" | "light" | "dark";
const storageKey = "private-gateway-theme";

export function readTheme(): ThemePreference {
  try {
    const value = window.localStorage.getItem(storageKey);
    if (value === "light" || value === "dark") return value;
  } catch {
    // Storage can be unavailable, for example in a private window.
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
      // The choice still applies to this tab.
    }
  }
  return [theme, choose];
}
