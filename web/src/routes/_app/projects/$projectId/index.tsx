// Project home: environment switcher (tabs, not routes) + this environment's
// resources. Empty states chain the golden path (ui-principles §11).
import { createFileRoute, Link } from "@tanstack/react-router";
import { Boxes, Database as DatabaseIcon, Settings as SettingsIcon } from "lucide-react";
import { useMemo } from "react";
import { useListApplications } from "@/api/gen/applications/applications";
import { useListComposeStacks } from "@/api/gen/compose-stacks/compose-stacks";
import { useGetMe } from "@/api/gen/auth/auth";
import { useListDatabases } from "@/api/gen/databases/databases";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type { Application, ComposeStack, Database, Environment } from "@/api/gen/model";
import { NewComposeStackDialog } from "@/components/create-compose-stack-dialog";
import { NewAppDialog } from "@/components/create-application-dialog";
import { engineLabel, NewDatabaseDialog } from "@/components/create-database-dialog";
import { ProvisioningSteps, provisioningSteps } from "@/components/db-provisioning-steps";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill, type Status } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { useCrumbs } from "@/lib/crumbs";
import { useRowNavigation } from "@/lib/keys";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

interface ProjectSearch {
  env?: string;
}

export const Route = createFileRoute("/_app/projects/$projectId/")({
  validateSearch: (s: Record<string, unknown>): ProjectSearch =>
    typeof s.env === "string" ? { env: s.env } : {},
  component: ProjectHome,
});

function ProjectHome() {
  const { projectId } = Route.useParams();
  const { env } = Route.useSearch();
  const project = useGetProject(projectId);
  const envs = useListEnvironments(projectId);

  // Team / project, and no "projects" root: the team is the root of every
  // trail in the panel (ui-principles §4, canvas 1b). The team named is the
  // one that owns this project rather than the sidebar's current scope, so the
  // trail reads the same for someone looking across every team they belong to;
  // until the membership list has arrived there is no name to use, and the
  // crumb is dropped rather than guessed at.
  const teams = useGetMe().data?.teams ?? [];
  const teamName = teams.find((t) => t.id === project.data?.project.team_id)?.name;
  useCrumbs([
    ...(teamName ? [{ label: teamName, to: "/projects" }] : []),
    { label: project.data?.project.name ?? projectId },
  ]);

  const activeEnv: Environment | undefined = useMemo(() => {
    const list = envs.data ?? [];
    return list.find((e) => e.id === env) ?? list.find((e) => e.name === "production") ?? list[0];
  }, [envs.data, env]);

  return (
    <>
      <PageHeader
        title={project.data?.project.name ?? "…"}
        badge={activeEnv && <EnvRollupChip envId={activeEnv.id} />}
        actions={
          <>
            <Link to="/projects/$projectId/settings" params={{ projectId }}>
              <Button variant="secondary">
                <SettingsIcon className="h-3.5 w-3.5" aria-hidden /> Project settings
              </Button>
            </Link>
            {activeEnv && (
              <NewAppDialog
                envId={activeEnv.id}
                projectId={projectId}
                projectName={project.data?.project.name ?? ""}
                envName={activeEnv.name}
              />
            )}
          </>
        }
        below={
          // Environments are tabs, not routes: switching one is a filter on
          // this page, not a new place (web-ui-design.md §3).
          <PageState query={envs} loading={<div className="h-9" />}>
            {(list) => (
              <div className="-mb-px flex gap-1 overflow-x-auto" role="tablist" aria-label="Environments">
                {list.map((e) => (
                  <Link
                    key={e.id}
                    to="/projects/$projectId"
                    params={{ projectId }}
                    search={{ env: e.id }}
                    role="tab"
                    aria-selected={activeEnv?.id === e.id}
                    className={cn(
                      "whitespace-nowrap rounded-t-lg px-4 py-2.5 text-[13px] text-text-mid hover:text-text",
                      activeEnv?.id === e.id &&
                        "border border-b-0 border-border bg-surface font-semibold text-text",
                    )}
                  >
                    {e.name}
                  </Link>
                ))}
              </div>
            )}
          </PageState>
        }
      />
      {/* The resource board sits on the raised surface the active tab joins. */}
      <div className="bg-surface">
        <PageBody className="pt-[26px] pb-9">
          {activeEnv && (
            <EnvResources
              projectId={projectId}
              projectName={project.data?.project.name ?? ""}
              envId={activeEnv.id}
              envName={activeEnv.name}
            />
          )}
        </PageBody>
      </div>
    </>
  );
}

