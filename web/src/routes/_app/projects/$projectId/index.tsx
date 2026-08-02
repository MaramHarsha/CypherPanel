// Project home: environment switcher (tabs, not routes) + this environment's
// resources. Empty states chain the golden path (ui-principles §11).
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Database as DatabaseIcon, Plus, Settings as SettingsIcon } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { useListApplications, useCreateApplication } from "@/api/gen/applications/applications";
import { useCreateDatabase, useListDatabases } from "@/api/gen/databases/databases";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type { Application, CreateDatabaseRequest, Database, Environment } from "@/api/gen/model";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot } from "@/components/status-badge";
import { AdvancedSection } from "@/components/advanced-section";
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

  useCrumbs([
    { label: "projects", to: "/projects" },
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
        <PageBody>{activeEnv && <EnvResources projectId={projectId} envId={activeEnv.id} />}</PageBody>
      </div>
    </>
  );
}

function EnvResources({ projectId, envId }: { projectId: string; envId: string }) {
  const apps = useListApplications(envId);
  const dbs = useListDatabases(envId);

  return (
    <div className="space-y-8">
      <section className="space-y-3.5">
        <Eyebrow>Applications{apps.data ? ` — ${apps.data.length}` : ""}</Eyebrow>
        <PageState
          query={apps}
          empty={
            <EmptyState
              emphasis
              title="Deploy your first app"
              hint="Point CypherPanel at a git repository and it will build and run it, live at your domain."
              action={<NewAppDialog envId={envId} primary />}
            />
          }
        >
          {(list) => (
            <ul className="grid gap-3.5 sm:grid-cols-2 lg:grid-cols-3">
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
          <NewDatabaseDialog envId={envId} />
        </div>
        <PageState
          query={dbs}
          empty={
            <EmptyState
              title="No databases in this environment"
              hint="A managed database (PostgreSQL, MySQL, MariaDB, MongoDB, Redis, Valkey) is provisioned, credentialed, and backed up by CypherPanel."
              action={<NewDatabaseDialog envId={envId} primary />}
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
          "flex h-full flex-col rounded-lg border bg-bg p-4.5 transition-colors hover:border-border-strong",
          broken ? "border-[1.5px] border-status-error/50" : "border-border",
        )}
      >
        <span className="flex items-center gap-2.5">
          <StatusDot status={status} />
          <span className="min-w-0 truncate text-base font-semibold">{app.name}</span>
          <span
            className={cn(
              "ml-auto shrink-0 font-mono text-[10.5px] font-medium uppercase tracking-wide",
              broken ? "text-danger" : "text-text-faint",
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

const ENGINE_VERSIONS: Record<string, string[]> = {
  postgresql: ["17", "16", "15"],
  mysql: ["8.4", "8.0"],
  mariadb: ["11", "10.11"],
  mongodb: ["7", "6"],
  redis: ["7"],
  valkey: ["8", "7"],
};

// Simple by default (ui-principles §6): pick an engine, name it, done — server
// and version have working defaults; limits fold into Advanced.
function NewDatabaseDialog({ envId, primary }: { envId: string; primary?: boolean }) {
  const navigate = useNavigate();
  const { projectId } = Route.useParams();
  const servers = useListServers();
  const [name, setName] = useState("");
  const [engine, setEngine] = useState<keyof typeof ENGINE_VERSIONS>("postgresql");
  const [version, setVersion] = useState(ENGINE_VERSIONS.postgresql![0]!);
  const [serverId, setServerId] = useState("");
  const [error, setError] = useState<string | null>(null);

  const enrolled = (servers.data ?? []).filter((s) => s.enrolled);
  const chosenServer = serverId || enrolled[0]?.id || "";

  // The root password exists in plaintext exactly once, in the create
  // response. Navigating straight to the database threw it away, and nothing
  // can recover it afterwards — the stored copy is sealed. So the dialog stays
  // open and hands it over first (ui-principles §6: every generated value gets
  // a copy button).
  const [created, setCreated] = useState<{ id: string; password: string } | null>(null);

  const create = useCreateDatabase({
    mutation: {
      onSuccess: (res) => setCreated({ id: res.database.id, password: res.root_password }),
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
      data: { name, engine: engine as CreateDatabaseRequest["engine"], version, server_id: chosenServer },
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
        <DialogContent
          title="Copy the root password now"
          description="This is the only time it is shown — the stored copy is sealed and cannot be read back."
        >
          <CopyField value={created.password} />
          <div className="mt-5 flex justify-end">
            <Button
              variant="primary"
              onClick={() =>
                void navigate({
                  to: "/projects/$projectId/databases/$dbId",
                  params: { projectId, dbId: created.id },
                })
              }
            >
              I've saved it — open the database
            </Button>
          </div>
        </DialogContent>
      ) : (
      <DialogContent title="Create a database" description="CypherPanel provisions the engine, generates credentials, and can back it up on a schedule.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name">
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="primary" />}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Engine">
              {(id) => (
                <select
                  id={id}
                  value={engine}
                  onChange={(e) => {
                    const next = e.target.value as keyof typeof ENGINE_VERSIONS;
                    setEngine(next);
                    setVersion(ENGINE_VERSIONS[next]![0]!);
                  }}
                  className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text"
                >
                  {Object.keys(ENGINE_VERSIONS).map((e) => (
                    <option key={e} value={e}>
                      {e}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="Version">
              {(id) => (
                <select
                  id={id}
                  value={version}
                  onChange={(e) => setVersion(e.target.value)}
                  className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text"
                >
                  {ENGINE_VERSIONS[engine]!.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>
          {enrolled.length > 1 && (
            <Field label="Server">
              {(id) => (
                <select
                  id={id}
                  value={chosenServer}
                  onChange={(e) => setServerId(e.target.value)}
                  className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text"
                >
                  {enrolled.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          )}
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create database"}
            </Button>
          </div>
        </form>
      </DialogContent>
      )}
    </Dialog>
  );
}

// Simple by default, power underneath (ui-principles §6): the create form asks
// only what a first-timer must answer; every working default is visible.
function NewAppDialog({ envId, primary, eyebrow }: { envId: string; primary?: boolean; eyebrow?: string }) {
  const navigate = useNavigate();
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

  const enrolled = (servers.data ?? []).filter((s) => s.enrolled);
  const chosenServer = serverId || enrolled[0]?.id || "";

  const create = useCreateApplication({
    mutation: {
      onSuccess: (res) =>
        void navigate({
          to: "/projects/$projectId/applications/$appId",
          params: { projectId, appId: res.application.id },
        }),
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
