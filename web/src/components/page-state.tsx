// PageState — the four-state page contract as a wrapper (ui-principles §1):
// Loading / Empty / Error / Content, making the contract the path of least
// resistance for every data region.
import { type UseQueryResult } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import { type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { SkeletonRows, useSkeletonDelay } from "@/components/ui/skeleton";

interface PageStateProps<T, E> {
  // E stays generic: the generated client's error type is the API's `Error`
  // model, not the global Error — runtime narrowing happens via instanceof.
  query: UseQueryResult<T, E>;
  /** Rendered when data resolves but `isEmpty(data)` (default: empty array). */
  empty?: ReactNode;
  isEmpty?: (data: T) => boolean;
  /**
   * Replaces the row skeleton outright, and — because the route asked for this
   * exact placeholder — is painted immediately rather than behind the 200 ms
   * gate. Use it where the region is a fixed-height control strip and holding
   * the space matters more than staying silent.
   */
  loading?: ReactNode;
  /**
   * Grid template for the default skeleton — mirror the real table's columns.
   * Unset, the placeholder is a single column of bars: a card grid or a
   * single-resource page that got the project list's three columns would paint
   * a shape it is about to replace, and reflow twice instead of none.
   */
  skeletonColumns?: string;
  skeletonRows?: number;
  /** Rows lead with a neutral status dot when the real list has one. */
  skeletonDot?: boolean;
  children: (data: T) => ReactNode;
}

export function PageState<T, E = unknown>({
  query,
  empty,
  isEmpty,
  loading,
  skeletonColumns = "1fr",
  skeletonRows = 3,
  skeletonDot = false,
  children,
}: PageStateProps<T, E>) {
  // Canvas 10e: a page never spins, it shows the shape of what is coming — and
  // only past 200 ms, because a placeholder that appears and vanishes inside
  // one blink reads as a fault rather than as a load.
  const showSkeleton = useSkeletonDelay(query.isPending);

  if (query.isPending) {
    if (loading !== undefined) return <>{loading}</>;
    if (!showSkeleton) return null;
    return (
      <div aria-busy>
        <SkeletonRows columns={skeletonColumns} rows={skeletonRows} dot={skeletonDot} />
      </div>
    );
  }

  if (query.isError) {
    const err = query.error;
    const headline = err instanceof ApiError ? err.message : "Something went wrong loading this";
    return (
      <div className="rounded-lg border border-danger/35 bg-danger/[0.06] p-5">
        <p className="text-[15px] font-semibold text-text">{headline}</p>
        <div className="mt-4 flex items-center gap-3">
          <Button size="sm" onClick={() => void query.refetch()}>
            <RotateCw className="h-3.5 w-3.5" /> Retry
          </Button>
          {!(err instanceof ApiError) && (
            <details className="text-xs text-text-faint">
              <summary className="cursor-pointer">Details</summary>
              <pre className="mono mt-1 whitespace-pre-wrap">{String(err)}</pre>
            </details>
          )}
        </div>
      </div>
    );
  }

  const data = query.data;
  const emptyCheck = isEmpty ?? ((d: T) => Array.isArray(d) && d.length === 0);
  if (empty !== undefined && emptyCheck(data)) return <>{empty}</>;
  return <>{children(data)}</>;
}
