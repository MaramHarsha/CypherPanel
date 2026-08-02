// The authed shell. Mission Control puts navigation in a top bar rather than a
// sidebar (ui-principles §4, revised 2026-08-02): the same four items, but the
// full width of the viewport belongs to the content, which is what an ops tool
// full of wide tables actually needs. The bar sits on an ink rule — the
// strongest line in the product — so chrome and content never blur together.
import { createFileRoute, Link, Outlet, redirect, useNavigate } from "@tanstack/react-router";
import { ChevronDown, LogOut, Moon, Search, Sun } from "lucide-react";
import { useGetMe, useLogout } from "@/api/gen/auth/auth";
import { CommandPalette, openCommandPalette } from "@/components/command-palette";
import { SSEBanner } from "@/components/sse-banner";
import {
  Dropdown,
  DropdownContent,
  DropdownItem,
  DropdownSeparator,
  DropdownTrigger,
} from "@/components/ui/dropdown";
import { clearToken, isAuthenticated } from "@/lib/auth";
import { CrumbsProvider } from "@/lib/crumbs";
import { LiveProvider, useLiveStatus } from "@/lib/live";
import { setTheme, useTheme } from "@/lib/theme";
import { TeamProvider, useTeamScope } from "@/lib/team";

export const Route = createFileRoute("/_app")({
  beforeLoad: ({ location }) => {
    if (!isAuthenticated()) {
      throw redirect({ to: "/login", search: { return: location.href } });
    }
  },
  component: AppShell,
});

const NAV = [
  { to: "/projects", label: "Projects" },
  { to: "/servers", label: "Servers" },
  { to: "/templates", label: "Templates" },
  { to: "/settings", label: "Settings" },
] as const;

function AppShell() {
  return (
    <TeamProvider>
      <LiveProvider>
        <CrumbsProvider>
          <div className="flex min-h-dvh flex-col bg-bg">
            <CommandPalette />
            <TopBar />
            <LiveBanner />
            <main className="mx-auto w-full max-w-[1400px] flex-1">
              <Outlet />
            </main>
          </div>
        </CrumbsProvider>
      </LiveProvider>
    </TeamProvider>
  );
}

function LiveBanner() {
  return <SSEBanner status={useLiveStatus()} />;
}

/** The wordmark. Set in the sans, split at the syllable, accent on "Panel". */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={className}>
      Cypher<span className="text-accent">Panel</span>
    </span>
  );
}

function TopBar() {
  return (
    <header className="sticky top-0 z-30 border-b-[1.5px] border-border-strong bg-bg">
      {/* The bar never scrolls. Four short items always fit beside the
          wordmark, so the nav is shrink-0 and owns its width; the right-hand
          controls give ground instead (the ⌘K pill hides below lg, the team
          name below sm). Below sm the nav wraps onto its own full-width line
          rather than scrolling — a scroll container here both clipped the
          active underline, which is pulled 1.5px down onto the header rule,
          and painted a scrollbar inside 56px of chrome. */}
      <div className="mx-auto flex min-h-14 max-w-[1400px] flex-wrap items-stretch gap-x-6 px-4 sm:flex-nowrap sm:px-8">
        <Link
          to="/projects"
          aria-label="CypherPanel — projects"
          className="order-1 flex h-14 shrink-0 items-center text-base font-bold tracking-tight"
        >
          <Wordmark />
        </Link>

        <nav
          aria-label="Main"
          className="order-3 flex w-full shrink-0 items-stretch gap-5 sm:order-2 sm:w-auto"
        >
          {NAV.map(({ to, label }) => (
            <Link
              key={to}
              to={to}
              className="-mb-[1.5px] flex items-center whitespace-nowrap border-b-[2.5px] border-transparent pb-2.5 text-[13.5px] font-medium text-text-mid hover:text-text sm:pb-0"
              activeProps={{ className: "border-accent text-text" }}
            >
              {label}
            </Link>
          ))}
        </nav>

        <div className="order-2 ml-auto flex h-14 shrink-0 items-center gap-2.5 sm:order-3">
          <button
            type="button"
            onClick={openCommandPalette}
            className="hidden items-center gap-2 rounded-full border border-border bg-surface py-1.5 pl-3 pr-2 text-[12.5px] text-text-faint hover:border-border-strong hover:text-text-mid lg:flex"
          >
            <Search className="h-3.5 w-3.5" aria-hidden />
            Jump to anything
            <kbd className="rounded border border-border px-1.5 py-px font-mono text-[10px]">⌘K</kbd>
          </button>
          <ThemeToggle />
          <AccountMenu />
        </div>
      </div>
    </header>
  );
}

function ThemeToggle() {
  const theme = useTheme();
  const next = theme === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
      onClick={() => setTheme(next)}
      className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-border bg-surface text-text-mid hover:border-border-strong hover:text-text"
    >
      {theme === "dark" ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
    </button>
  );
}

/** Initials for the team avatar chip — two letters, from word starts. */
function initials(name: string): string {
  const parts = name.trim().split(/[\s\-_]+/).filter(Boolean);
  if (parts.length === 0) return "··";
  const first = parts[0]?.[0] ?? "";
  const second = parts.length > 1 ? (parts[1]?.[0] ?? "") : (parts[0]?.[1] ?? "");
  return (first + second).toUpperCase();
}

/** Team scope + account in one chip — teams are context, not a destination. */
function AccountMenu() {
  const me = useGetMe();
  const navigate = useNavigate();
  const { teamId, setTeamId } = useTeamScope();
  const logout = useLogout({
    mutation: {
      onSettled: () => {
        clearToken();
        void navigate({ to: "/login" });
      },
    },
  });

  const teams = me.data?.teams ?? [];
  const current = teams.find((t) => t.id === teamId);
  const label = current?.name ?? (teams.length > 1 ? "All teams" : (teams[0]?.name ?? "Account"));

  return (
    <Dropdown>
      <DropdownTrigger asChild>
        <button
          type="button"
          className="flex shrink-0 items-center gap-2 rounded-full border border-border bg-surface py-1 pl-1 pr-2.5 text-[12.5px] font-medium text-text hover:border-border-strong"
        >
          <span className="flex h-6 w-6 items-center justify-center rounded-full bg-primary font-mono text-[10px] text-primary-fg">
            {initials(label)}
          </span>
          <span className="hidden max-w-32 truncate sm:inline">{label}</span>
          <ChevronDown className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
        </button>
      </DropdownTrigger>
      <DropdownContent align="end" className="w-56">
        <div className="truncate px-2 py-1.5 font-mono text-[11px] text-text-faint" title={me.data?.email}>
          {me.data?.email ?? "…"}
        </div>
        {teams.length > 1 && (
          <>
            <DropdownSeparator />
            <div className="eyebrow px-2 pb-1 pt-1.5">Team scope</div>
            <DropdownItem onSelect={() => setTeamId(null)}>All teams</DropdownItem>
            {teams.map((t) => (
              <DropdownItem key={t.id} onSelect={() => setTeamId(t.id)}>
                {t.name}
              </DropdownItem>
            ))}
          </>
        )}
        <DropdownSeparator />
        <DropdownItem onSelect={() => logout.mutate()} className="flex items-center gap-2">
          <LogOut className="h-3.5 w-3.5" aria-hidden /> Sign out
        </DropdownItem>
      </DropdownContent>
    </Dropdown>
  );
}
