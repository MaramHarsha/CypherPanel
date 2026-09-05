import { useEffect, useState, type CSSProperties } from "react";
import { cn } from "@/lib/utils";

/** Quiet loading placeholder — matches final layout, no shimmer louder than
 *  content (web-ui-design.md §2). Canvas 10e paints these in `--border` — the
 *  same grey as the neutral status dot beside them — so a name bar never reads
 *  paler than the dot that labels it. */
export function Skeleton({ className, style }: { className?: string; style?: CSSProperties }) {
  return <div className={cn("animate-pulse rounded bg-border", className)} style={style} aria-hidden />;
}

/**
 * True only once `delayMs` has passed — canvas `10e`: "shown only past 200 ms;
 * under that, nothing flashes."
 *
 * Most loads on a local panel resolve well inside that, and a skeleton that
 * appears and vanishes within one blink reads as a fault. Gating on time
 * rather than on a request count keeps that decision in one place instead of
 * every route inventing its own threshold.
 */
export function useSkeletonDelay(pending: boolean, delayMs = 200) {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    if (!pending) {
      setReady(false);
      return;
    }
    const t = setTimeout(() => setReady(true), delayMs);
    return () => clearTimeout(t);
  }, [pending, delayMs]);
  return pending && ready;
}

export interface SkeletonRowsProps {
  /** Column widths, mirroring the real table's grid — applied from `sm` up. */
  columns: string;
  rows?: number;
  /** Rows carry a leading status dot — drawn neutral, never a fake green. */
  dot?: boolean;
  className?: string;
}

/**
 * Bar widths per row — name, sub-line, chip, timestamp — read off canvas 10e.
 * A real list is ragged; three rows of identical bars read as a loading
 * *pattern* rather than as the shape of what is arriving.
 */
const WIDTHS = [
  [130, 190, 110, 80],
  [100, 150, 90, 60],
  [120, 170, 100, 70],
] as const;

/** Three rows: 1, .7, .4 — the canvas's exact fade, floored so a long list
 *  never fades to nothing. */
function fade(i: number): number {
  return 1 - i * 0.3 < 0.25 ? 0.25 : 1 - i * 0.3;
}

/**
 * List skeleton — canvas `10e`. Rows mirror the real layout (name, rollup
 * chip, timestamp) so the page does not reflow when data lands, the first row
 * takes the ink rule the real table uses, and successive rows fade back so the
 * eye reads one list rather than three identical bars.
 *
 * Status dots stay `--border` grey on purpose: colouring them optimistically
 * means the page claims health it has not observed, which is the same class of
 * lie as a stale container reporting success.
 */
export function SkeletonRows({ columns, rows = 3, dot = true, className }: SkeletonRowsProps) {
  return (
    <div className={cn("flex flex-col", className)} aria-hidden>
      {Array.from({ length: rows }, (_, i) => {
        const [name, sub, chip, stamp] = WIDTHS[i % WIDTHS.length]!;
        return (
          <div
            key={i}
            className={cn(
              // The lists this stands in for stack below `sm` and only become a
              // grid above it. Holding the grid at every width would push the
              // row past a 360px viewport — a horizontal scrollbar on every
              // route, for the length of every load.
              "flex flex-col gap-2 py-4 sm:grid sm:items-center sm:gap-4 sm:[grid-template-columns:var(--sk-cols)]",
              i === 0 ? "border-t-[1.5px] border-t-border-strong" : "border-t border-t-border",
            )}
            style={{ "--sk-cols": columns, opacity: fade(i) } as CSSProperties}
          >
            <div className="flex items-center gap-3.5">
              {dot && <span className="size-2.5 flex-none rounded-full bg-border" />}
              <div className="min-w-0 flex-1">
                <Skeleton className="h-[15px] max-w-full" style={{ width: name }} />
                <Skeleton className="mt-[7px] h-[9px] max-w-full bg-border-subtle" style={{ width: sub }} />
              </div>
            </div>
            <Skeleton className="h-5 max-w-full bg-border-subtle" style={{ width: chip }} />
            <Skeleton
              className="h-[11px] max-w-full bg-border-subtle sm:justify-self-end"
              style={{ width: stamp }}
            />
          </div>
        );
      })}
    </div>
  );
}

export interface SkeletonCardsProps {
  /** Card count — as many as the grid shows above the fold, no more. */
  count?: number;
  /**
   * The real grid's column classes, e.g. `"sm:grid-cols-2 xl:grid-cols-3"`
   * (templates) or `"md:grid-cols-2"` (the project board). Passed through
   * rather than computed so the placeholder is the grid it stands in for.
   */
  columns?: string;
  /** Cards lead with a neutral status dot when the real card has one. */
  dot?: boolean;
  className?: string;
}

/**
 * Card-grid skeleton — the shape of a template card or an application card
 * (1b/2j): a bordered surface with a name bar, a mono sub-bar and a meta line,
 * on the same grid the real cards use, so nothing reflows when they arrive.
 * Fades across the row like SkeletonRows so it reads as one board.
 */
export function SkeletonCards({ count = 3, columns = "sm:grid-cols-2 xl:grid-cols-3", dot = false, className }: SkeletonCardsProps) {
  return (
    <div className={cn("grid grid-cols-1 gap-3", columns, className)} aria-hidden>
      {Array.from({ length: count }, (_, i) => {
        const [name, sub, , meta] = WIDTHS[i % WIDTHS.length]!;
        return (
          <div
            key={i}
            className="flex flex-col rounded-lg border border-border bg-surface p-4.5"
            style={{ opacity: fade(i) }}
          >
            <div className="flex items-center gap-2.5">
              {dot && <span className="size-2.5 flex-none rounded-full bg-border" />}
              <Skeleton className="h-[16px] max-w-full" style={{ width: name }} />
              <Skeleton className="ml-auto h-[10px] w-14 max-w-full bg-border-subtle" />
            </div>
            <Skeleton className="mt-2.5 h-[11px] max-w-full bg-border-subtle" style={{ width: sub }} />
            <Skeleton className="mt-4 h-[11px] max-w-full bg-border-subtle" style={{ width: meta }} />
          </div>
        );
      })}
    </div>
  );
}

export interface SkeletonFormProps {
  /** Label + control pairs to draw. */
  fields?: number;
  /** Two-up field grid above `sm`, as the settings forms are (12c/13i). */
  columns?: 1 | 2;
  className?: string;
}

/**
 * Single-resource skeleton — a settings page or a `dl` of facts (2f/2h/12c):
 * a 12px label bar over a 36px control-shaped bar, at the form's own gutter,
 * so the page arrives into a form rather than into three table rows it never
 * had.
 */
export function SkeletonForm({ fields = 4, columns = 2, className }: SkeletonFormProps) {
  return (
    <div
      className={cn("grid grid-cols-1 gap-x-3 gap-y-4", columns === 2 && "sm:grid-cols-2", className)}
      aria-hidden
    >
      {Array.from({ length: fields }, (_, i) => {
        const [label] = WIDTHS[i % WIDTHS.length]!;
        return (
          <div key={i} style={{ opacity: fade(Math.floor(i / columns)) }}>
            <Skeleton className="h-[12px] max-w-full bg-border-subtle" style={{ width: label * 0.6 }} />
            <Skeleton className="mt-[7px] h-9 w-full rounded-md" />
          </div>
        );
      })}
    </div>
  );
}
