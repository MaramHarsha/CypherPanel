// Settings layout: the account, the people, and the credentials that outlive
// any one project. Everything project-scoped lives inside its project instead
// (ui-principles §4 — nav items compete with each other).
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { PageBody, PageHeader } from "@/components/page-header";

export const Route = createFileRoute("/_app/settings")({ component: SettingsLayout });

const TABS = [
  { to: "/settings", label: "Account", exact: true },
  { to: "/settings/profile", label: "Profile", exact: false },
  { to: "/settings/teams", label: "Teams", exact: false },
  { to: "/settings/users", label: "Users", exact: false },
  { to: "/settings/deploy-keys", label: "Deploy keys", exact: false },
  { to: "/settings/backup-targets", label: "Backup targets", exact: false },
] as const;

function SettingsLayout() {
  // The trail is declared by each tab, not here: a dateline reading SETTINGS on
  // the deploy-keys tab names the section rather than the place, and the accent
  // segment is meant to be where you actually are (canvas 13a).
  return (
    <>
      <PageHeader
        title="Settings"
        below={
          <nav className="-mb-px flex gap-5 overflow-x-auto" aria-label="Settings">
            {TABS.map((t) => (
              <Link
                key={t.to}
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
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
