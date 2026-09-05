// Blocking operations — design canvas `10d` (dark twin `13an`). The popup an
// operation gets when it runs longer than ~2s AND cannot be cancelled honestly.
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

// Glyph, colour — and the word. Canvas 14g: "every dot carries the word, not
// just the color", so the mark that is decoration for the eye is spelled out
// for a screen reader instead of leaving the whole list sounding identical.
const MARK: Record<StepState, { glyph: string; word: string; className: string }> = {
  done: { glyph: "✓", word: "Done:", className: "text-status-running" },
  active: { glyph: "▸", word: "In progress:", className: "text-status-deploying" },
  pending: { glyph: "○", word: "Waiting:", className: "text-text-disabled" },
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
  const active = steps.find((s) => s.state === "active");
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
        {/* Steps are set in the panel's readable secondary body colour, not the
            label grey: this is the only account of what the machine is doing
            and it has to survive being read across a desk. */}
        <ol className="mt-0.5 font-mono text-[11.5px] leading-[2.1] text-text-dim">
          {steps.map((s) => (
            <li key={s.label} className={cn(s.state === "pending" && "text-text-disabled")}>
              <span className={MARK[s.state].className} aria-hidden>
                {MARK[s.state].glyph}
              </span>
              <span className="sr-only">{MARK[s.state].word} </span> {s.label}
            </li>
          ))}
        </ol>

        {/* 14g: a log tail would never stop talking, so the stage summary speaks
            instead — one polite announcement each time the work moves on. */}
        <p role="status" className="sr-only">
          {active?.label ?? ""}
        </p>

        {progress !== undefined && (
          <div
            // The track is the quiet in-card rule colour, so an unfilled bar
            // reads as an empty channel rather than as a second surface.
            className="mt-3 h-1.5 overflow-hidden rounded-full bg-border-subtle"
            role="progressbar"
            aria-label={title}
            aria-valuenow={Math.round(progress * 100)}
            aria-valuemin={0}
            aria-valuemax={100}
          >
            <div
              className="h-full rounded-full bg-status-error transition-[width] duration-500 motion-reduce:transition-none"
              style={{ width: `${Math.min(100, Math.max(0, progress * 100))}%` }}
            />
          </div>
        )}

        {/* The canvas centres the ⚠ against the three lines this sentence takes
            at 430px. Narrower than that it wraps to five or six, where a glyph
            floating beside the middle of a paragraph reads as a mistake. */}
        <p className="mt-3.5 flex items-start gap-[9px] text-[12px] leading-[1.5] text-text-faint sm:items-center">
          <span className="flex-none" aria-hidden>
            ⚠
          </span>
          {noCancelReason}
        </p>
      </DialogContent>
    </Dialog>
  );
}
