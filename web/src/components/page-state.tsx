// PageState — the four-state page contract as a wrapper (ui-principles §1):
// Loading / Empty / Error / Content, making the contract the path of least
// resistance for every data region.
import { type UseQueryResult } from "@tanstack/react-query";
import { RotateCw } from "lucide-react";
import { type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

interface PageStateProps<T, E> {
  // E stays generic: the generated client's error type is the API's `Error`
  // model, not the global Error — runtime narrowing happens via instanceof.
  query: UseQueryResult<T, E>;
  /** Rendered when data resolves but `isEmpty(data)` (default: empty array). */
  empty?: ReactNode;
  isEmpty?: (data: T) => boolean;
  /** Skeleton matching the final layout; default is three quiet rows. */
  loading?: ReactNode;
  children: (data: T) => ReactNode;
}

export function PageState<T, E = unknown>({ query, empty, isEmpty, loading, children }: PageStateProps<T, E>) {
  if (query.isPending) {
    return (
      loading ?? (
        <div className="space-y-2" aria-busy>
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      )
    );
  }

  if (query.isError) {
    const err = query.error;
    const headline = err instanceof ApiError ? err.message : "Something went wrong loading this";
    return (
      <div className="rounded-md border border-danger/30 bg-danger/5 p-4">
        <p className="text-sm text-text">{headline}</p>
        <div className="mt-3 flex items-center gap-3">
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
