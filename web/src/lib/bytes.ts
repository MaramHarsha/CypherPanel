// Byte figures, in the words an operator uses.
//
// Disk is reported in three numbers — total, free, and a boolean saying the
// panel considers the host low (disk-management.md §4). Zero total means the
// figure could NOT be read: an older agent, or a host where statfs failed. The
// spec is explicit that a client shows that as unknown and never as full, so
// the type here makes "unknown" a state a caller has to handle rather than a
// zero it can accidentally render as 0% free.

/** Binary units, because this is a filesystem and `df -h` says GiB. */
const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

/** "412 GiB", "1.4 TiB", "0 B". One decimal only above the kilobyte. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  let v = n;
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${i === 0 ? Math.round(v) : v.toFixed(v >= 100 ? 0 : 1)} ${UNITS[i]}`;
}

export interface Disk {
  /** Percentage of the filesystem in use, 0–100. */
  usedPercent: number;
  freeLabel: string;
  totalLabel: string;
  /** Whether the panel has flagged this host as past its threshold. */
  low: boolean;
}

/**
 * The three raw fields as one renderable fact, or null when the figure was
 * never reported. Callers render the null as "unknown"; nothing here invents a
 * percentage from a zero.
 */
export function disk(
  total: number | undefined,
  free: number | undefined,
  low: boolean | undefined,
): Disk | null {
  if (!total || total <= 0) return null;
  const used = Math.max(0, total - (free ?? 0));
  return {
    usedPercent: Math.min(100, Math.round((used / total) * 100)),
    freeLabel: formatBytes(free ?? 0),
    totalLabel: formatBytes(total),
    low: low === true,
  };
}
