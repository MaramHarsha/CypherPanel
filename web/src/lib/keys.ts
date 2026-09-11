// Keyboard vocabulary — canvas 14f. One module so that the shell's global
// shortcuts, a page's row navigation and the application tab strip agree on
// what a key means, and so the two rules every shortcut obeys are written
// once: a key pressed into a field is text, not a command; an open overlay
// owns the keyboard.
import { useCallback, useEffect, useRef, useState } from "react";

/** A key pressed into a field is text, not a command. */
export function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || typeof el.tagName !== "string") return false;
  const tag = el.tagName.toLowerCase();
  return tag === "input" || tag === "textarea" || tag === "select" || el.isContentEditable === true;
}

/**
 * An open dialog, sheet or menu owns the keyboard: a `1` behind a confirm
 * must not move the page the operator is confirming against. A menu counts —
 * its own typeahead answers to the same letters, and it does not stop the
 * event on its way past.
 */
export function overlayOpen(): boolean {
  return (
    document.querySelector('[data-state="open"]:is([role="dialog"],[role="menu"],[role="alertdialog"])') !==
    null
  );
}

/** True when a keydown should be left alone by every shortcut handler. */
export function shouldIgnoreKey(e: KeyboardEvent): boolean {
  return e.metaKey || e.ctrlKey || e.altKey || e.isComposing || isTyping(e.target) || overlayOpen();
}

/**
 * The application tabs in the order `1`–`7` addresses them (14f "1–7 · app
 * tabs"). The strip in routes/_app/projects/$projectId/applications/$appId.tsx
 * should draw from this list so a tab added there is addressable here.
 */
export const APP_TABS = [
  { segment: "", label: "Overview", short: "Overview" },
  { segment: "deployments", label: "Deployments", short: "Deploys" },
  { segment: "logs", label: "Logs", short: "Logs" },
  { segment: "env", label: "Env vars", short: "Env" },
  { segment: "previews", label: "Previews", short: "Previews" },
  { segment: "tasks", label: "Tasks", short: "Tasks" },
  { segment: "settings", label: "Settings", short: "Settings" },
] as const;

export type AppTabSegment = (typeof APP_TABS)[number]["segment"];

/** `/projects/p_…/applications/app_…` — the only route the app keys apply to. */
export const APP_ROUTE = /^\/projects\/([^/]+)\/applications\/([^/]+)/;

// ── `d` — deploy (on an app) ────────────────────────────────────────────────
//
// The shell knows the key; the application layout owns the button and the
// mutation. A window event joins them without the shell reaching three routes
// down: the shell fires `requestDeploy()` when `d` is pressed on an app route,
// and the deploy button subscribes with `useDeployShortcut(trigger)`. A deploy
// is not destructive in the card's sense — deletes and rollbacks keep their
// confirms — and the handler is the same busy-guarded path the click uses.
const DEPLOY_EVENT = "cypher:deploy-shortcut";

export function requestDeploy(): void {
  window.dispatchEvent(new Event(DEPLOY_EVENT));
}

export function useDeployShortcut(handler: () => void): void {
  const ref = useRef(handler);
  ref.current = handler;
  useEffect(() => {
    const on = () => ref.current();
    window.addEventListener(DEPLOY_EVENT, on);
    return () => window.removeEventListener(DEPLOY_EVENT, on);
  }, []);
}

// ── `j` / `k` — row navigation ──────────────────────────────────────────────
//
// Canvas 14g, TAB ORDER: "content rows (each row one stop, enter opens,
// actions inside via arrow keys)". The list is a roving-tabindex composite:
// exactly one row is in the tab order, the rest are reachable by key, and the
// controls inside a row are reached by ← → rather than by Tab.
//
// Adoption:
//   const rows = useRowNavigation();
//   <ul ref={rows}>…<li data-row>…</li>…</ul>
//
// A row that is itself a link or button opens on Enter natively. A row that is
// a plain element opens the control marked `data-row-open` (or, failing that,
// its first link or button). `j`/`k` work from anywhere on the page — they are
// what the overlay promises — while ↑ ↓ Home End only act once focus is inside
// the list, so the arrows keep scrolling the page everywhere else.

const ROW = "[data-row]";
const FOCUSABLE = 'a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])';

function isActivatable(el: HTMLElement): boolean {
  const tag = el.tagName.toLowerCase();
  return tag === "a" || tag === "button";
}

