// Full-screen panel errors — design canvas 8a–8e (read from their dark twins
// 13p–13t; every value maps through the token sheet, so one component serves
// both themes).
//
// Each page comes in two fits. Standalone (`embedded` unset) it owns the
// viewport, which is what the router mounts for an unknown URL or a render
// fault. Embedded, it sits where a page's content would have been — inside the
// shell, under the top bar — which is how an API answer reaches it: React
// Query hands errors back as state rather than throwing them, so PageState and
// ResourceGone render these in place instead of an inline red box.
//
// The shared shape is a 96px mono numeral with its middle digit in accent, a
// 19px headline that says what happened in the operator's terms, one
// explanatory line, and actions that are never a dead end (ui-principles §11).
import { Link, useParams } from "@tanstack/react-router";
import { type ReactNode, useEffect, useState } from "react";
import { ApiError, NetworkError, faultBundleOf, requestLineOf } from "@/api/client";
import { useGetMe } from "@/api/gen/auth/auth";
import { useGetProject } from "@/api/gen/projects/projects";
import { useListTeamMembers, useListUsers } from "@/api/gen/teams/teams";
import { openCommandPalette } from "@/components/command-palette";
import { CopyButton } from "@/components/copy-field";
import { ReportIssueDialog } from "@/components/report-issue-dialog";
import { RequestAccessDialog } from "@/components/request-access-dialog";
import { ActionButton } from "@/components/ui/action-button";
import { type Role } from "@/lib/roles";
import { useTeamScope } from "@/lib/team";
import { cn } from "@/lib/utils";

function Frame({ children, embedded }: { children: ReactNode; embedded?: boolean }) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center px-8 text-center text-text",
        embedded ? "min-h-[60vh] py-16" : "min-h-dvh bg-bg py-10",
      )}
    >
      {children}
    </div>
  );
}

/** The 4·0·4 numeral: middle digit accent, the rest ink. */
function Numeral({ code }: { code: string }) {
  const [a, b, c] = code.split("");
  return (
    <div className="font-mono text-[96px] leading-none tracking-[-0.04em]" aria-hidden>
      {a}
      <span className="text-accent">{b}</span>
      {c}
    </div>
  );
}

function Headline({ children }: { children: ReactNode }) {
  return (
    <h1 className="mb-1.5 mt-4 text-[19px] font-bold tracking-[-0.02em] text-text">{children}</h1>
  );
}

function Explain({ children, wide }: { children: ReactNode; wide?: boolean }) {
  return (
    <p className={cn("text-[13px] leading-[1.55] text-text-mid", wide ? "max-w-[370px]" : "max-w-[340px]")}>
      {children}
    </p>
  );
}

function Actions({ children }: { children: ReactNode }) {
  return <div className="mt-[22px] flex flex-wrap items-center justify-center gap-2.5">{children}</div>;
}

/** The filled pill: paper-on-ink in dark, ink-on-paper in light. */
const solid =
  "rounded-full bg-primary px-[18px] py-[9px] text-[12.5px] font-semibold text-primary-fg hover:bg-primary-hover";
/** The outlined pill beside it. */
const outline =
  "rounded-full border border-border-strong bg-surface px-[18px] py-[9px] text-[12.5px] font-semibold text-text hover:bg-raised";

/**
 * Whole seconds until `until` (ms epoch), ticking once a second; undefined
 * while there is no deadline. Shared by the throttle countdown (8e) and the
 * retry countdown (8c), which are the same clock with different copy.
 */
export function useSecondsLeft(until: number | null | undefined): number | undefined {
  const left = () => (until ? Math.max(0, Math.ceil((until - Date.now()) / 1000)) : undefined);
  const [seconds, setSeconds] = useState(left);
  useEffect(() => {
    setSeconds(left());
    if (!until) return;
    const t = setInterval(() => setSeconds(left()), 1000);
    return () => clearInterval(t);
    // `left` closes over `until` and nothing else.
  }, [until]);
  return seconds;
}

