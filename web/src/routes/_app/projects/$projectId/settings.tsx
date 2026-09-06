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
import { createFileRoute, Outlet } from "@tanstack/react-router";
import { PageBody, PageHeader } from "@/components/page-header";
import { TabStrip, type Tab } from "@/components/tab-strip";

export const Route = createFileRoute("/_app/projects/$projectId/settings")({
  component: ProjectSettingsLayout,
});

const TABS: readonly Tab[] = [
  { to: "", label: "General" },
  { to: "notifiers", label: "Notifiers" },
  { to: "webhooks", label: "Webhooks" },
  { to: "shared-variables", label: "Shared variables" },
  { to: "protection", label: "Protection" },
];

// Three rather than 14c's four: "Shared variables" is the widest label in the
// panel, and four of these leave nothing for the fold at 360px.
const NARROW = 3;

function ProjectSettingsLayout() {
  const { projectId } = Route.useParams();
  // The trail is declared by each tab, not here: a dateline reading SETTINGS on
  // the Webhooks tab names the section rather than the place, and the accent
  // segment is meant to be where you actually are (mirrors _app/settings.tsx).
  return (
    <>
      <PageHeader
        title="Project settings"
        below={
          <TabStrip
            label="Project settings"
            base={`/projects/${projectId}/settings`}
            tabs={TABS}
            narrow={NARROW}
          />
        }
      />
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
