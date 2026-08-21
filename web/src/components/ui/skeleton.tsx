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
            style={
              {
                "--sk-cols": columns,
                // Three rows: 1, .7, .4 — the canvas's exact fade.
                opacity: 1 - i * 0.3 < 0.25 ? 0.25 : 1 - i * 0.3,
              } as CSSProperties
            }
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