/** 8a — 404. Two meanings: never existed, or a teammate deleted it. */
export function NotFoundPage({
  resource,
  backTo = "/projects",
  backLabel = "← Projects",
  auditLogHref,
  embedded,
}: {
  /** The id or name that was asked for — the thing that "may have been deleted". */
  resource?: string;
  /** The way back. Defaults to the projects list; a resource layout passes its parent. */
  backTo?: string;
  backLabel?: string;
  /** Set once the audit log page lands; omitted, the third action is hidden. */
  auditLogHref?: string;
  embedded?: boolean;
}) {
  return (
    <Frame embedded={embedded}>
      <Numeral code="404" />
      <Headline>This page doesn't exist.</Headline>
      <Explain>
        Or it did — {resource ? <span className="mono text-[12px]">{resource}</span> : "the resource"} may have
        been deleted by a teammate. The audit log remembers.
      </Explain>
      <Actions>
        <Link to={backTo} className={solid}>
          {backLabel}
        </Link>
        {/* ⌘K is the palette the panel already ships; this is the same door,
            opened by asking for it rather than by faking the keystroke. */}
        <button type="button" className={outline} onClick={openCommandPalette}>
          ⌘K Search
        </button>
        {/* The third action the canvas draws. It is what makes "deleted by a
            teammate" actionable rather than a shrug — but it is only offered
            once the audit log exists (canvas 3g, V1.x): a 404 whose way out is
            another 404 is the dead end this page exists to avoid. */}
        {auditLogHref && (
          <Link to={auditLogHref} className={outline}>
            Audit log
          </Link>
        )}
      </Actions>
    </Frame>
  );
}

/**
 * 8b — 403. The design is emphatic that this page *names the fix*: which
 * action, which role it needs, which role you hold, and who can grant it. A
 * 403 that only says "forbidden" makes the operator guess.
 */
export function ForbiddenPage({
  action,
  needs,
  held,
  scope = "team",
  owners = [],
  onRequestAccess,
  embedded,
}: {
  /** What was refused, as a gerund: "Deploying an application". */
  action: string;
  /** The rank the route wanted — member / admin / owner. */
  needs: Role;
  /** The rank the caller holds; unknown while identity is still resolving. */
  held?: string;
  /** Panel roles gate infrastructure routes; team roles gate everything in a project. */
  scope?: "panel" | "team";
  owners?: string[];
  onRequestAccess?: () => void;
  embedded?: boolean;
}) {
  const where = scope === "panel" ? "on this panel" : "on this team";
  return (
    <Frame embedded={embedded}>
      <Numeral code="403" />
      <Headline>You can see this, but not touch it.</Headline>
      <Explain wide>
        <b className="text-text">{action}</b> needs the <b className="text-text">{needs}</b> role —{" "}
        {held ? (
          <>
            you're a <b className="text-text">{held}</b> {where}.
          </>
        ) : (
          <>yours is lower {where}.</>
        )}
      </Explain>
      <Actions>
        {owners[0] && (
          <button type="button" className={solid} onClick={onRequestAccess}>
            Request access from {owners[0].split("@")[0]}@
          </button>
        )}
        <button type="button" className={outline} onClick={() => window.history.back()}>
          Back
        </button>
      </Actions>
      {owners.length > 0 && (
        <p className="mono mt-[18px] text-[11px] text-text-faint">owners: {owners.join(", ")}</p>
      )}
    </Frame>
  );
}

// ─── What a 403 was about ────────────────────────────────────────────────────
//
// cypherd's 403 body is only `{"error":"insufficient role"}`; the rank a route
// wants is written in core/api/rest (authz.go and the require*Role calls in
// each handler), and this is the reading of it that lets the page name the
// fix. It is a mirror, so it has to be kept in step: the proper source is a
// `needed_role` field on the Error envelope, which the API does not have yet.

const PANEL_ROUTE = /^\/api\/v1\/(servers|deploy-keys|backup-targets|users|panel)(\/|$)/;
const TEAM_ROUTE = /^\/api\/v1\/teams\/[^/]+(\/members(\/[^/]+)?)?$/;

/** The rank the route wanted and which of the two role ladders it was read from. */
export function requiredRoleFor(method: string, path: string): { needs: Role; scope: "panel" | "team" } {
  if (PANEL_ROUTE.test(path)) {
    const oneUser = /^\/api\/v1\/users\/[^/]+$/.test(path) && method !== "GET";
    return { scope: "panel", needs: oneUser ? "owner" : "admin" };
  }
  if (method === "POST" && path === "/api/v1/teams") return { scope: "panel", needs: "admin" };
  if (method === "POST" && path === "/api/v1/projects") return { scope: "team", needs: "admin" };
  const team = TEAM_ROUTE.exec(path);
  if (team) {
    if (method === "GET") return { scope: "team", needs: "member" };
    // Renaming or deleting the team itself is the owner's; its roster is the admin's.
    return { scope: "team", needs: team[1] ? "admin" : "owner" };
  }
  if (method === "DELETE" && /^\/api\/v1\/projects\/[^/]+$/.test(path)) return { scope: "team", needs: "admin" };
  return { scope: "team", needs: "member" };
}