/** Worst-first: the loudest state in the environment is the one that names it. */
const SEVERITY: Record<Status, number> = {
  error: 5,
  degraded: 4,
  deploying: 3,
  unknown: 2,
  running: 1,
  stopped: 0,
};

/**
 * The chip beside the project name (canvas 1b). It rolls up the ACTIVE
 * environment rather than the whole project, because the title row sits
 * directly above the environment tabs — a chip that averaged production and a
 * pr-branch would contradict the board underneath it.
 */
function EnvRollupChip({ envId }: { envId: string }) {
  const apps = useListApplications(envId);
  const dbs = useListDatabases(envId);
  const stacks = useListComposeStacks(envId);

  const statuses = useMemo(
    () => [
      ...(apps.data ?? []).map((a) => normalizeStatus(a.status)),
      ...(dbs.data ?? []).map((d) => normalizeStatus(d.status)),
      ...(stacks.data ?? []).map((s) => normalizeStatus(s.status)),
    ],
    [apps.data, dbs.data, stacks.data],
  );

  // Silence until every list has landed: a chip that guesses is worse than no
  // chip at all (handoff — skeletons never wear a status colour).
  if (apps.isPending || dbs.isPending || stacks.isPending) return null;

  const worst = statuses.reduce<Status>((acc, s) => (SEVERITY[s] > SEVERITY[acc] ? s : acc), "stopped");
  const errors = statuses.filter((s) => s === "error").length;
  const degraded = statuses.filter((s) => s === "degraded").length;
  const deploying = statuses.filter((s) => s === "deploying").length;
  const stopped = statuses.filter((s) => s === "stopped").length;

  const label = (() => {
    if (statuses.length === 0) return "empty";
    if (errors > 0) return `${errors} ${errors === 1 ? "error" : "errors"}`;
    if (degraded > 0) return `${degraded} degraded`;
    if (deploying > 0) return `${deploying} deploying`;
    if (stopped === statuses.length) return `${stopped} stopped`;
    return "all running";
  })();

  return <StatusPill status={statuses.length === 0 ? "unknown" : worst}>{label}</StatusPill>;
}

function EnvResources({
  projectId,
  projectName,
  envId,
  envName,
}: {
  projectId: string;
  projectName: string;
  envId: string;
  envName: string;
}) {
  const apps = useListApplications(envId);
  const dbs = useListDatabases(envId);
  const stacks = useListComposeStacks(envId);
  // A provisioning row says where it is landing; the create dialog holds the
  // same list, so this is the cache, not a second request.
  const servers = useListServers();
  const serverName = (id: string) => servers.data?.find((s) => s.id === id)?.name ?? "the server";
  const appCount = apps.data?.length;
  // Canvas 14g TAB ORDER: the board is one list to the keyboard — j/k and ↑↓
  // walk apps then databases as one sequence, Enter opens the card.
  const rowNav = useRowNavigation();

  return (
    <div ref={rowNav} className="space-y-8">
      <section className="space-y-3.5">
        <Eyebrow>
          Applications
          {appCount !== undefined && ` — ${appCount}`}
          {appCount ? " · click one" : ""}
        </Eyebrow>
        <PageState
          query={apps}
          empty={
            <EmptyState
              emphasis
              title="Deploy your first app"
              hint="Point CypherPanel at a git repository and it will build and run it, live at your domain."
              action={
                <NewAppDialog envId={envId} projectId={projectId} projectName={projectName} envName={envName} primary />
              }
            />
          }
        >
          {(list) => (
            // Always 2-up, capped at 760px (1b): the board is a column on the
            // surface, not a wall of tiles stretched to the shell's width.
            <ul className="grid max-w-[760px] gap-3.5 sm:grid-cols-2">
              {list.map((a) => (
                <AppRow key={a.id} projectId={projectId} app={a} />
              ))}
            </ul>
          )}
        </PageState>
      </section>

      <section className="space-y-3.5">
        <div className="flex items-center justify-between gap-3">
          <Eyebrow>Databases{dbs.data ? ` — ${dbs.data.length}` : ""}</Eyebrow>
          <NewDatabaseDialog envId={envId} projectId={projectId} projectName={projectName} envName={envName} />
        </div>
        <PageState
          query={dbs}
          empty={
            <EmptyState
              title="No databases in this environment"
              hint="A managed database (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey) is provisioned, credentialed, and backed up by CypherPanel."
              action={
                <NewDatabaseDialog
                  envId={envId}
                  projectId={projectId}
                  projectName={projectName}
                  envName={envName}
                  primary
                />
              }
            />
          }
        >
          {(list) => (
            <ul className="space-y-2.5">
              {list.map((d) => (
                <DbRow key={d.id} projectId={projectId} db={d} serverName={serverName(d.server_id)} />
              ))}
            </ul>
          )}
        </PageState>
      </section>

      {/* The third Resource (glossary): a compose file the agent converges to.
          It shares the database row's shape rather than the application card's,
          because a stack has no revision to headline and no build to watch —
          what it has is a name, a placement and a state. */}
      <section className="space-y-3.5">
        <div className="flex items-center justify-between gap-3">
          <Eyebrow>Compose stacks{stacks.data ? ` — ${stacks.data.length}` : ""}</Eyebrow>
          <NewComposeStackDialog envId={envId} projectId={projectId} projectName={projectName} envName={envName} />
        </div>
        <PageState
          query={stacks}
          empty={
            <EmptyState
              title="No compose stacks in this environment"
              hint="Already have a compose file? Paste it and CypherPanel runs it as written — the file is the desired state, and its history is the way back."
              action={
                <NewComposeStackDialog
                  envId={envId}
                  projectId={projectId}
                  projectName={projectName}
                  envName={envName}
                  primary
                />
              }
            />
          }
        >
          {(list) => (
            <ul className="space-y-2.5">
              {list.map((st) => (
                <ComposeRow key={st.id} projectId={projectId} stack={st} serverName={serverName(st.server_id)} />
              ))}
            </ul>
          )}
        </PageState>
      </section>
    </div>
  );
}

