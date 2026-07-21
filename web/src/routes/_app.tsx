// The authed shell: sidebar (exactly four items — ui-principles §4), header
// with breadcrumbs, live-updates banner, team switcher in the sidebar footer.
import {
  createFileRoute,
  Link,
  Outlet,
  redirect,
  useNavigate,
} from "@tanstack/react-router";
import {
  Boxes,
  ChevronsUpDown,
  LayoutTemplate,
  LogOut,
  Moon,
  Server,
  Settings,
  Sun,
} from "lucide-react";
import { useGetMe, useLogout } from "@/api/gen/auth/auth";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { SSEBanner } from "@/components/sse-banner";
import {
  Dropdown,
  DropdownContent,
  DropdownItem,
  DropdownSeparator,
  DropdownTrigger,
} from "@/components/ui/dropdown";
import { clearToken, isAuthenticated } from "@/lib/auth";
import { CrumbsProvider, useCrumbsValue } from "@/lib/crumbs";
import { LiveProvider, useLiveStatus } from "@/lib/live";
import { setTheme, useTheme } from "@/lib/theme";
import { TeamProvider, useTeamScope } from "@/lib/team";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app")({
  beforeLoad: ({ location }) => {
    if (!isAuthenticated()) {
      throw redirect({ to: "/login", search: { return: location.href } });
    }
  },
  component: AppShell,
});

const NAV = [
  { to: "/projects", label: "Projects", icon: Boxes },
  { to: "/servers", label: "Servers", icon: Server },
  { to: "/templates", label: "Templates", icon: LayoutTemplate },
  { to: "/settings", label: "Settings", icon: Settings },
] as const;

function AppShell() {
  return (
    <TeamProvider>
      <LiveProvider>
        <CrumbsProvider>
          <div className="flex min-h-dvh bg-bg">
            <Sidebar />
            <div className="flex min-w-0 flex-1 flex-col">
              <Header />
              <MobileNav />
              <main className="min-w-0 flex-1 px-4 py-5 sm:px-6">
                <div className="mx-auto w-full max-w-5xl">
                  <Outlet />
                </div>
              </main>
            </div>
          </div>
        </CrumbsProvider>
      </LiveProvider>
    </TeamProvider>
  );
}

function Sidebar() {
  return (
    <aside className="sticky top-0 hidden h-dvh w-52 shrink-0 flex-col border-r border-border bg-surface sm:flex">
      <Link to="/projects" className="mono block px-4 py-4 text-[13px] tracking-wide text-text">
        <span className="text-accent">▲</span> cypherpanel
      </Link>
      <nav className="flex-1 space-y-0.5 px-2" aria-label="Main">
        {NAV.map(({ to, label, icon: Icon }) => (
          <Link
            key={to}
            to={to}
            className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-[13px] text-text-mid hover:bg-raised hover:text-text"
            activeProps={{ className: "bg-raised text-text" }}
          >
            <Icon className="h-4 w-4" aria-hidden />
            {label}
          </Link>
        ))}
      </nav>
      <TeamSwitcher />
    </aside>
  );
}

/** At <sm the sidebar becomes a top nav row — usable at 360 px. */
function MobileNav() {
  return (
    <nav className="flex gap-1 overflow-x-auto border-b border-border px-2 py-1.5 sm:hidden" aria-label="Main">
      {NAV.map(({ to, label }) => (
        <Link
          key={to}
          to={to}
          className="rounded-md px-2.5 py-1 text-[13px] text-text-mid"
          activeProps={{ className: "bg-raised text-text" }}
        >
          {label}
        </Link>
      ))}
    </nav>
  );
}

function TeamSwitcher() {
  const me = useGetMe();
  const { teamId, setTeamId } = useTeamScope();
  const teams = me.data?.teams ?? [];
  const current = teams.find((t) => t.id === teamId);

  if (teams.length <= 1) return <UserFooter />;

  return (
    <div className="border-t border-border p-2">
      <Dropdown>
        <DropdownTrigger asChild>
          <button
            type="button"
            className="flex w-full items-center justify-between rounded-md px-2 py-1.5 text-[13px] text-text-mid hover:bg-raised hover:text-text"
          >
            <span className="truncate">{current?.name ?? "All teams"}</span>
            <ChevronsUpDown className="h-3.5 w-3.5 shrink-0" aria-hidden />
          </button>
        </DropdownTrigger>
        <DropdownContent align="start" className="w-48">
          <DropdownItem onSelect={() => setTeamId(null)}>All teams</DropdownItem>
          <DropdownSeparator />
          {teams.map((t) => (
            <DropdownItem key={t.id} onSelect={() => setTeamId(t.id)}>
              {t.name}
            </DropdownItem>
          ))}
        </DropdownContent>
      </Dropdown>
      <UserFooter />
    </div>
  );
}

function UserFooter() {
  const me = useGetMe();
  const navigate = useNavigate();
  const logout = useLogout({
    mutation: {
      onSettled: () => {
        clearToken();
        void navigate({ to: "/login" });
      },
    },
  });
  const theme = useTheme();

  return (
    <div className="flex items-center justify-between gap-1 border-t border-border px-3 py-2">
      <span className="mono min-w-0 truncate text-xs text-text-faint" title={me.data?.email}>
        {me.data?.email ?? "…"}
      </span>
      <span className="flex shrink-0">
        <button
          type="button"
          aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
          onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          className="rounded p-1.5 text-text-faint hover:bg-raised hover:text-text"
        >
          {theme === "dark" ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
        </button>
        <button
          type="button"
          aria-label="Sign out"
          onClick={() => logout.mutate()}
          className="rounded p-1.5 text-text-faint hover:bg-raised hover:text-text"
        >
          <LogOut className="h-3.5 w-3.5" />
        </button>
      </span>
    </div>
  );
}

function Header() {
  const crumbs = useCrumbsValue();
  const live = useLiveStatus();
  return (
    <>
      <header
        className={cn(
          "sticky top-0 z-30 flex h-12 items-center gap-3 border-b border-border bg-bg/90 px-4 backdrop-blur sm:px-6",
        )}
      >
        <Breadcrumbs crumbs={crumbs} />
      </header>
      <SSEBanner status={live} />
    </>
  );
}
