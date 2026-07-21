// Settings layout. Teams/users management arrives with slice 4 — the tabs
// shown are the ones that work today, no stubs.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/settings")({ component: SettingsLayout });

const TABS = [
  { to: "/settings", label: "Account", exact: true },
  { to: "/settings/deploy-keys", label: "Deploy keys", exact: false },
  { to: "/settings/backup-targets", label: "Backup targets", exact: false },
] as const;

function SettingsLayout() {
  useCrumbs([{ label: "settings" }]);
  return (
    <div className="space-y-4">
      <nav className="flex gap-0.5 overflow-x-auto border-b border-border" aria-label="Settings">
        {TABS.map((t) => (
          <Link
            key={t.to}
            to={t.to}
            activeOptions={{ exact: t.exact }}
            className="-mb-px whitespace-nowrap border-b-2 border-transparent px-3 py-2 text-[13px] text-text-mid hover:text-text"
            activeProps={{ className: "-mb-px border-accent text-text" }}
          >
            {t.label}
          </Link>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}
