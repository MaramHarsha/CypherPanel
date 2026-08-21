// Project settings: who hears about this project's events, and the project's
// own danger zone. Two tabs because the two audiences want opposite things —
// Notifiers reach people in prose, Webhooks reach machines as a signed JSON
// contract (outbound-webhooks.md §7). The board datelines the second screen
// ATLAS-CRM / SETTINGS / WEBHOOKS, which is a route, not an anchor.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { PageBody, PageHeader } from "@/components/page-header";

export const Route = createFileRoute("/_app/projects/$projectId/settings")({
  component: ProjectSettingsLayout,
});

const TABS = [
  { to: ".", label: "Notifiers", exact: true },
  { to: "webhooks", label: "Webhooks", exact: false },
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
          <nav className="-mb-px flex gap-5 overflow-x-auto" aria-label="Project settings">
            {TABS.map((t) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to}
                activeOptions={{ exact: t.exact }}
                className="whitespace-nowrap border-b-2 border-transparent px-0.5 py-2.5 text-[13px] text-text-mid hover:text-text"
                activeProps={{ className: "border-border-strong font-semibold text-text" }}
              >
                {t.label}
              </Link>
            ))}
          </nav>
        }
      />
      {/* The layout owns the page gutters so both tabs are inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
