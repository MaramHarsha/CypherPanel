// Project home: environment switcher (tabs, not routes) + this environment's
// resources. Empty states chain the golden path (ui-principles §11).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Database as DatabaseIcon, Plus, Settings as SettingsIcon, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import {
  getListApplicationsQueryKey,
  useListApplications,
  useCreateApplication,
} from "@/api/gen/applications/applications";
import { useGetMe } from "@/api/gen/auth/auth";
import {
  getListDatabasesQueryKey,
  useCreateDatabase,
  useGetDatabase,
  useListDatabases,
} from "@/api/gen/databases/databases";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type { Application, Database, Environment } from "@/api/gen/model";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill, type Status } from "@/components/status-badge";
import { AdvancedSection } from "@/components/advanced-section";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
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
            {activeEnv && <NewAppDialog envId={activeEnv.id} eyebrow={`${project.data?.project.name ?? ""} / ${activeEnv.name} / new application`} />}
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

  const statuses = useMemo(
    () => [
      ...(apps.data ?? []).map((a) => normalizeStatus(a.status)),
      ...(dbs.data ?? []).map((d) => normalizeStatus(d.status)),
    ],
    [apps.data, dbs.data],
  );

  // Silence until both lists have landed: a chip that guesses is worse than no
  // chip at all (handoff — skeletons never wear a status colour).
  if (apps.isPending || dbs.isPending) return null;

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
  const appCount = apps.data?.length;

  return (
    <div className="space-y-8">
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
                <NewAppDialog
                  envId={envId}
                  primary
                  eyebrow={`${projectName} / ${envName} / new application`}
                />
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
          <NewDatabaseDialog envId={envId} projectName={projectName} envName={envName} />
        </div>
        <PageState
          query={dbs}
          empty={
            <EmptyState
              title="No databases in this environment"
              hint="A managed database (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey) is provisioned, credentialed, and backed up by CypherPanel."
              action={
                <NewDatabaseDialog envId={envId} projectName={projectName} envName={envName} primary />
              }
            />
          }
        >
          {(list) => (
            <ul className="space-y-2.5">
              {list.map((d) => (
                <DbRow key={d.id} projectId={projectId} db={d} />
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
    <li>
      <Link
        to="/projects/$projectId/applications/$appId"
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
          <StatusDot status={status} />
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

function DbRow({ projectId, db }: { projectId: string; db: Database }) {
  return (
    <li>
      <Link
        to="/projects/$projectId/databases/$dbId"
        params={{ projectId, dbId: db.id }}
        className="flex items-center gap-3.5 rounded-lg border border-border bg-bg px-4 py-3.5 transition-colors hover:border-border-strong"
      >
        <StatusDot status={db.status} />
        <DatabaseIcon className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
        <span className="min-w-0 truncate text-[15px] font-semibold">{db.name}</span>
        <span className="min-w-0 truncate font-mono text-[11.5px] text-text-faint">
          {db.engine} {db.version}
        </span>
        <span className="ml-auto shrink-0 font-mono text-[11.5px] text-text-faint">
          created {relativeTime(db.created_at)}
        </span>
      </Link>
    </li>
  );
}

/**
 * The engine tiles of canvas 9b: the engine is a card you click, with its
 * version as a small mono line inside the chosen one — not a name hidden in a
 * menu. `port` is what "expose externally" publishes on the host; using the
 * engine's conventional port keeps that control a yes/no instead of a number
 * the operator has to invent.
 */
const ENGINES = {
  postgresql: { label: "Postgres", versions: ["17", "16", "15"], port: 5432 },
  mysql: { label: "MySQL", versions: ["8.4", "8.0"], port: 3306 },
  mariadb: { label: "MariaDB", versions: ["11", "10.11"], port: 3306 },
  mongodb: { label: "Mongo", versions: ["7", "6"], port: 27017 },
  redis: { label: "Redis", versions: ["7"], port: 6379 },
  valkey: { label: "Valkey", versions: ["8", "7"], port: 6379 },
} as const;

type EngineKey = keyof typeof ENGINES;
const ENGINE_KEYS = Object.keys(ENGINES) as EngineKey[];

/** The 38×22 pill switch of 9b — the one toggle the create forms draw. */
function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className={cn(
        "relative h-[22px] w-[38px] shrink-0 rounded-full transition-colors",
        checked ? "bg-primary" : "bg-border-input",
      )}
    >
      <span
        className={cn(
          "absolute top-[2px] block h-[18px] w-[18px] rounded-full bg-surface transition-[left]",
          checked ? "left-[18px]" : "left-[2px]",
        )}
      />
    </button>
  );
}

// Simple by default (ui-principles §6): pick an engine, name it, done — server
// and version have working defaults; limits fold into Advanced.
function NewDatabaseDialog({
  envId,
  projectName,
  envName,
  primary,
}: {
  envId: string;
  projectName: string;
  envName: string;
  primary?: boolean;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { projectId } = Route.useParams();
  const servers = useListServers();
  const [name, setName] = useState("");
  const [engine, setEngine] = useState<EngineKey>("postgresql");
  const [version, setVersion] = useState<string>(ENGINES.postgresql.versions[0]);
  const [serverId, setServerId] = useState("");
  const [initialDatabase, setInitialDatabase] = useState("");
  const [expose, setExpose] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Redis and Valkey number their databases rather than naming them, so there
  // is nothing to ask for.
  const namesDatabases = engine !== "redis" && engine !== "valkey";

  // A builder-only agent is built without a workload driver and rejects rollout
  // work, so offering it would create the resource and then fail every deploy.
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServer = serverId || enrolled[0]?.id || "";
  const serverName = enrolled.find((s) => s.id === chosenServer)?.name ?? "the server";

  // The root password exists in plaintext exactly once, in the create
  // response. Navigating straight to the database threw it away, and nothing
  // can recover it afterwards — the stored copy is sealed. So the dialog stays
  // open and hands it over first (ui-principles §6: every generated value gets
  // a copy button).
  const [created, setCreated] = useState<{ id: string; name: string; password: string } | null>(null);

  const create = useCreateDatabase({
    mutation: {
      onSuccess: (res) => {
        // The board behind this popup is drawn from the cached list, so a
        // database created here is invisible there until the list is asked for
        // again — including the rollup chip, which counts the same two lists.
        void qc.invalidateQueries({ queryKey: getListDatabasesQueryKey(envId) });
        setCreated({ id: res.database.id, name: res.database.name, password: res.root_password });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the database"),
    },
  });

  if (!servers.isPending && enrolled.length === 0) {
    return (
      <Button variant="primary" size="sm" onClick={() => void navigate({ to: "/servers" })}>
        <Plus className="h-3.5 w-3.5" /> New database
      </Button>
    );
  }

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: envId,
      data: {
        name,
        engine,
        version,
        server_id: chosenServer,
        ...(expose ? { expose_port: ENGINES[engine].port } : {}),
        ...(namesDatabases && initialDatabase.trim() !== ""
          ? { initial_database: initialDatabase.trim() }
          : {}),
      },
    });
  };

  return (
    <Dialog onOpenChange={(open) => !open && setCreated(null)}>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New database
        </Button>
      </DialogTrigger>
      {created ? (
        <CreatePanel
          created={created}
          projectId={projectId}
          engineLabel={`${ENGINES[engine].label} ${version}`}
          serverName={serverName}
        />
      ) : (
        <DialogContent
          size="form"
          eyebrow={`${projectName} / ${envName} / new database`}
          title="Create a database"
          description="CypherPanel provisions the engine, generates credentials, and can back it up on a schedule."
        >
          <form onSubmit={submit} className="space-y-4">
            {/* 10a locks the form while the create is in flight: a second
                submit would provision a second database. */}
            <fieldset disabled={create.isPending} className="min-w-0 space-y-4">
              {/* Six engines, and the widest labels ("Postgres", "MariaDB")
                  are single unbreakable words: at 360px four columns leave a
                  50px text box and the words spill into their neighbours. The
                  canvas draws this grid at the 560px dialog width only. */}
              <div className="grid grid-cols-2 gap-2 min-[420px]:grid-cols-3 sm:grid-cols-4">
                {ENGINE_KEYS.map((key) => {
                  const spec = ENGINES[key];
                  if (key !== engine) {
                    return (
                      <button
                        key={key}
                        type="button"
                        onClick={() => {
                          setEngine(key);
                          setVersion(spec.versions[0]);
                        }}
                        className="rounded-lg border border-border bg-surface px-1.5 py-3 text-center text-[12.5px] font-semibold text-text-mid transition-colors hover:border-border-strong"
                      >
                        {spec.label}
                        <span className="mt-[3px] block font-mono text-[10.5px] font-normal text-text-disabled">
                          {spec.versions[0]}
                        </span>
                      </button>
                    );
                  }
                  return (
                    <div
                      key={key}
                      className="rounded-lg border-[1.5px] border-border-strong bg-surface px-1.5 py-3 text-center text-[12.5px] font-semibold text-text"
                    >
                      {spec.label}
                      {/* The version is a menu on the engine you chose, not a
                          second question about an engine you might not want. */}
                      <Select
                        aria-label="Version"
                        value={version}
                        onChange={(e) => setVersion(e.target.value)}
                        className="mx-auto mt-[3px] h-auto w-auto border-0 bg-transparent p-0 text-center text-[10.5px] font-normal text-text-faint"
                      >
                        {spec.versions.map((v) => (
                          <option key={v} value={v}>
                            {v}
                          </option>
                        ))}
                      </Select>
                    </div>
                  );
                })}
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Name">
                  {(id) => (
                    <Input
                      id={id}
                      required
                      autoFocus
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="primary"
                    />
                  )}
                </Field>
                {/* Placement is always stated (9b) — a one-server fleet is
                    still an answer to "where does this land". */}
                <Field label="Server">
                  {(id) => (
                    <Select
                      id={id}
                      value={chosenServer}
                      onChange={(e) => setServerId(e.target.value)}
                      disabled={enrolled.length < 2}
                    >
                      {enrolled.map((s) => (
                        <option key={s.id} value={s.id}>
                          {s.name}
                        </option>
                      ))}
                    </Select>
                  )}
                </Field>
              </div>

              {namesDatabases && (
                <Field
                  label="Application database"
                  hint="Optional — a database created inside the engine for your app to use. Cannot be changed later."
                >
                  {(id) => (
                    <Input
                      id={id}
                      value={initialDatabase}
                      onChange={(e) => setInitialDatabase(e.target.value)}
                      placeholder={engine === "postgresql" ? "postgres" : "appdb"}
                      pattern="[A-Za-z_][A-Za-z0-9_]*"
                      maxLength={63}
                      title="Letters, digits and underscores; must not start with a digit."
                    />
                  )}
                </Field>
              )}

              {/* 9b asks this as a yes/no, and the sub-line names the
                  consequence rather than the port number — until it is on, at
                  which point the port is exactly what the operator needs. */}
              <div className="flex items-center gap-3 rounded-lg border border-border bg-surface px-3.5 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-semibold text-text">Expose externally</p>
                  <p className="mt-0.5 text-xs leading-relaxed text-text-mid">
                    {expose
                      ? `Publishes port ${ENGINES[engine].port} on ${serverName} — anything that can reach the server can reach the database.`
                      : "Off = reachable only from apps in this project (recommended)."}
                  </p>
                </div>
                <Toggle checked={expose} onChange={setExpose} label="Expose externally" />
              </div>
            </fieldset>

            {error && (
              <p role="alert" className="text-[13px] text-danger">
                {error}
              </p>
            )}
            <div className="flex flex-wrap items-center gap-2.5 pt-1">
              <ActionButton
                type="submit"
                variant="accent"
                size="lg"
                state={create.isPending ? "busy" : create.isError ? "failed" : "idle"}
                busyLabel="Creating…"
              >
                Create database →
              </ActionButton>
              <DialogClose asChild>
                <Button variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <span className="text-xs text-text-faint">
                password generated and shown once, like tokens
              </span>
            </div>
          </form>
        </DialogContent>
      )}
    </Dialog>
  );
}

/** The step glyphs of 10a: shape as well as colour, so the state of a step
 *  survives a colour-blind reader. Same vocabulary as the blocking popup. */
const STEP_MARK = {
  done: { glyph: "✓", className: "text-status-running" },
  active: { glyph: "▸", className: "text-status-deploying" },
  pending: { glyph: "○", className: "text-text-disabled" },
  failed: { glyph: "✕", className: "text-danger" },
} as const;

/** 10a prints the create's own elapsed time — "8.4s" — beside the result. */
function elapsedLabel(ms: number): string {
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${String(Math.round(s % 60)).padStart(2, "0")}s`;
}

/**
 * Canvas 10a, panels 2 and 3 — what the click actually looks like.
 *
 * The steps are the control plane's own reading of the record: the create was
 * accepted, the agent is provisioning, the engine is serving. Finer steps
 * (image pull, volume, DNS) would be a story told over status fields the API
 * does not report, and a story is worse than a short list that is true.
 *
 * The secret is handed over in both panels rather than only in the last one:
 * 10a promises the popup is safe to close, and that promise is only honest if
 * closing it early does not take the one plaintext copy with it.
 */
function CreatePanel({
  created,
  projectId,
  engineLabel,
  serverName,
}: {
  created: { id: string; name: string; password: string };
  projectId: string;
  engineLabel: string;
  serverName: string;
}) {
  const navigate = useNavigate();
  const db = useGetDatabase(created.id, {
    query: {
      refetchInterval: (q) => {
        const status = q.state.data?.status;
        return status === "running" || status === "error" ? false : 2_000;
      },
    },
  });

  const status = db.data?.status ?? "provisioning";
  const ready = status === "running";
  const failed = status === "error";

  // Frozen the moment it came up: a clock still ticking behind a finished
  // result reads as work still happening.
  const startedAt = useRef(Date.now());
  const [tookMs, setTookMs] = useState<number | null>(null);
  useEffect(() => {
    if (ready) setTookMs((t) => t ?? Date.now() - startedAt.current);
  }, [ready]);

  const openDatabase = () =>
    void navigate({
      to: "/projects/$projectId/databases/$dbId",
      params: { projectId, dbId: created.id },
    });

  const secret = (
    <>
      <p className="mt-2 text-xs leading-relaxed text-text-mid">
        Copy the password now — it's sealed after this popup.
      </p>
      {/* The one field in the product set on ink: 10a puts the secret on the
          pane so it reads as something handed over, not something stored. */}
      <CopyField
        value={created.password}
        className="mt-2.5 border-transparent bg-toast px-3.5 py-2.5 text-toast-text [&_button:hover]:bg-pane-border [&_button:hover]:text-toast-text"
      />
    </>
  );

  if (ready) {
    return (
      <DialogContent
        title={`${created.name} is running`}
        hideTitle
        className="border-[1.5px] border-border-strong bg-surface [&>div:first-child]:p-0"
      >
        <div className="pt-6">
          <div className="flex items-center gap-2">
            <StatusDot status="running" />
            <span className="text-[14px] font-bold text-text">{created.name} is running</span>
            {tookMs !== null && (
              <span className="ml-auto font-mono text-[10.5px] text-text-faint">
                {elapsedLabel(tookMs)}
              </span>
            )}
          </div>
          {secret}
          <div className="mt-3.5 flex flex-wrap gap-2">
            <Button variant="primary" size="sm" onClick={openDatabase}>
              Open {created.name}
            </Button>
            <DialogClose asChild>
              <Button variant="secondary" size="sm">
                Done
              </Button>
            </DialogClose>
          </div>
        </div>
      </DialogContent>
    );
  }

  const steps = [
    { label: `accepted · ${engineLabel}`, state: "done" as const },
    {
      label: `provisioning on ${serverName}`,
      state: failed ? ("failed" as const) : ("active" as const),
    },
    { label: "accepting connections", state: "pending" as const },
  ];
  // Half credit for the step in flight — the bar tracks the list above it and
  // nothing else, so it stops where the work stops.
  const progress = failed ? 1 / 3 : 1.5 / 3;

  return (
    <DialogContent
      title={`Creating ${created.name}…`}
      hideTitle
      className="border border-border bg-surface [&>div:first-child]:p-0"
    >
      <div className="pt-6">
        <div className="flex items-baseline gap-3">
          <span className="text-[14px] font-bold text-text">
            {failed ? `${created.name} could not start` : `Creating ${created.name}…`}
          </span>
          {/* The footnote promises this popup can be closed, so there has to be
              something to click that closes it. */}
          <DialogClose
            aria-label="Close"
            className="ml-auto shrink-0 rounded p-0.5 text-text-faint hover:text-text"
          >
            <X className="h-4 w-4" />
          </DialogClose>
        </div>

        <ol className="mt-2.5 font-mono text-[11.5px] leading-[2.1] text-text-dim">
          {steps.map((s) => (
            <li key={s.label} className={cn(s.state === "pending" && "text-text-disabled")}>
              <span className={STEP_MARK[s.state].className} aria-hidden>
                {STEP_MARK[s.state].glyph}
              </span>{" "}
              {s.label}
            </li>
          ))}
        </ol>

        {/* The agent's own words when it has any — never our paraphrase. */}
        {db.data?.status_detail && (
          <p className={cn("mt-1.5 text-xs leading-relaxed", failed ? "text-danger" : "text-text-faint")}>
            {db.data.status_detail}
          </p>
        )}

        <div
          className="mt-2.5 h-[5px] overflow-hidden rounded-full bg-border-subtle"
          role="progressbar"
          aria-valuenow={Math.round(progress * 100)}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className={cn(
              "h-full rounded-full transition-[width] duration-500",
              failed ? "bg-status-error" : "bg-primary",
            )}
            style={{ width: `${progress * 100}%` }}
          />
        </div>

        {secret}

        {failed ? (
          <div className="mt-3.5 flex flex-wrap items-center gap-2.5">
            <Button variant="primary" size="sm" onClick={openDatabase}>
              Open {created.name}
            </Button>
            <span className="text-[11px] text-text-faint">
              the record is on the project board — retry or remove it from there
            </span>
          </div>
        ) : (
          <p className="mt-2.5 text-[11px] leading-relaxed text-text-faint">
            safe to close — creation continues; the card on the project board shows progress
          </p>
        )}
      </div>
    </DialogContent>
  );
}

// Simple by default, power underneath (ui-principles §6): the create form asks
// only what a first-timer must answer; every working default is visible.
function NewAppDialog({ envId, primary, eyebrow }: { envId: string; primary?: boolean; eyebrow?: string }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { projectId } = Route.useParams();
  const servers = useListServers();
  const [name, setName] = useState("");
  const [sourceKind, setSourceKind] = useState<"github" | "image">("github");
  const [repo, setRepo] = useState("");
  const [image, setImage] = useState("");
  const [branch, setBranch] = useState("main");
  const [domain, setDomain] = useState("");
  const [serverId, setServerId] = useState("");
  const [port, setPort] = useState("8080");
  const [buildKind, setBuildKind] = useState<"auto" | "dockerfile" | "static">("auto");
  const [dockerfile, setDockerfile] = useState("./Dockerfile");
  const [context, setContext] = useState(".");
  const [error, setError] = useState<string | null>(null);

  // A builder-only agent is built without a workload driver and rejects rollout
  // work, so offering it would create the resource and then fail every deploy.
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServer = serverId || enrolled[0]?.id || "";

  const create = useCreateApplication({
    mutation: {
      onSuccess: (res) => {
        // Coming back from the application will render the board from cache,
        // so the list is refreshed on the way out rather than leaving the
        // operator to wonder where their new application went.
        void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(envId) });
        void navigate({
          to: "/projects/$projectId/applications/$appId",
          params: { projectId, appId: res.application.id },
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the application"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: envId,
      data: {
        name,
        source: sourceKind === "image" ? { kind: "image", image } : { kind: "github", repo, branch },
        build: { kind: buildKind, dockerfile_path: dockerfile, context },
        runtime: { server_id: chosenServer, port: Number(port), replicas: 1 },
        route: { domain: domain || undefined, https: true, path_prefix: "" },
      },
    });
  };

  if (!servers.isPending && enrolled.length === 0) {
    // Golden path: no servers yet → the next step is joining one, in context.
    return (
      <Dialog>
        <DialogTrigger asChild>
          <Button variant="primary" size={primary ? "lg" : "md"}>
            <Plus className="h-3.5 w-3.5" /> New application
          </Button>
        </DialogTrigger>
        <DialogContent
          title="Join a server first"
          description="An application runs on one of your servers, and none has joined yet. Joining takes one copy-paste command and about a minute."
        >
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => void navigate({ to: "/servers" })}>
              Go to Servers
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New application
        </Button>
      </DialogTrigger>
      <DialogContent
        size="form"
        eyebrow={eyebrow}
        title="Deploy an application"
        description="CypherPanel builds it (or pulls the image) and keeps it running at your domain."
      >
        <form onSubmit={submit} className="space-y-4">
          {/* Source first: it is the one thing only the operator knows.
              Everything with a working default folds into Advanced below
              (ui-principles §6, design 5w). */}
          <Field label="Deploy from">
            {(id) => (
              <Select id={id} value={sourceKind} onChange={(e) => setSourceKind(e.target.value as typeof sourceKind)}>
                <option value="github">Git repository — built from source</option>
                <option value="image">Container image — prebuilt, no build step</option>
              </Select>
            )}
          </Field>
          {sourceKind === "github" ? (
            <Field label="Repository" hint="Public, or private with a deploy key (Settings → Deploy keys).">
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={repo}
                  onChange={(e) => setRepo(e.target.value)}
                  placeholder="github.com/acme/web"
                />
              )}
            </Field>
          ) : (
            <Field label="Image" hint="Any public registry reference; the server pulls it directly.">
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={image}
                  onChange={(e) => setImage(e.target.value)}
                  placeholder="ghcr.io/acme/web:1.2"
                />
              )}
            </Field>
          )}

          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Name">
              {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />}
            </Field>
            {enrolled.length > 1 ? (
              <Field label="Server">
                {(id) => (
                  <Select id={id} value={chosenServer} onChange={(e) => setServerId(e.target.value)}>
                    {enrolled.map((sv) => (
                      <option key={sv.id} value={sv.id}>
                        {sv.name}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            ) : (
              <Field label="Server">
                {(id) => <Input id={id} value={enrolled[0]?.name ?? ""} readOnly tabIndex={-1} className="opacity-60" />}
              </Field>
            )}
          </div>

          <Field label="Domain" hint="TLS via Let's Encrypt, automatic. Leave empty for internal-only.">
            {(id) => (
              <Input id={id} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
            )}
          </Field>

          <AdvancedSection note="defaults work">
            {sourceKind === "github" && (
              <Field label="How to build it" hint="Detect picks a Dockerfile if there is one, otherwise serves the repo as a static site.">
                {(id) => (
                  <Select id={id} value={buildKind} onChange={(e) => setBuildKind(e.target.value as typeof buildKind)}>
                    <option value="auto">Detect automatically</option>
                    <option value="dockerfile">Dockerfile</option>
                    <option value="static">Static site (HTML, CSS, JS)</option>
                  </Select>
                )}
              </Field>
            )}
            <div className="grid gap-3 sm:grid-cols-2">
              {sourceKind === "github" && (
                <Field label="Branch">
                  {(id) => <Input id={id} required value={branch} onChange={(e) => setBranch(e.target.value)} />}
                </Field>
              )}
              <Field label="Port" hint="The port your app listens on.">
                {(id) => <Input id={id} required inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} />}
              </Field>
            </div>
            {sourceKind === "github" && (
              <div className="grid gap-3 sm:grid-cols-2">
                {buildKind !== "static" && (
                  <Field label="Dockerfile path">
                    {(id) => <Input id={id} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />}
                  </Field>
                )}
                <Field label="Build context" hint="The directory to build from.">
                  {(id) => <Input id={id} value={context} onChange={(e) => setContext(e.target.value)} />}
                </Field>
              </div>
            )}
          </AdvancedSection>

          {error && (
            <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2 pt-1">
            <Button
              type="submit"
              variant="accent"
              size="lg"
              disabled={create.isPending || (sourceKind === "github" ? repo.trim() === "" : image.trim() === "")}
            >
              {create.isPending ? "Deploying…" : "Deploy →"}
            </Button>
            <DialogClose asChild>
              <Button variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