const NOUNS: Record<string, string> = {
  servers: "a server",
  "deploy-keys": "a deploy key",
  "backup-targets": "a backup target",
  users: "a user",
  panel: "panel settings",
  teams: "a team",
  projects: "a project",
  environments: "an environment",
  applications: "an application",
  deployments: "a deployment",
  databases: "a database",
  previews: "a preview",
  notifiers: "a notifier",
  "webhook-endpoints": "a webhook endpoint",
  "webhook-deliveries": "a webhook delivery",
  "shared-variables": "a shared variable",
  "scheduled-tasks": "a scheduled task",
  templates: "a template",
  inbox: "the inbox",
};

/** Action verbs for the routes that do something rather than edit something. */
const VERBS: [RegExp, string][] = [
  [/\/deploy$/, "Deploying an application"],
  [/\/rollback$/, "Rolling back a deployment"],
  [/\/restore$/, "Restoring a database"],
  [/\/backups\/[^/]+\/run$/, "Running a backup"],
  [/\/backups$/, "Scheduling a backup"],
  [/\/start$/, "Starting a database"],
  [/\/stop$/, "Stopping a database"],
  [/\/reset-password$/, "Resetting a database password"],
  [/\/env(\/|$)/, "Changing env vars"],
  [/\/test$/, "Sending a test"],
  [/\/ping$/, "Pinging a webhook endpoint"],
  [/\/rotate-secret$/, "Rotating a webhook secret"],
  [/\/redeliver$/, "Redelivering a webhook"],
  [/\/zones\/refresh$/, "Refreshing DNS zones"],
  [/\/install$/, "Installing a template"],
  [/\/members(\/|$)/, "Changing team members"],
];

