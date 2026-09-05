// The step list of canvas 10a, panel 2 — "real steps, not a spinner" — shared
// by the create popup and the database row on the project board. The popup's
// footnote promises "the card on the project board shows progress", and a
// promise made in one place has to be kept in the other with the same list.
//
// The steps are the control plane's own reading of the record: the create was
// accepted, the agent is provisioning, the engine is serving. The finer steps
// the canvas draws — image pull with "(cached)", volume, container start, DNS —
// would be a story told over fields the API does not report (Database carries
// only `status` and a free-text `status_detail`), and a story is worse than a
// short list that is true. When the record grows a provisioning phase, this is
// the one place to map it.
import type { DatabaseStatus } from "@/api/gen/model";
import { cn } from "@/lib/utils";

export type DbStepState = "done" | "active" | "pending" | "failed";

export interface DbStep {
  /** "provisioning on srv-frankfurt-1" — the work, not a label. */
  label: string;
  state: DbStepState;
}

// Glyph, colour — and the word. Canvas 14g: "every dot carries the word, not
// just the colour"; the same vocabulary as ui/blocking-progress.tsx, plus the
// failed mark a create can end on.
const MARK: Record<DbStepState, { glyph: string; word: string; className: string }> = {
  done: { glyph: "✓", word: "Done:", className: "text-status-running" },
  active: { glyph: "▸", word: "In progress:", className: "text-status-deploying" },
  pending: { glyph: "○", word: "Waiting:", className: "text-text-disabled" },
  failed: { glyph: "✕", word: "Failed:", className: "text-danger" },
};

export interface Provisioning {
  steps: DbStep[];
  /** 0–1: half credit for the step in flight — the bar tracks the list and
   *  nothing else, so it stops where the work stops. */
  progress: number;
  ready: boolean;
  failed: boolean;
}

/** Reads the three plane-side steps off a database's status. */
export function provisioningSteps(
  status: DatabaseStatus | undefined,
  engineLabel: string,
  serverName: string,
): Provisioning {
  const ready = status === "running";
  const failed = status === "error";
  const steps: DbStep[] = [
    { label: `accepted · ${engineLabel}`, state: "done" },
    { label: `provisioning on ${serverName}`, state: failed ? "failed" : ready ? "done" : "active" },
    { label: "accepting connections", state: ready ? "done" : "pending" },
  ];
  return { steps, progress: failed ? 1 / 3 : ready ? 1 : 1.5 / 3, ready, failed };
}

export function ProvisioningSteps({
  steps,
  progress,
  failed,
  /** The agent's own words when it has any — never our paraphrase. */
  detail,
  /** Names the bar for a screen reader: "Creating atlas-pg". */
  label,
  /** The board row's tighter setting; the popup uses the canvas's 11.5/2.1. */
  compact,
  className,
}: {
  steps: DbStep[];
  progress: number;
  failed?: boolean;
  detail?: string;
  label: string;
  compact?: boolean;
  className?: string;
}) {
  const active = steps.find((s) => s.state === "active" || s.state === "failed");
  return (
    <div className={className}>
      <ol
        className={cn(
          "font-mono text-text-dim",
          compact ? "text-[11px] leading-[1.9]" : "text-[11.5px] leading-[2.1]",
        )}
      >
        {steps.map((s) => (
          <li key={s.label} className={cn(s.state === "pending" && "text-text-disabled")}>
            <span className={MARK[s.state].className} aria-hidden>
              {MARK[s.state].glyph}
            </span>
            <span className="sr-only">{MARK[s.state].word} </span> {s.label}
          </li>
        ))}
      </ol>

      {/* 14g: one polite announcement each time the work moves on. */}
      <p role="status" className="sr-only">
        {active ? `${MARK[active.state].word} ${active.label}` : ""}
      </p>

      {detail && (
        <p className={cn("mt-1.5 text-xs leading-relaxed", failed ? "text-danger" : "text-text-faint")}>{detail}</p>
      )}

      <div
        className={cn("h-[5px] overflow-hidden rounded-full bg-border-subtle", compact ? "mt-2" : "mt-2.5")}
        role="progressbar"
        aria-label={label}
        aria-valuenow={Math.round(progress * 100)}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div
          className={cn(
            "h-full rounded-full transition-[width] duration-500 motion-reduce:transition-none",
            failed ? "bg-status-error" : "bg-primary",
          )}
          style={{ width: `${Math.min(100, Math.max(0, progress * 100))}%` }}
        />
      </div>
    </div>
  );
}
