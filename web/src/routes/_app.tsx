// The authed shell. Mission Control puts navigation in a top bar rather than a
// sidebar (ui-principles §4, revised 2026-08-02): the same four items, but the
// full width of the viewport belongs to the content, which is what an ops tool
// full of wide tables actually needs. The bar sits on an ink rule — the
// strongest line in the product — so chrome and content never blur together.
import {
  createFileRoute,
  Link,
  Outlet,
  redirect,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import {
  Boxes,
  Check,
  ChevronDown,
  Inbox,
  LayoutTemplate,
  LogOut,
  Moon,
  Search,
  Server,
  Settings,
  Sun,
  UserRound,
} from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { useGetMe, useLogout } from "@/api/gen/auth/auth";
import { PlaneOfflinePage } from "@/components/error-page";
import { CommandPalette, openCommandPalette } from "@/components/command-palette";
import { InboxBell } from "@/components/inbox-bell";
import { ShortcutsOverlay } from "@/components/shortcuts-overlay";
import { SSEBanner } from "@/components/sse-banner";
import { UserAvatar } from "@/components/user-avatar";
import {
  Dropdown,
  DropdownContent,
  DropdownItem,
  DropdownSeparator,
  DropdownTrigger,
} from "@/components/ui/dropdown";
import { clearToken, isAuthenticated } from "@/lib/auth";
import { CrumbsProvider } from "@/lib/crumbs";
import { APP_ROUTE, APP_TABS, requestDeploy, shouldIgnoreKey } from "@/lib/keys";
import { LiveProvider, useLiveStatus } from "@/lib/live";
import { relativeTime } from "@/lib/time";
import { toggleTheme, useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { TeamProvider, useTeamScope } from "@/lib/team";

export const Route = createFileRoute("/_app")({
  beforeLoad: ({ location }) => {
    if (!isAuthenticated()) {
      throw redirect({ to: "/login", search: { return: location.href } });
    }
  },
  component: AppShell,
});

/** The top bar's four destinations (ui-principles §4 fixes the count). */
const NAV = [
  { to: "/projects", label: "Projects" },
  { to: "/servers", label: "Servers" },
  { to: "/templates", label: "Templates" },
  { to: "/settings", label: "Settings" },
] as const;

function AppShell() {
  return (
    <PlaneGate>
      <TeamProvider>
        <LiveProvider>
          <CrumbsProvider>
            <div className="flex min-h-dvh flex-col bg-bg">
              <SkipLink />
              <CommandPalette />
              <AppShortcuts />
              <TopBar />
              <LiveBanner />
              {/* The bottom bar is fixed, so the last rows of any page would
                  sit under it without this — and it grows by the notch inset on
                  a phone that has one, so the reserve has to grow with it.
                  `tabIndex={-1}` lets the skip link land focus here without
                  putting the region itself in the tab order. */}
              <main
                id="main"
                tabIndex={-1}
                className="mx-auto w-full max-w-[1400px] flex-1 pb-[calc(4rem+env(safe-area-inset-bottom))] outline-none sm:pb-0"
              >
                <Outlet />
              </main>
              <BottomTabs />
            </div>
          </CrumbsProvider>
        </LiveProvider>
      </TeamProvider>
    </PlaneGate>
  );
}

/**
 * Canvas 14g, TAB ORDER: the sequence starts at the top bar, and WCAG 2.4.1
 * asks for a way past it. The link is the first stop on every page, invisible
 * until it has focus, and drawn as the ink pill so it is unmistakably a
 * control when it appears. It focuses <main> itself rather than relying on
 * the hash so the router's history is not touched.
 */
function SkipLink() {
  return (
    <a
      href="#main"
      onClick={(e) => {
        e.preventDefault();
        document.getElementById("main")?.focus();
      }}
      className="sr-only z-50 rounded-full bg-primary px-[18px] py-2 text-[12.5px] font-semibold text-primary-fg focus:not-sr-only focus:fixed focus:left-4 focus:top-3"
    >
      Skip to content
    </a>
  );
}

/**
 * 8c is not an SSE drop — SSEBanner already answers that. It is the panel
 * losing cypherd altogether, which the identity query is the first thing to
 * notice: an HTTP answer of any kind (including 401, which redirects) means the
 * plane is up, so only a transport-level failure counts. Waiting for a retry to
 * burn keeps a single slow request from replacing the whole app, and any cached
 * identity means we can still render the shell honestly with stale statuses.
 */
function PlaneGate({ children }: { children: ReactNode }) {
  const me = useGetMe();
  const unreachable =
    me.isError && !(me.error instanceof ApiError) && me.data === undefined && me.failureCount > 1;

  // Freshness has to be remembered rather than read off the query: the state
  // this page renders in is the one where there is no data, and react-query
  // stamps `dataUpdatedAt` only alongside data. A ref outlives that.
  const lastSync = useRef<number | undefined>(undefined);
  if (me.isSuccess) lastSync.current = me.dataUpdatedAt;

  // Starting a refetch clears the error and resets the failure count, so
  // `unreachable` drops to false every time the poll fires — which, unlatched,
  // swapped this page and the entire shell back and forth every few seconds
  // for as long as cypherd was down, remounting the providers and refiring
  // every query on each pass. Once shown, the page stays until an answer
  // actually arrives; a retry in flight leaves the button under the pointer
  // that pressed it.
  const [offline, setOffline] = useState(false);
  useEffect(() => {
    if (unreachable) setOffline(true);
    else if (me.isSuccess) setOffline(false);
  }, [unreachable, me.isSuccess]);

  if (!unreachable && !offline) return children;
  return (
    <PlaneOfflinePage
      lastSyncLabel={
        lastSync.current ? relativeTime(new Date(lastSync.current).toISOString()) : undefined
      }
      onRetry={() => void me.refetch()}
    />
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
      {/* 56px, and it stays 56px: below `sm` the nav moves to the bottom bar
          rather than wrapping onto a second full-width line, which used to
          push roughly 90px of chrome above every page and left the accent
          underline floating mid-screen instead of riding the header rule. */}
      <div className="mx-auto flex h-14 max-w-[1400px] items-stretch gap-x-[26px] px-4 sm:px-8">
        <Link
          to="/projects"
          aria-label="CypherPanel — projects"
          className="flex shrink-0 items-center text-base font-bold tracking-tight"
        >
          <Wordmark />
        </Link>

        <nav aria-label="Main" className="hidden shrink-0 items-stretch gap-[22px] sm:flex">
          {NAV.map(({ to, label }) => (
            // The resting colours live in `inactiveProps`, not in the base
            // class: TanStack concatenates base and active, so a
            // `border-transparent` in the base would sit beside `border-accent`
            // at equal specificity and the sheet's own ordering would decide
            // which one paints. It decided against the active one.
            <Link
              key={to}
              to={to}
              className="-mb-[1.5px] flex items-center whitespace-nowrap border-b-[2.5px] text-[13.5px] font-medium hover:text-text"
              activeProps={{ className: "border-accent text-text" }}
              inactiveProps={{ className: "border-transparent text-text-mid" }}
            >
              {label}
            </Link>
          ))}
        </nav>

        <div className="ml-auto flex shrink-0 items-center gap-2.5">
          <button
            type="button"
            onClick={openCommandPalette}
            className="hidden items-center gap-2 rounded-full border border-border-input bg-surface py-1.5 pl-3 pr-2 text-[12.5px] text-text-faint hover:border-border-strong hover:text-text-mid lg:flex"
          >
            <Search className="h-3.5 w-3.5" aria-hidden />
            Jump to anything
            <kbd className="rounded border border-border px-1.5 py-px font-mono text-[10px]">⌘K</kbd>
          </button>
          {/* 14b. A phone has no ⌘K, so the pill it replaces has to be a real
              control and not just a hint about a keystroke. */}
          <button
            type="button"
            onClick={openCommandPalette}
            aria-label="Jump to anything"
            className="flex h-[34px] w-[34px] items-center justify-center rounded-full border border-border-input bg-surface text-text-mid hover:border-border-strong hover:text-text lg:hidden"
          >
            <Search className="h-4 w-4" aria-hidden />
          </button>
          {/* 13u. Chrome, not navigation: the bell opens a panel in place and
              leads nowhere, so it belongs in this control cluster rather than
              as a fifth item in a nav ui-principles §4 fixes at four. */}
          <InboxBell />
          <AccountMenu />
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}

/**
 * 14b/14e. Below `sm` navigation is a fixed bottom bar: thumb-reachable, and
 * the active item carries the same accent rule as the top bar, pulled up onto
 * the bar's own hairline.
 *
 * The canvas draws Projects · Servers · Inbox · Profile, and that is what the
 * bar carries: Templates and Settings stay reachable through ⌘K (the search
 * circle) and the account menu, which is where a phone puts the things it
 * does not visit every hour.
 */
const BOTTOM_TABS = [
  { to: "/projects", label: "Projects", icon: Boxes },
  { to: "/servers", label: "Servers", icon: Server },
  { to: "/inbox", label: "Inbox", icon: Inbox },
  { to: "/settings/profile", label: "Profile", icon: UserRound },
] as const;

const TAB_CLASS =
  // The bottom of a notched phone belongs to the home indicator, which would
  // otherwise be drawn straight through these labels.
  "-mt-px flex flex-1 flex-col items-center gap-[3px] border-t-2 pb-[max(0.75rem,env(safe-area-inset-bottom))] pt-2.5 text-[10.5px]";
const TAB_ACTIVE = "border-accent font-semibold text-text";
const TAB_INACTIVE = "border-transparent text-text-faint";

function BottomTabs() {
  return (
    <nav
      aria-label="Main"
      className="fixed inset-x-0 bottom-0 z-30 flex border-t border-border bg-surface sm:hidden"
    >
      {BOTTOM_TABS.map((t) => (
        <BottomTab key={t.to} {...t} />
      ))}
    </nav>
  );
}

function BottomTab({ to, label, icon: Icon }: (typeof BOTTOM_TABS)[number]) {
  return (
    <Link
      to={to}
      className={TAB_CLASS}
      activeProps={{ className: TAB_ACTIVE }}
      inactiveProps={{ className: TAB_INACTIVE }}
    >
      <Icon className="h-4 w-4" aria-hidden />
      {label}
    </Link>
  );
}

/**
 * ☾/☀ at the far right of the top bar (canvas 12g/14b). Hidden below `sm`: the
 * phone's header carries only search and the bell, and the Profile tab's
 * Light/Dark/Auto field is where a phone changes theme. Both write the same
 * preference (lib/theme.ts), so neither can disagree with the other.
 */
function ThemeToggle() {
  const theme = useTheme();
  const next = theme === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      aria-label={`Switch to ${next} theme`}
      title={`Switch to ${next} theme`}
      onClick={toggleTheme}
      // 34px on `border-border-input`, matching the search circle and the team
      // chip it sits between: one control line, one height, across the cluster.
      className="hidden h-[34px] w-[34px] shrink-0 items-center justify-center rounded-full border border-border-input bg-surface text-text-mid hover:border-border-strong hover:text-text sm:flex"
    >
      {theme === "dark" ? <Sun className="h-3.5 w-3.5" aria-hidden /> : <Moon className="h-3.5 w-3.5" aria-hidden />}
    </button>
  );
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
  const email = me.data?.email ?? "";
  const displayName = me.data?.display_name ?? "";
  // The chip names the person, not the team. The canvas draws a team pill here,
  // but the control it actually opens is the account menu — and a menu that
  // signs you out should say whose session it is about to end. Team scope keeps
  // its own section inside, where the active one is ticked.
  const label = displayName || email.split("@")[0] || "Account";
  const scoped = teams.find((t) => t.id === teamId);

  return (
    <Dropdown>
      <DropdownTrigger asChild>
        {/* 14b's phone header has no account chip — Profile is a tab there —
            but the chip is still the only way to sign out or change team
            scope, so it stays until the profile page carries both. */}
        <button
          type="button"
          aria-label={`Account menu for ${displayName || email}`}
          className="flex shrink-0 items-center gap-2 rounded-full border border-border-input bg-surface py-[5px] pl-1.5 pr-3 text-[12.5px] font-medium text-text hover:border-border-strong"
        >
          <UserAvatar
            userId={me.data?.id}
            name={displayName}
            email={email}
            className="h-[22px] w-[22px]"
            textClassName="text-[10px]"
          />
          <span className="hidden max-w-32 truncate sm:inline">{label}</span>
          <ChevronDown className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
        </button>
      </DropdownTrigger>
      <DropdownContent align="end" className="w-56">
        <div className="truncate px-2 py-1.5 font-mono text-[11px] text-text-faint" title={email}>
          {email || "…"}
        </div>
        {teams.length > 1 && (
          <>
            <DropdownSeparator />
            {/* The chip used to carry the scope; now that it carries the person,
                the tick is the only thing that says which team you are looking
                at, so it is not decoration. */}
            <div className="eyebrow px-2 pb-1 pt-1.5">Team scope</div>
            <DropdownItem onSelect={() => setTeamId(null)} className="flex items-center gap-2">
              <Check className={cn("h-3 w-3 shrink-0", scoped ? "invisible" : "text-accent")} aria-hidden />
              All teams
            </DropdownItem>
            {teams.map((t) => (
              <DropdownItem key={t.id} onSelect={() => setTeamId(t.id)} className="flex items-center gap-2">
                <Check
                  className={cn("h-3 w-3 shrink-0", scoped?.id === t.id ? "text-accent" : "invisible")}
                  aria-hidden
                />
                {t.name}
              </DropdownItem>
            ))}
          </>
        )}
        <DropdownSeparator />
        {/* Below `sm` the bar carries Projects/Servers/Inbox/Profile (14b), so
            the two places it dropped are reachable from here. */}
        <DropdownItem
          onSelect={() => void navigate({ to: "/templates" })}
          className="flex items-center gap-2 sm:hidden"
        >
          <LayoutTemplate className="h-3.5 w-3.5" aria-hidden /> Templates
        </DropdownItem>
        <DropdownItem
          onSelect={() => void navigate({ to: "/settings" })}
          className="flex items-center gap-2 sm:hidden"
        >
          <Settings className="h-3.5 w-3.5" aria-hidden /> Settings
        </DropdownItem>
        {/* The menu already names who you are, so the next question it should
            answer is where to change that (canvas 13i). */}
        <DropdownItem
          onSelect={() => void navigate({ to: "/settings/profile" })}
          className="flex items-center gap-2"
        >
          <UserRound className="h-3.5 w-3.5" aria-hidden /> Profile settings
        </DropdownItem>
        <DropdownItem onSelect={() => logout.mutate()} className="flex items-center gap-2">
          <LogOut className="h-3.5 w-3.5" aria-hidden /> Sign out
        </DropdownItem>
      </DropdownContent>
    </Dropdown>
  );
}

const LOGS_TAB = APP_TABS.findIndex((t) => t.segment === "logs") + 1;

/**
 * 14f. Every key the overlay prints is wired here or in lib/keys.ts, and every
 * key wired is printed there — a printed shortcut that does nothing is worse
 * than none. In the card's own words, "no shortcut triggers anything
 * destructive": `d` starts a deploy through the same busy-guarded button a
 * click uses, and deletes and rollbacks are not on the list at all. `j`/`k`
 * belong to the page's own row model (useRowNavigation) rather than to the
 * shell, because only the list knows what a row is.
 */
function AppShortcuts() {
  const [helpOpen, setHelpOpen] = useState(false);
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  // Read through a ref so the listener is bound once: rebinding on every
  // navigation would also reset a half-typed `g` chord.
  const app = useRef<{ projectId: string; appId: string } | null>(null);
  const match = APP_ROUTE.exec(pathname);
  app.current = match?.[1] && match[2] ? { projectId: match[1], appId: match[2] } : null;

  useEffect(() => {
    let chord = false;
    let chordTimer: ReturnType<typeof setTimeout> | undefined;
    const arm = () => {
      chord = true;
      clearTimeout(chordTimer);
      chordTimer = setTimeout(() => (chord = false), 1200);
    };

    const goTab = (index: number) => {
      const target = app.current;
      if (!target) return;
      const params = { projectId: target.projectId, appId: target.appId };
      // The switch names each route literally so the router can type it; the
      // order is APP_TABS's, which the tab strip draws from too.
      switch (APP_TABS[index - 1]?.segment) {
        case "":
          return void navigate({ to: "/projects/$projectId/applications/$appId", params });
        case "deployments":
          return void navigate({ to: "/projects/$projectId/applications/$appId/deployments", params });
        case "logs":
          return void navigate({ to: "/projects/$projectId/applications/$appId/logs", params });
        case "env":
          return void navigate({ to: "/projects/$projectId/applications/$appId/env", params });
        case "previews":
          return void navigate({ to: "/projects/$projectId/applications/$appId/previews", params });
        case "tasks":
          return void navigate({ to: "/projects/$projectId/applications/$appId/tasks", params });
        case "settings":
          return void navigate({ to: "/projects/$projectId/applications/$appId/settings", params });
      }
    };

    const onKey = (e: KeyboardEvent) => {
      if (shouldIgnoreKey(e)) return;

      const wasChord = chord;
      chord = false;

      if (e.key === "?") {
        e.preventDefault();
        setHelpOpen(true);
        return;
      }
      if (e.key === "g") {
        arm();
        return;
      }
      if (wasChord && e.key === "p") {
        e.preventDefault();
        void navigate({ to: "/projects" });
        return;
      }
      if (wasChord && e.key === "s") {
        e.preventDefault();
        void navigate({ to: "/servers" });
        return;
      }
      if (wasChord && e.key === "i") {
        e.preventDefault();
        void navigate({ to: "/inbox" });
        return;
      }
      if (!app.current) return;
      if (e.key === "l") {
        e.preventDefault();
        goTab(LOGS_TAB);
        return;
      }
      if (e.key === "d") {
        e.preventDefault();
        requestDeploy();
        return;
      }
      const tab = Number(e.key);
      if (Number.isInteger(tab) && tab >= 1 && tab <= APP_TABS.length) {
        e.preventDefault();
        goTab(tab);
      }
    };

    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      clearTimeout(chordTimer);
    };
  }, [navigate]);

  return <ShortcutsOverlay open={helpOpen} onOpenChange={setHelpOpen} />;
}
