// Timestamps: relative in display, absolute on hover, always UTC-safe
// (ui-principles §10).

export function relativeTime(iso: string | undefined | null): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "—";
  const s = Math.round((Date.now() - then) / 1000);
  if (s < 0) return "just now";
  if (s < 45) return "just now";
  if (s < 90) return "1m ago";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  const mo = Math.round(d / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.round(mo / 12)}y ago`;
}

/**
 * The forward half. `relativeTime` is past-tense only — a future timestamp
 * falls through its `s < 45` branch and reads "just now", which made every
 * session, token and preview on screen claim to expire this second. Anything
 * whose whole value is *when it stops working* wants this one instead.
 */
export function timeUntil(iso: string | undefined | null): string {
  if (!iso) return "—";
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "—";
  const s = Math.round((at - Date.now()) / 1000);
  if (s <= 0) return "expired";
  if (s < 60) return `in ${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `in ${m}m`;
  const h = Math.round(m / 60);
  if (h < 24) return `in ${h}h`;
  const d = Math.round(h / 24);
  if (d < 30) return `in ${d}d`;
  return `in ${Math.round(d / 30)}mo`;
}

/**
 * How long something has been true, with no tense attached — "3h", "2d".
 * `relativeTime` reads "3h ago", which is right for an event that happened and
 * wrong for a state that is still happening: a maintenance page is not "up 3h
 * ago", it is up, and has been for 3h (app-access-control.md §10).
 */
export function elapsedSince(iso: string | undefined | null): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "—";
  const s = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d`;
  return `${Math.round(d / 30)}mo`;
}

export function absoluteTime(iso: string | undefined | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC");
}
