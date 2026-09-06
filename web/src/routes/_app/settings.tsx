// Settings layout: the account, the people, and the credentials that outlive
// any one project. Everything project-scoped lives inside its project instead
// (ui-principles §4 — nav items compete with each other).
import { createFileRoute, Outlet } from "@tanstack/react-router";
import { PageBody, PageHeader } from "@/components/page-header";
import { TabStrip, type Tab } from "@/components/tab-strip";

export const Route = createFileRoute("/_app/settings")({ component: SettingsLayout });

// Twelve destinations is more than a phone can hold, so the strip folds like
// every other one (canvas 14c): the four an operator opens daily stay, the
// rest are one tap into "More". None of the four needs a short label at
// 360px, and the eight that fold are named in full in the menu.
const TABS: readonly Tab[] = [
  { to: "", label: "Account" },
  { to: "profile", label: "Profile" },
  { to: "teams", label: "Teams" },
  { to: "users", label: "Users" },
  { to: "deploy-keys", label: "Deploy keys" },
  { to: "registries", label: "Registries" },
  { to: "audit", label: "Audit" },
  { to: "backup-targets", label: "Backup targets" },
  { to: "mail", label: "Mail" },
  { to: "dns", label: "DNS" },
  { to: "tls", label: "TLS" },
  { to: "diagnostics", label: "Diagnostics" },
];

function SettingsLayout() {
  // The trail is declared by each tab, not here: a dateline reading SETTINGS on
  // the deploy-keys tab names the section rather than the place, and the accent
  // segment is meant to be where you actually are (canvas 13a).
  return (
    <>
      <PageHeader title="Settings" below={<TabStrip label="Settings" base="/settings" tabs={TABS} />} />
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
