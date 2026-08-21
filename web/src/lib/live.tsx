// Live invalidation: one /api/v1/events stream per session turns the plane's
// status transitions into TanStack Query invalidations — SSE-driven freshness
// with polling as the reconnect fallback (web-ui-design.md §5).
import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, type ReactNode } from "react";
import { getStreamEventsUrl } from "@/api/gen/health/health";
import { setSseOpen } from "@/lib/live-flag";
import { useSSE, type SSEStatus } from "@/lib/use-sse";

interface InvalidatePayload {
  resource: "application" | "database";
  id: string;
}

const LiveContext = createContext<SSEStatus>("connecting");

/** The stream's health, for the global stale banner. */
export function useLiveStatus(): SSEStatus {
  return useContext(LiveContext);
}

export function LiveProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();

  const status = useSSE(getStreamEventsUrl(), {
    onMessage: (msg) => {
      if (msg.event !== "invalidate") return;
      let payload: InvalidatePayload;
      try {
        payload = JSON.parse(msg.data) as InvalidatePayload;
      } catch {
        return;
      }
      const detail =
        payload.resource === "application"
          ? `/applications/${payload.id}`
          : `/databases/${payload.id}`;
      const listTail =
        payload.resource === "application" ? "/applications" : "/databases";
      void qc.invalidateQueries({
        predicate: (q) => {
          const k = q.queryKey[0];
          if (typeof k !== "string") return false;
          // The notification inbox rides the same stream rather than opening a
          // second one (notification-inbox.md §7). It is exact, not
          // approximate: every transition that mints an item also emits this
          // invalidation, and the item's recipients are a subset of the set
          // that can see the resource. Stream down, the QueryClient's SSE-gated
          // 5 s poll covers the bell — no new endpoint either.
          if (k.startsWith("/api/v1/inbox")) return true;
          // The changed resource itself (detail, deployments, previews …) and
          // every environment-scoped list that could contain it.
          return k.includes(detail) || k.endsWith(listTail);
        },
      });
    },
  });

  useEffect(() => {
    setSseOpen(status === "open");
    return () => setSseOpen(false);
  }, [status]);

  return <LiveContext.Provider value={status}>{children}</LiveContext.Provider>;
}