/** "Deleting a server", "Changing env vars" — the refused request, in words. */
export function actionFor(method: string, path: string): string {
  for (const [re, verb] of VERBS) if (re.test(path)) return verb;
  const first = path.replace(/^\/api\/v1\//, "").split("/")[0] ?? "";
  const noun = NOUNS[first] ?? "this";
  switch (method) {
    case "POST":
      return `Creating ${noun}`;
    case "PATCH":
    case "PUT":
      return `Changing ${noun}`;
    case "DELETE":
      return `Deleting ${noun}`;
    default:
      return `Viewing ${noun}`;
  }
}

/**
 * The 403 page assembled from a refused request. Half of what it says has to
 * be looked up here: the caller's own rank from /auth/me, and the people who
 * can raise it from the team's member list (or, for panel routes, the user
 * list — which only an admin may read, so a member is told the fix without a
 * name). The team is the scoped one, the project's own when the page has a
 * project id, or the only one there is.
 */
export function ForbiddenForError({ error, embedded }: { error: ApiError; embedded?: boolean }) {
  const { needs, scope } = requiredRoleFor(error.method, error.path);
  const action = actionFor(error.method, error.path);
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const params = useParams({ strict: false });
  const project = useGetProject(params.projectId ?? "", { query: { enabled: Boolean(params.projectId) } });
  const teams = me.data?.teams ?? [];
  const team =
    teams.find((t) => t.id === project.data?.project.team_id) ??
    teams.find((t) => t.id === teamId) ??
    (teams.length === 1 ? teams[0] : undefined);

  const members = useListTeamMembers(team?.id ?? "", { query: { enabled: scope === "team" && team !== undefined } });
  // Only an admin may read the user list; asked for before identity resolves,
  // a member would be refused a second time on this very page.
  const users = useListUsers({
    query: { enabled: scope === "panel" && me.data !== undefined && me.data.role !== "member" },
  });
  const owners =
    scope === "team"
      ? (members.data ?? []).filter((m) => m.role === "owner").map((m) => m.email)
      : (users.data ?? []).filter((u) => u.role === "owner").map((u) => u.email);
  const held = scope === "panel" ? me.data?.role : team?.role;
  const [asking, setAsking] = useState(false);

  // A team refusal has a real ask behind it now (invitations-and-access-
  // requests.md): the request lands in the owners' inbox, and granting it runs
  // the ordinary member-role path, so the last-owner guard still holds. A PANEL
  // refusal has no such route — panel rank is not team rank and there is no
  // API for asking for it — so that one is still an email, with the ask
  // already written so the owner knows which switch to flip.
  const canAskInPanel = scope === "team" && team !== undefined;

  return (
    <>
      <ForbiddenPage
        action={action}
        needs={needs}
        held={held}
        scope={scope}
        owners={owners}
        embedded={embedded}
        onRequestAccess={() => {
          if (canAskInPanel) {
            setAsking(true);
            return;
          }
          const owner = owners[0];
          if (owner === undefined) return;
          const subject = encodeURIComponent("CypherPanel — access request");
          const body = encodeURIComponent(
            `${action} needs the ${needs} role${team ? ` on ${team.name}` : ""}${held ? ` — I'm a ${held}` : ""}.`,
          );
          window.location.href = `mailto:${owner}?subject=${subject}&body=${body}`;
        }}
      />
      {canAskInPanel && (
        <RequestAccessDialog
          open={asking}
          onOpenChange={setAsking}
          teamId={team.id}
          teamName={team.name}
          role={needs}
          held={held}
          owners={owners}
          action={action}
        />
      )}
    </>
  );
}

/**
 * 8c — control plane unreachable. Deliberately calm: the single most important
 * fact is that the fleet is unaffected, because routing and containers live on
 * the servers. Panic here would be a lie about the blast radius.
 */
export function PlaneOfflinePage({
  retryEverySeconds = 5,
  retrying = false,
  lastSyncLabel,
  onRetry,
  embedded,
}: {
  /**
   * The cadence of the countdown: `onRetry` fires when it reaches zero, then
   * it restarts. Five seconds is the poll the query layer already runs while
   * the event stream is down (main.tsx), so the line says what is happening
   * either way. `0` turns it off — for a page whose only retry is a reload.
   */
  retryEverySeconds?: number;
  /** A retry in flight — the pill shows it, and the countdown waits for it. */
  retrying?: boolean;
  /** "40s ago"; omitted means this tab has never had an answer. */
  lastSyncLabel?: string;
  onRetry?: () => void;
  embedded?: boolean;
}) {
  const retry = onRetry ?? (() => location.reload());
  // The deadline is remembered, not the count, so a re-render cannot stretch
  // a second. It is re-armed at every zero crossing — after the retry it
  // triggers, or past one that is still in flight — which is what
  // "retrying in 4s" promises.
  const [until, setUntil] = useState<number | null>(null);
  useEffect(() => {
    setUntil(retryEverySeconds > 0 ? Date.now() + retryEverySeconds * 1000 : null);
  }, [retryEverySeconds]);
  const left = useSecondsLeft(until);
  useEffect(() => {
    if (left !== 0 || retryEverySeconds <= 0) return;
    if (!retrying) retry();
    setUntil(Date.now() + retryEverySeconds * 1000);
    // Runs once per zero crossing: `retry` and `retrying` are read, not watched,
    // because a retry that starts must not itself restart the clock.
  }, [left]);

  return (
    <Frame embedded={embedded}>
      <div
        className="flex size-16 items-center justify-center rounded-full border-[1.5px] border-dashed border-text-faint font-mono text-2xl text-text-faint"
        aria-hidden
      >
        ⇅
      </div>
      <Headline>Can't reach the control plane.</Headline>
      <Explain wide>
        <b className="text-text">Your apps are still serving.</b> Routing and containers live on the servers,
        not here — the panel is only the steering wheel.
      </Explain>
      <div
        role="status"
        aria-live="polite"
        className="mono mt-5 flex items-center gap-[9px] text-[12px] text-text-faint"
      >
        <span className="size-[7px] flex-none rounded-full bg-status-degraded" aria-hidden />
        {retrying ? "retrying" : left !== undefined ? `retrying in ${left}s` : "retrying"}
        {` · last sync ${lastSyncLabel ?? "never"}`}
      </div>
      <ActionButton
        variant="secondary"
        className={cn(outline, "mt-3.5")}
        state={retrying ? "busy" : "idle"}
        busyLabel="Retrying…"
        onClick={retry}
      >
        Retry now
      </ActionButton>
    </Frame>
  );
}

/**
 * 8d — 500. The canvas draws a trace id worth pasting; cypherd does not stamp
 * one yet, so the chip carries what the response actually said — the route
 * and the status — which is the same thing an issue needs, minus the lookup.
 */
export function ServerFaultPage({
  error,
  onReload,
  reloading = false,
  embedded,
}: {
  error?: unknown;
  /** Re-asks the question that failed; without it the pill reloads the page. */
  onReload?: () => void;
  reloading?: boolean;
  embedded?: boolean;
}) {
  const [reporting, setReporting] = useState(false);
  const route = requestLineOf(error);
  const status = error instanceof ApiError ? error.status : undefined;
  const bundle = faultBundleOf(error);
  return (
    <Frame embedded={embedded}>
      <Numeral code="500" />
      <Headline>The panel hit a bug. Your fleet didn't.</Headline>
      <Explain>
        This request failed inside cypherd. It's logged — attach the details if you file an issue.
      </Explain>
      {route && (
        <div className="mono mt-[18px] flex items-center gap-2.5 rounded-md bg-toast px-3.5 py-[9px] text-[12px] text-toast-text">
          {route}
          {status !== undefined && <span className="text-toast-faint">→ {status}</span>}
          <span className="-mr-1.5 rounded border border-pane-border [&_button]:text-toast-text [&_button:hover]:bg-white/5 [&_button:hover]:text-toast-text">
            <CopyButton value={bundle} label="Copy details" />
          </span>
        </div>
      )}
      <Actions>
        <ActionButton
          variant="primary"
          state={reloading ? "busy" : "idle"}
          busyLabel="Reloading…"
          onClick={onReload ?? (() => location.reload())}
        >
          Reload
        </ActionButton>
        <button type="button" className={outline} onClick={() => setReporting(true)}>
          Report issue ↗
        </button>
      </Actions>
      <ReportIssueDialog open={reporting} onOpenChange={setReporting} error={error} />
    </Frame>
  );
}

/**
 * 8e — 429. Grounded in core/auth/ratelimit.go, which throttles failed
 * sign-ins per client (five in fifteen minutes). The bar drains toward zero so
 * the wait is legible without reading the clock — when the wait is known:
 * cypherd sends no Retry-After yet, and without one the page says "in a
 * moment" in the server's own words rather than counting down a number it
 * made up.
 */
export function ThrottledPage({
  secondsLeft,
  totalSeconds,
  onTryAgain,
  embedded,
}: {
  secondsLeft?: number;
  totalSeconds?: number;
  /** Offered only when there is no countdown to end the pause by itself. */
  onTryAgain?: () => void;
  embedded?: boolean;
}) {
  const known = secondsLeft !== undefined;
  const mm = known ? Math.floor(secondsLeft / 60) : 0;
  const ss = known ? String(secondsLeft % 60).padStart(2, "0") : "00";
  const total = totalSeconds && totalSeconds > 0 ? totalSeconds : undefined;
  return (
    <Frame embedded={embedded}>
      <Numeral code="429" />
      <Headline>Too many attempts.</Headline>
      <Explain>
        Sign-in from this client is paused after repeated failures. Try again{" "}
        {known ? (
          <>
            in{" "}
            <b className="mono text-text">
              {mm}:{ss}
            </b>
          </>
        ) : (
          "in a moment"
        )}{" "}
        — or use a recovery code if you've lost your device.
      </Explain>
      {known && total && (
        <div className="mt-5 h-1.5 w-60 overflow-hidden rounded-full bg-raised" aria-hidden>
          <div
            className="h-full rounded-full bg-accent transition-[width] duration-1000 motion-reduce:transition-none"
            style={{ width: `${Math.min(100, Math.max(0, (secondsLeft / total) * 100))}%` }}
          />
        </div>
      )}
      <span role="status" aria-live="polite" className="sr-only">
        {known ? `Sign-in paused, ${secondsLeft} seconds left` : "Sign-in paused"}
      </span>
      {!known && onTryAgain && (
        <Actions>
          <button type="button" className={outline} onClick={onTryAgain}>
            Try again
          </button>
        </Actions>
      )}
    </Frame>
  );
}

/**
 * What the router mounts when a route component throws. ApiError carries the
 * status, which is what picks between the pages: a 403 is a role problem with
 * a named fix, a 404 a page that is not there, anything else a panel fault.
 */
export function ErrorForRoute({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.status === 403) return <ForbiddenForError error={error} />;
  if (error instanceof ApiError && error.status === 404) return <NotFoundPage />;
  // No query to re-ask here — the retry is a reload, and a reload on a timer
  // would loop for as long as the plane was down.
  if (error instanceof NetworkError) return <PlaneOfflinePage retryEverySeconds={0} />;
  return <ServerFaultPage error={error} />;
}
