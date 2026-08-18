import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

/** Quiet loading placeholder — matches final layout, no shimmer louder than
 *  content (web-ui-design.md §2). */
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded bg-raised", className)} aria-hidden />;
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
  /** Column widths, mirroring the real table's grid. */
  columns: string;
  rows?: number;
  /** Rows carry a leading status dot — drawn neutral, never a fake green. */
  dot?: boolean;
  className?: string;
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
      {Array.from({ length: rows }, (_, i) => (
        <div
          key={i}
          className={cn(
            "grid items-center gap-4 py-4",
            i === 0 ? "border-t-[1.5px] border-t-border-strong" : "border-t border-t-border",
          )}
          style={{
            gridTemplateColumns: columns,
            // Three rows: 1, .7, .4 — the canvas's exact fade.
            opacity: 1 - i * 0.3 < 0.25 ? 0.25 : 1 - i * 0.3,
          }}
        >
          <div className="flex items-center gap-3.5">
            {dot && <span className="size-2.5 flex-none rounded-full bg-border" />}
            <div className="flex-1">
              <Skeleton className="h-[15px] w-[130px]" />
              <Skeleton className="mt-[7px] h-[9px] w-[190px] bg-border/45" />
            </div>
          </div>
          <Skeleton className="h-5 w-[110px] bg-border/45" />
          <Skeleton className="h-[11px] w-20 justify-self-end bg-border/45" />
        </div>
      ))}
    </div>
  );
}
