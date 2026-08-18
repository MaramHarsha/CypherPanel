// Blocking operations — design canvas `10d`. The popup an operation gets when
// it runs longer than ~2s AND cannot be cancelled honestly.
//
// Two rules the canvas is emphatic about, both about not lying:
//
//   1. It shows the *real* steps, in order, with the one in flight marked —
//      not a bar that fills on a timer. A restore that stalls at "applying
//      dump" is information; a bar at 70% is decoration.
//   2. There is no cancel, and it says why. Offering a cancel that cannot
//      stop the work is a worse failure than offering none, because the
//      operator believes the work stopped.
//
// Closing it does not stop the job — that is stated in the popup, and the
// resource card carries the progress afterwards.
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

export type StepState = "done" | "active" | "pending";

export interface ProgressStep {
  /** "fetched backup from b2-backups · checksum ✓" — the work, not a label. */
  label: string;
  state: StepState;
}

const MARK: Record<StepState, { glyph: string; className: string }> = {
  done: { glyph: "✓", className: "text-status-running" },
  active: { glyph: "▸", className: "text-status-deploying" },
  pending: { glyph: "○", className: "text-text-disabled" },
};

export interface BlockingProgressProps {
  open: boolean;
  /** "Restoring atlas-pg…" — present tense, names the resource. */
  title: string;
  /** The consequence, stated plainly: "The database is offline during this." */
  detail: string;
  steps: ProgressStep[];
  /** 0–1. Omit when the work has no honest measure of progress. */
  progress?: number;
  /** Why there is no cancel. Required — `10d` never shows one without it. */
  noCancelReason: string;
  onOpenChange?: (open: boolean) => void;
}

export function BlockingProgress({
  open,
  title,
  detail,
  steps,
  progress,
  noCancelReason,
  onOpenChange,
}: BlockingProgressProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="alert"
        title={title}
        description={detail}
        hideClose
        // The red top rule marks it as consequential, matching the destructive
        // confirm it usually follows.
        className="border-t-[3px] border-t-status-error"
        // Escape and the overlay would both read as "cancel", and there is no
        // cancel. The popup says as much; these make the UI agree with it.
        onEscapeKeyDown={(e) => e.preventDefault()}
        onPointerDownOutside={(e) => e.preventDefault()}
      >
        <ol className="mt-1 font-mono text-[11.5px] leading-[2.1] text-text-mid">
          {steps.map((s) => (
            <li key={s.label} className={cn(s.state === "pending" && "text-text-disabled")}>
              <span className={MARK[s.state].className} aria-hidden>
                {MARK[s.state].glyph}
              </span>{" "}
              {s.label}
            </li>
          ))}
        </ol>

        {progress !== undefined && (
          <div
            className="mt-3 h-1.5 overflow-hidden rounded-full bg-raised"
            role="progressbar"
            aria-valuenow={Math.round(progress * 100)}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div
              className="h-full rounded-full bg-status-error transition-[width] duration-500"
              style={{ width: `${Math.min(100, Math.max(0, progress * 100))}%` }}
            />
          </div>
        )}

        <p className="mt-3.5 flex items-start gap-[9px] text-[12px] leading-[1.5] text-text-faint">
          <span className="flex-none" aria-hidden>
            ⚠
          </span>
          {noCancelReason}
        </p>
      </DialogContent>
    </Dialog>
  );
}
