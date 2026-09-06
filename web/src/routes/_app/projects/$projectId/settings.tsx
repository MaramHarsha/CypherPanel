// Project settings: what the project is, who hears about its events, what its
// applications share, and the project's own danger zone. Four tabs because
// each answers a different question — General is the project's own facts and
// its delete (canvas 12c), Notifiers reach people in prose, Webhooks reach
// machines as a signed JSON contract (outbound-webhooks.md §7), and Shared
// variables are the values every app in the project reads
// (shared-variables.md §8). The board datelines each screen
// ATLAS-CRM / SETTINGS / <TAB>, which is a route, not an anchor.
//
// 12c's strip also draws Quotas, Protection and Status page. Protection now has
// an endpoint (deploy-protection.md) so it is drawn; Quotas and Status page do
// not, so they are still absent — a tab that opens onto nothing is a dead end,
// and each arrives with its endpoint.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { PageBody, PageHeader } from "@/components/page-header";

export const Route = createFileRoute("/_app/projects/$projectId/settings")({
  component: ProjectSettingsLayout,
});

const TABS = [
  { to: ".", label: "General", exact: true },
  { to: "notifiers", label: "Notifiers", exact: false },
  { to: "webhooks", label: "Webhooks", exact: false },
  { to: "shared-variables", label: "Shared variables", exact: false },
  { to: "protection", label: "Protection", exact: false },
] as const;

function ProjectSettingsLayout() {
  // The trail is declared by each tab, not here: a dateline reading SETTINGS on
  // the Webhooks tab names the section rather than the place, and the accent
  // segment is meant to be where you actually are (mirrors _app/settings.tsx).
  return (
    <>
      <PageHeader
        title="Project settings"
        below={
          <nav className="-mb-px flex gap-[18px] overflow-x-auto" aria-label="Project settings">
            {TABS.map((t) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to}
                activeOptions={{ exact: t.exact }}
                className="whitespace-nowrap border-b-2 border-transparent px-0.5 py-2 text-[13px] text-text-mid hover:text-text"
                activeProps={{ className: "border-border-strong font-semibold text-text" }}
              >
                {t.label}
              </Link>
            ))}
          </nav>
        }
      />
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
