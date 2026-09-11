// PageState — the four-state page contract as a wrapper (ui-principles §1):
// Loading / Empty / Error / Content, making the contract the path of least
// resistance for every data region.
import { type UseQueryResult } from "@tanstack/react-query";
import { type ReactNode } from "react";
import { ApiError, NetworkError } from "@/api/client";
import {
  ForbiddenForError,
  NotFoundPage,
  PlaneOfflinePage,
  ServerFaultPage,
} from "@/components/error-page";
import { ActionButton } from "@/components/ui/action-button";
import { SkeletonRows, useSkeletonDelay } from "@/components/ui/skeleton";
import { relativeTime } from "@/lib/time";

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
    return (
      <QueryError
        error={query.error}
        retry={() => void query.refetch()}
        retrying={query.isFetching}
        lastSyncAt={query.dataUpdatedAt}
      />
    );
  }

  const data = query.data;
  const emptyCheck = isEmpty ?? ((d: T) => Array.isArray(d) && d.length === 0);
  if (empty !== undefined && emptyCheck(data)) return <>{empty}</>;
  return <>{children(data)}</>;
}

/** The last prefixed id in a path (`app_…`, `tm_…`) — the thing a 404 was about. */
function resourceIdOf(path: string): string | undefined {
  return path
    .split("/")
    .reverse()
    .find((seg) => /^[a-z]+_[A-Za-z0-9-]+$/.test(seg));
}

/**
 * A failed query, answered with the page its status deserves (canvas 8a–8d).
 * React Query hands errors back as state, so this is the one place a real API
 * answer meets the designed pages: no answer at all is 8c, a refusal is 8b,
 * a panel fault is 8d, a resource that is not there is 8a. Anything else is a
 * 4xx with the server's own sentence — an inline box with the retry beside it.
 */
export function QueryError({
  error,
  retry,
  retrying = false,
  lastSyncAt,
}: {
  error: unknown;
  /** Asks the same question again. Without it the offline and fault pages reload the tab. */
  retry?: () => void;
  retrying?: boolean;
  /** react-query's `dataUpdatedAt`; 0 means this region has never had an answer. */
  lastSyncAt?: number;
}) {
  if (error instanceof NetworkError) {
    return (
      <PlaneOfflinePage
        embedded
        // Without a query to re-ask, the retry is a reload — never on a timer.
        retryEverySeconds={retry ? 5 : 0}
        retrying={retrying}
        lastSyncLabel={lastSyncAt ? relativeTime(new Date(lastSyncAt).toISOString()) : undefined}
        onRetry={retry}
      />
    );
  }
  if (error instanceof ApiError) {
    if (error.status === 403) return <ForbiddenForError error={error} embedded />;
    if (error.status === 404) return <NotFoundPage embedded resource={resourceIdOf(error.path)} />;
    if (error.status < 500) {
      return (
        <div className="rounded-lg border border-danger/35 bg-danger/[0.06] p-5" role="alert">
          <p className="text-[15px] font-semibold text-text">{error.message}</p>
          <div className="mt-4 flex items-center gap-3">
            <ActionButton
              size="sm"
              state={retrying ? "busy" : "idle"}
              busyLabel="Retrying…"
              onClick={retry ?? (() => location.reload())}
            >
              Retry
            </ActionButton>
          </div>
        </div>
      );
    }
  }
  // A 5xx, or something the panel itself threw while reading the answer.
  return <ServerFaultPage embedded error={error} onReload={retry} reloading={retrying} />;
}
