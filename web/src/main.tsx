import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "@/api/client";
import { useGetMe } from "@/api/gen/auth/auth";
import { useListTeamMembers } from "@/api/gen/teams/teams";
import { ForbiddenPage, NotFoundPage, ServerFaultPage } from "@/components/error-page";
import { applyTheme, storedTheme } from "@/lib/theme";
import { isSseOpen } from "@/lib/live-flag";
import { useTeamScope } from "@/lib/team";
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
    error instanceof ApiError && error.status === 403 ? <ForbiddenRoute /> : <PanelFault error={error} />,
});

/**
 * 8b/13q is emphatic that the 403 *names the fix*, and half of that naming has
 * to be assembled here: cypherd's 403 body is only `{"error":"insufficient
 * role"}`, so the page learns the caller's own role from /auth/me and the
 * people who can grant more from the team's member list. Without this the
 * request-access pill and the `owners:` line never render at all, and the page
 * reduces to a numeral and a Back button.
 */
function ForbiddenRoute() {
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const teams = me.data?.teams ?? [];
  // Scoped team first; failing that, the only team there is. With several
  // teams and no scope set there is nobody specific to name.
  const team = teams.find((t) => t.id === teamId) ?? (teams.length === 1 ? teams[0] : undefined);
  const members = useListTeamMembers(team?.id ?? "", { query: { enabled: team !== undefined } });
  const owners = (members.data ?? []).filter((m) => m.role === "owner").map((m) => m.email);

  return (
    <ForbiddenPage
      // `member` is the floor of the three roles, and a 403 means the caller is
      // below what the route wanted — so it is the safe reading while the team
      // is still resolving.
      held={team?.role ?? "member"}
      owners={owners}
      onRequestAccess={() => {
        const owner = owners[0];
        if (owner === undefined) return;
        const subject = encodeURIComponent("CypherPanel — access request");
        window.location.href = `mailto:${owner}?subject=${subject}`;
      }}
    />
  );
}

/** 8d/13s — the panel's own fault, with the one action that is not "reload". */
function PanelFault({ error }: { error: unknown }) {
  const traceId = error instanceof ApiError ? undefined : traceIdOf(error);
  return (
    <ServerFaultPage
      traceId={traceId}
      onReport={() => {
        // 13ai's promise about this button is that nothing leaves the machine
        // silently and nothing identifying leaves it at all: the issue opens
        // pre-filled in a new tab, carrying the trace id and a prompt, and the
        // operator writes and sends the rest themselves.
        const params = new URLSearchParams({
          title: "Panel fault",
          body: `${traceId ? `trace id: ${traceId}\n\n` : ""}What I was doing:\n`,
        });
        window.open(
          `https://github.com/MaramHarsha/CypherPanel/issues/new?${params.toString()}`,
          "_blank",
          "noopener,noreferrer",
        );
      }}
    />
  );
}

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