export function useRowNavigation() {
  const container = useRef<HTMLElement | null>(null);
  const current = useRef(0);
  // Re-run the tabindex sweep when the list mounts or its rows change.
  const [node, setNode] = useState<HTMLElement | null>(null);

  const rows = useCallback(
    (): HTMLElement[] => (container.current ? Array.from(container.current.querySelectorAll<HTMLElement>(ROW)) : []),
    [],
  );

  // One stop per row: the current row carries tabindex 0, every other row and
  // every control inside any row carries -1. Rows keep their own focusability
  // (a row that is an <a> was already focusable; a <li> gains it).
  const sweep = useCallback(() => {
    const all = rows();
    if (all.length === 0) return;
    if (current.current >= all.length) current.current = all.length - 1;
    all.forEach((row, i) => {
      row.tabIndex = i === current.current ? 0 : -1;
      row.querySelectorAll<HTMLElement>(FOCUSABLE).forEach((c) => {
        if (c !== row) c.tabIndex = -1;
      });
    });
  }, [rows]);

  const ref = useCallback(
    (el: HTMLElement | null) => {
      container.current = el;
      setNode(el);
    },
    [],
  );

  useEffect(() => {
    if (!node) return;
    sweep();
    const mo = new MutationObserver(sweep);
    mo.observe(node, { childList: true, subtree: true });

    const focusRow = (i: number) => {
      const all = rows();
      if (all.length === 0) return;
      const next = Math.max(0, Math.min(i, all.length - 1));
      current.current = next;
      sweep();
      all[next]?.focus();
      all[next]?.scrollIntoView({ block: "nearest" });
    };

    // Which row the focus is in right now, or -1 when it is outside the list.
    const focusedRowIndex = () => {
      const active = document.activeElement;
      if (!(active instanceof HTMLElement)) return -1;
      const row = active.closest<HTMLElement>(ROW);
      return row ? rows().indexOf(row) : -1;
    };

    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnoreKey(e)) return;
      const inList = focusedRowIndex();
      const inside = inList >= 0 && node.contains(document.activeElement);

      // j / k: from anywhere. With focus outside the list they start from the
      // row that last held it — landing where the operator left off.
      if (e.key === "j" || e.key === "k") {
        if (e.shiftKey) return;
        e.preventDefault();
        const from = inside ? inList : current.current - (e.key === "j" ? 1 : -1);
        focusRow(from + (e.key === "j" ? 1 : -1));
        return;
      }
      if (!inside) return;

      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          return focusRow(inList + 1);
        case "ArrowUp":
          e.preventDefault();
          return focusRow(inList - 1);
        case "Home":
          e.preventDefault();
          return focusRow(0);
        case "End":
          e.preventDefault();
          return focusRow(Number.MAX_SAFE_INTEGER);
        case "ArrowRight":
        case "ArrowLeft": {
          // Actions inside the row, cycling; the row itself is position 0.
          const row = rows()[inList];
          if (!row) return;
          const inner = Array.from(row.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((c) => c !== row);
          if (inner.length === 0) return;
          e.preventDefault();
          const active = document.activeElement as HTMLElement | null;
          const at = active === row ? -1 : inner.indexOf(active as HTMLElement);
          const step = e.key === "ArrowRight" ? 1 : -1;
          const nextAt = at + step;
          if (nextAt < 0 || nextAt >= inner.length) return row.focus();
          return inner[nextAt]?.focus();
        }
        case "Enter": {
          const row = rows()[inList];
          const active = document.activeElement as HTMLElement | null;
          // Enter on a control inside the row is that control's own affair;
          // Enter on a row that is a link or button is the browser's.
          if (!row || active !== row || isActivatable(row)) return;
          const open =
            row.querySelector<HTMLElement>("[data-row-open]") ?? row.querySelector<HTMLElement>("a[href], button");
          if (!open) return;
          e.preventDefault();
          open.click();
          return;
        }
      }
    };

    // Clicking into a row makes it the current stop, so the next `j` continues
    // from where the pointer left off rather than from the top.
    const onFocusIn = (e: FocusEvent) => {
      const row = (e.target as HTMLElement | null)?.closest<HTMLElement>(ROW);
      if (!row) return;
      const i = rows().indexOf(row);
      if (i >= 0 && i !== current.current) {
        current.current = i;
        sweep();
      }
    };

    window.addEventListener("keydown", onKey);
    node.addEventListener("focusin", onFocusIn);
    return () => {
      mo.disconnect();
      window.removeEventListener("keydown", onKey);
      node.removeEventListener("focusin", onFocusIn);
    };
  }, [node, rows, sweep]);

  return ref;
}
