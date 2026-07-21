// StatusBadge — the single source of status rendering (web-ui-design.md §6).
// One vocabulary everywhere (ui-principles §5): mono label + dot; `deploying`
// pulses (an earned animation); `unknown` is a hollow dot — never fake
// certainty.
import { cn } from "@/lib/utils";

export type Status = "running" | "deploying" | "stopped" | "error" | "degraded" | "unknown";

const DOT: Record<Status, string> = {
  running: "bg-status-running",
  deploying: "bg-status-deploying animate-status-pulse",
  stopped: "bg-status-stopped",
  error: "bg-status-error",
  degraded: "bg-status-degraded",
  unknown: "border border-status-unknown bg-transparent",
};

const TEXT: Record<Status, string> = {
  running: "text-status-running",
  deploying: "text-status-deploying",
  stopped: "text-status-stopped",
  error: "text-status-error",
  degraded: "text-status-degraded",
  unknown: "text-status-unknown",
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
    <span className={cn("inline-flex items-center gap-1.5", className)}>
      <span className={cn("h-2 w-2 shrink-0 rounded-full", DOT[s])} aria-hidden />
      <span className={cn("mono text-xs", TEXT[s])} aria-live="polite">
        {s}
      </span>
    </span>
  );
}

/** Dot-only variant for dense rows and rollups. */
export function StatusDot({ status, className }: { status: string | undefined | null; className?: string }) {
  const s = normalizeStatus(status);
  return (
    <span
      className={cn("inline-block h-2 w-2 rounded-full", DOT[s], className)}
      role="img"
      aria-label={s}
    />
  );
}
