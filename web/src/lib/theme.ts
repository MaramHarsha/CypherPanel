// Light is the default; dark is the stored opt-in (ui-principles §9, revised
// 2026-08-02 with the Mission Control direction). The class lives on <html>;
// main.tsx applies it before first render.
//
// Three preferences, two themes. "auto" is not a third look — it defers to the
// operating system, which is what a panel read in daylight and at 2am wants by
// default once someone thinks to ask for it (canvas 13i). Everything that only
// cares what is on screen keeps asking for the resolved theme.
import { useSyncExternalStore } from "react";

const STORAGE_KEY = "cypher.theme";
export type Theme = "dark" | "light";
export type ThemePreference = Theme | "auto";

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());

/** The system's own answer, for `auto`. */
function systemTheme(): Theme {
  try {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  } catch {
    return "light";
  }
}

export function storedPreference(): ThemePreference {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw === "dark" || raw === "light" || raw === "auto" ? raw : "light";
  } catch {
    return "light";
  }
}

/** What is actually painted right now. */
export function storedTheme(): Theme {
  const pref = storedPreference();
  return pref === "auto" ? systemTheme() : pref;
}

export function applyTheme(theme: Theme): void {
  const el = document.documentElement;
  el.classList.toggle("dark", theme === "dark");
  el.classList.toggle("light", theme === "light");
}

export function setTheme(preference: ThemePreference): void {
  try {
    localStorage.setItem(STORAGE_KEY, preference);
  } catch {
    // Private mode: the choice still applies for this tab.
  }
  applyTheme(preference === "auto" ? systemTheme() : preference);
  notify();
}

// While the preference is `auto` the system can change under us — at sunset, or
// when someone flips their laptop's appearance — and the page has to follow
// without a reload. The listener is registered once, at module load, because it
// has to outlive any component that happens to be mounted.
try {
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (storedPreference() !== "auto") return;
    applyTheme(systemTheme());
    notify();
  });
} catch {
  // No matchMedia (very old browsers, some test environments): `auto` then
  // resolves to light and simply never changes on its own.
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The theme on screen — what an icon or a swatch should reflect. */
export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, storedTheme);
}

/** The stored choice, including `auto` — what a preference control shows. */
export function useThemePreference(): ThemePreference {
  return useSyncExternalStore(subscribe, storedPreference);
}