/** Short revision for display — the first 7 of the revision id's suffix. */
function shortRev(id: string | null | undefined): string | null {
  if (!id) return null;
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

/**
 * The card splits the status marker from the status word — the dot leads the
 * row, the word sits at its far end — so it cannot use StatusBadge, which sets
 * the pair together. These are StatusBadge's own colours: the word is the
 * card's main signal on the board (1b), not a caption.
 */
const STATE_TEXT: Record<Status, string> = {
  running: "text-status-running",
  deploying: "text-status-deploying",
  stopped: "text-status-stopped",
  error: "text-danger",
  degraded: "text-status-degraded-text",
  unknown: "text-status-unknown",
};

function AppRow({ projectId, app }: { projectId: string; app: Application }) {
  const status = normalizeStatus(app.status);
  const broken = status === "error";
  const rev = shortRev(app.observed_revision_id ?? app.desired_revision_id);

  return (
    <li data-row>
      <Link
        // The broken card promises "View logs →", so that is where it goes
        // (12f: fail → logs). A card that names one place and lands on
        // another is the dead end 15a is named against.
        to={
          broken
            ? "/projects/$projectId/applications/$appId/logs"
            : "/projects/$projectId/applications/$appId"
        }
        params={{ projectId, appId: app.id }}
        className={cn(
          "flex h-full flex-col rounded-lg border bg-bg p-4.5",
          "transition-[border-color,box-shadow] hover:border-border-strong hover:shadow-card",
          broken ? "border-[1.5px] border-status-error/50" : "border-border",
          // A stopped resource is still there, just not carrying anything —
          // the live cards should hold the board (1b).
          status === "stopped" && "opacity-65",
        )}
      >
        <span className="flex items-center gap-2.5">
          <StatusDot status={status} decorative />
          <span className="min-w-0 truncate text-base font-semibold">{app.name}</span>
          <span
            className={cn(
              "ml-auto shrink-0 font-mono text-[10.5px] font-medium uppercase tracking-wide",
              STATE_TEXT[status],
            )}
          >
            {status}
          </span>
        </span>

        <span className="mt-2 block truncate font-mono text-[11.5px] text-text-faint">
          {app.route.domain || "internal"}
          {rev && ` · rev ${rev}`}
        </span>

        {/* An error card says what broke and offers the remedy inline — a
            screen you can only stare at is a bug (ui-principles §11). */}
        {broken ? (
          <span className="mt-3.5 block rounded-md bg-status-error/[0.07] px-3 py-2.5 text-xs leading-relaxed text-danger">
            {app.status_detail ?? "The container exited unexpectedly."}
            <span className="mt-1 block font-semibold text-text">View logs →</span>
          </span>
        ) : (
          <span className="mt-3.5 block font-mono text-[11px] text-text-faint">
            created {relativeTime(app.created_at)}
          </span>
        )}
      </Link>
    </li>
  );
}

/**
 * The database row on the board. 10a's progress popup promises that "the card
 * on the project board shows progress" once it is closed, so a provisioning
 * row carries the same step list and bar the popup drew, and an error row
 * says what broke — the same shape the application card gives its error.
 */
function DbRow({ projectId, db, serverName }: { projectId: string; db: Database; serverName: string }) {
  const status = normalizeStatus(db.status);
  const broken = status === "error";
  const provisioning = db.status === "provisioning";
  const { steps, progress } = provisioningSteps(db.status, engineLabel(db.engine, db.version), serverName);

  return (
    <li data-row>
      <Link
        to="/projects/$projectId/databases/$dbId"
        params={{ projectId, dbId: db.id }}
        className={cn(
          "flex flex-col rounded-lg border bg-bg px-4 py-3.5",
          "transition-[border-color,box-shadow] hover:border-border-strong hover:shadow-card",
          broken ? "border-[1.5px] border-status-error/50" : "border-border",
          status === "stopped" && "opacity-65",
        )}
      >
        <span className="flex items-center gap-3.5">
          <StatusDot status={db.status} decorative />
          <DatabaseIcon className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
          <span className="min-w-0 truncate text-[15px] font-semibold">{db.name}</span>
          <span className="min-w-0 truncate font-mono text-[11.5px] text-text-faint">{engineLabel(db.engine, db.version)}</span>
          <span className="ml-auto flex shrink-0 items-center gap-3.5">
            <span className="hidden font-mono text-[11.5px] text-text-faint sm:inline">created {relativeTime(db.created_at)}</span>
            <span className={cn("font-mono text-[10.5px] font-medium uppercase tracking-wide", STATE_TEXT[status])}>
              {status}
            </span>
          </span>
        </span>

        {provisioning && (
          <ProvisioningSteps
            compact
            steps={steps}
            progress={progress}
            detail={db.status_detail}
            label={`Creating ${db.name}`}
            className="mt-2.5 pl-6"
          />
        )}

        {/* An error row says what broke and where the remedy is — a row you
            can only stare at is a bug (ui-principles §11). */}
        {broken && (
          <span className="mt-3 block rounded-md bg-status-error/[0.07] px-3 py-2.5 text-xs leading-relaxed text-danger">
            {db.status_detail ?? "The engine exited unexpectedly."}
            <span className="mt-1 block font-semibold text-text">Open {db.name} →</span>
          </span>
        )}
      </Link>
    </li>
  );
}

/**
 * The compose-stack row on the board, built on DbRow's shape for the reason
 * given at its section: a stack is a name, a placement and a state. Its one
 * extra fact is the service count the file asks for — `degraded` means some of
 * them and not others, and a row that says "degraded" without saying how many
 * is a row you have to click to understand.
 */
function ComposeRow({
  projectId,
  stack,
  serverName,
}: {
  projectId: string;
  stack: ComposeStack;
  serverName: string;
}) {
  const status = normalizeStatus(stack.status);
  const broken = status === "error";
  const undeployed = !stack.desired_revision_id;

  return (
    <li data-row>
      <Link
        to="/projects/$projectId/compose/$stackId"
        params={{ projectId, stackId: stack.id }}
        className={cn(
          "flex flex-col rounded-lg border bg-bg px-4 py-3.5",
          "transition-[border-color,box-shadow] hover:border-border-strong hover:shadow-card",
          broken ? "border-[1.5px] border-status-error/50" : "border-border",
          status === "stopped" && "opacity-65",
        )}
      >
        <span className="flex items-center gap-3.5">
          <StatusDot status={stack.status} decorative />
          <Boxes className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
          <span className="min-w-0 truncate text-[15px] font-semibold">{stack.name}</span>
          <span className="min-w-0 truncate font-mono text-[11.5px] text-text-faint">
            {stack.route.domain || "internal"} · {serverName}
          </span>
          <span className="ml-auto flex shrink-0 items-center gap-3.5">
            <span className="hidden font-mono text-[11.5px] text-text-faint sm:inline">
              created {relativeTime(stack.created_at)}
            </span>
            <span className={cn("font-mono text-[10.5px] font-medium uppercase tracking-wide", STATE_TEXT[status])}>
              {status}
            </span>
          </span>
        </span>

        {/* A stack that exists but has never been deployed is the one state
            the status word cannot express — "stopped" reads as something that
            was running and is not, and this never was. */}
        {undeployed && !broken && (
          <span className="mt-2.5 block pl-6 font-mono text-[11.5px] text-text-faint">
            never deployed — the file is stored, nothing is running
          </span>
        )}

        {broken && (
          <span className="mt-3 block rounded-md bg-status-error/[0.07] px-3 py-2.5 text-xs leading-relaxed text-danger">
            {stack.status_detail ?? "Compose could not bring the stack up."}
            <span className="mt-1 block font-semibold text-text">Open {stack.name} →</span>
          </span>
        )}
      </Link>
    </li>
  );
}
