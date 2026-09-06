// StatusBadge — the single source of status rendering (web-ui-design.md §6).
// One vocabulary everywhere (ui-principles §5). Mission Control gives the
// marker a shape as well as a color, so status survives a color-blind reader
// and a phone in sunlight: `error` is a square, everything else is a dot,
// `deploying` wears a halo and pulses, `unknown` is hollow — never fake
// certainty.
import { cn } from "@/lib/utils";

export type Status = "running" | "deploying" | "stopped" | "error" | "degraded" | "unknown";

const MARKER: Record<Status, string> = {
  running: "rounded-full bg-status-running",
  deploying:
    "rounded-full bg-status-deploying animate-status-pulse ring-3 ring-status-deploying/20",
  stopped: "rounded-full bg-status-stopped",
  error: "rounded-[2px] bg-status-error",
  degraded: "rounded-full bg-status-degraded",
  unknown: "rounded-full border border-status-unknown bg-transparent",
};

const TEXT: Record<Status, string> = {
  running: "text-status-running",
  deploying: "text-status-deploying",
  stopped: "text-status-stopped",
  // The word darkens where the dot does not: --status-error / --status-degraded
  // are marker colours, and only their -text twins hold 4.5:1 on paper.
  error: "text-danger",
  degraded: "text-status-degraded-text",
  unknown: "text-status-unknown",
};

/** Tinted pill for rollups: `1 APP ERROR`, `ALL RUNNING`, `2 STOPPED`. */
const PILL: Record<Status, string> = {
  running: "text-status-running bg-status-running/8 border-status-running/25",
  deploying: "text-status-deploying bg-status-deploying/8 border-status-deploying/25",
  stopped: "text-text-mid bg-text-mid/6 border-border",
  error: "text-danger bg-status-error/9 border-status-error/30",
  degraded: "text-status-degraded-text bg-status-degraded/8 border-status-degraded/25",
  unknown: "text-text-mid bg-text-mid/6 border-border",
};

export function normalizeStatus(s: string | undefined | null): Status {
  switch (s) {
    case "running":
    case "deploying":
    case "stopped":
    case "error":
    case "degraded":
      return s;
    // A database reports "provisioning" for the same in-progress meaning apps
    // call "deploying" — one visual vocabulary (ui-principles §5).
    case "provisioning":
      return "deploying";
    default:
      return "unknown";
  }
}

export function StatusBadge({ status, className }: { status: string | undefined | null; className?: string }) {
  const s = normalizeStatus(status);
  return (
    <span className={cn("inline-flex items-center gap-2", className)}>
      {/* The word is in the very next span, so the dot drops its label here:
          it carries one only because StatusDot may stand alone. */}
      <StatusDot status={s} decorative />
      {/* No aria-live. Canvas 14g gives the announcement to the page's stage
          summary — a twenty-row table whose every row is its own live region
          talks over the change the operator is actually waiting for. */}
      <span className={cn("font-mono text-[11px] font-medium uppercase tracking-wide", TEXT[s])}>
        {s}
      </span>
    </span>
  );
}

/** Marker-only variant for dense rows and rollups. */
export function StatusDot({
  status,
  decorative,
  className,
}: {
  status: string | undefined | null;
  /** Set where the word is already next to the dot (StatusBadge, a row that
      pairs the marker with StatusWord): the label would be read twice. */
  decorative?: boolean;
  className?: string;
}) {
  const s = normalizeStatus(status);
  return (
    <span
      className={cn("inline-block h-2.5 w-2.5 shrink-0", MARKER[s], className)}
      role={decorative ? undefined : "img"}
      aria-label={decorative ? undefined : s}
      aria-hidden={decorative || undefined}
    />
  );
}

/**
 * The bare status word — mono 10px uppercase in the status colour, no marker,
 * no tint. Canvas 14b/14c set it right-aligned on a phone card's name row
 * (`1 ERROR`, `RUNNING`, `DEPLOYING`) where a pill would crowd the name; the
 * marker that gives the word its shape sits at the row's start. Takes its own
 * text so a rollup count can ride along; defaults to the status itself.
 */
export function StatusWord({
  status,
  children,
  className,
}: {
  status: string | undefined | null;
  children?: React.ReactNode;
  className?: string;
}) {
  const s = normalizeStatus(status);
  return (
    <span className={cn("font-mono text-[10px] font-medium uppercase tracking-wide", TEXT[s], className)}>
      {children ?? s}
    </span>
  );
}

/** Tinted rollup pill — carries its own sentence, e.g. "1 app error". */
export function StatusPill({
  status,
  children,
  className,
}: {
  status: string | undefined | null;
  children: React.ReactNode;
  className?: string;
}) {
  const s = normalizeStatus(status);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded border px-2 py-[3px] font-mono text-[11.5px] font-medium uppercase tracking-wide",
        PILL[s],
        className,
      )}
    >
      {children}
    </span>
  );
}
