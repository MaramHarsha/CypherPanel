// EmptyState — one sentence + the next verb, quiet (web-ui-design.md §6).
// On fresh panels these chain the golden path (ui-principles §11): each empty
// state is the next step, already in context, so there is no wizard to build.
//
// Canvas 15a gives all four of them one shape: a centred column led by a large
// mono glyph, with no card and no dashed rule of its own. The frame in those
// renders belongs to whatever the empty state sits inside — a list, a card, a
// panel — so drawing another one here would double the border every time.
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  title: string;
  /** The beginner-first "what belongs here / what happens next" line. */
  hint?: string;
  /** One or two buttons; 15a pairs an outline pill with an ink one. */
  action?: ReactNode;
  /**
   * The 30px mono mark above the copy — `⎇` for previews, `▢?` for a search
   * miss, `≡` for a filtered list. Unicode, never an icon font (handoff).
   */
  glyph?: ReactNode;
  /** Golden-path steps get the accent hairline; ordinary empties stay bare. */
  emphasis?: boolean;
  className?: string;
}

export function EmptyState({ title, hint, action, glyph = "▢", emphasis, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        "px-2.5 py-4 text-center",
        // The golden path is the one empty that asks to be noticed, so it
        // takes an accent hairline — and the room a frame needs. No tint: 15a
        // fills none of them.
        emphasis && "rounded-lg border border-accent/35 px-6 py-7",
        className,
      )}
    >
      {glyph && (
        <div className="mb-2.5 font-mono text-[30px] font-normal leading-none text-text-disabled" aria-hidden>
          {glyph}
        </div>
      )}
      <p className="text-[13px] text-text-dim">{title}</p>
      {hint && <p className="mx-auto mt-1 max-w-md text-[12px] leading-[1.55] text-text-faint">{hint}</p>}
      {action && <div className="mt-3.5 flex flex-wrap items-center justify-center gap-2">{action}</div>}
    </div>
  );
}
