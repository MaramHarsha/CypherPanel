// Dark is the default; light is the stored opt-in (ui-principles §9). The
// class lives on <html>; main.tsx applies it before first render.
import { useSyncExternalStore } from "react";

const STORAGE_KEY = "cypher.theme";
export type Theme = "dark" | "light";

const listeners = new Set<() => void>();

export function storedTheme(): Theme {
  try {
    return localStorage.getItem(STORAGE_KEY) === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}

export function applyTheme(theme: Theme): void {
  const el = document.documentElement;
  el.classList.toggle("dark", theme === "dark");
  el.classList.toggle("light", theme === "light");
}

export function setTheme(theme: Theme): void {
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Private mode: theme still applies for this tab.
  }
  applyTheme(theme);
  listeners.forEach((l) => l());
}

export function useTheme(): Theme {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    storedTheme,
  );
}
