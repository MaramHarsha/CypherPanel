import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "@/api/client";
import { ForbiddenPage, NotFoundPage, ServerFaultPage } from "@/components/error-page";
import { applyTheme, storedTheme } from "@/lib/theme";
import { isSseOpen } from "@/lib/live-flag";
import { routeTree } from "./routeTree.gen";
import "@/styles/globals.css";

// Theme before first render (no inline script — strict CSP).
applyTheme(storedTheme());

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      retry: 1,
      // SSE-driven invalidation is the freshness mechanism; the 5 s poll is
      // the honest fallback while the stream is down (web-ui-design.md §5).
      refetchInterval: () => (isSseOpen() ? false : 5_000),
    },
  },
});

// Canvas 8a/8b/8d wired at the router, so an unknown URL, a role-gated one and
// a panel fault each land on their designed page instead of a blank screen.
// ApiError carries the status, which is what picks between them: a thrown 403
// is a role problem with a named fix, anything else is a panel fault with a
// trace id worth pasting.
const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  defaultNotFoundComponent: () => <NotFoundPage />,
  defaultErrorComponent: ({ error }) =>
    error instanceof ApiError && error.status === 403 ? (
      <ForbiddenPage />
    ) : (
      <ServerFaultPage traceId={error instanceof ApiError ? undefined : traceIdOf(error)} />
    ),
});

/** Best-effort: cypherd stamps a trace id into the message when it has one. */
function traceIdOf(error: unknown): string | undefined {
  const m = /trace[_-]([a-z0-9-]+)/i.exec(error instanceof Error ? error.message : String(error));
  return m ? `trace_${m[1]}` : undefined;
}

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
