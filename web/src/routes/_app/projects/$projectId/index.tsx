// Project home: environment switcher (tabs, not routes) + this environment's
// resources. Empty states chain the golden path (ui-principles §11).
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Database as DatabaseIcon, Plus } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { useListApplications, useCreateApplication } from "@/api/gen/applications/applications";
import { useListDatabases } from "@/api/gen/databases/databases";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type { Application, Database, Environment } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { AdvancedSection } from "@/components/advanced-section";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
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
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-base font-semibold text-text">{project.data?.project.name ?? "…"}</h1>
        {activeEnv && <NewAppDialog envId={activeEnv.id} />}
      </div>

      <PageState query={envs}>
        {(list) => (
          <div className="flex gap-0.5 overflow-x-auto border-b border-border" role="tablist" aria-label="Environments">
            {list.map((e) => (
              <Link
                key={e.id}
                to="/projects/$projectId"
                params={{ projectId }}
                search={{ env: e.id }}
                role="tab"
                aria-selected={activeEnv?.id === e.id}
                className={cn(
                  "mono -mb-px whitespace-nowrap border-b-2 border-transparent px-3 py-2 text-xs text-text-mid hover:text-text",
                  activeEnv?.id === e.id && "border-accent text-text",
                )}
              >
                {e.name}
              </Link>
            ))}
          </div>
        )}
      </PageState>

      {activeEnv && <EnvResources projectId={projectId} envId={activeEnv.id} />}
    </div>
  );
}

function EnvResources({ projectId, envId }: { projectId: string; envId: string }) {
  const apps = useListApplications(envId);
  const dbs = useListDatabases(envId);

  return (
    <div className="space-y-6">
      <section className="space-y-2">
        <Eyebrow>Applications</Eyebrow>
        <PageState
          query={apps}
          empty={
            <EmptyState
              title="No applications in this environment"
              hint="Deploy your first app: point CypherPanel at a git repository and it will build and run it, live at your domain."
              action={<NewAppDialog envId={envId} primary />}
            />
          }
        >
          {(list) => (
            <ul className="divide-y divide-border rounded-md border border-border bg-surface">
              {list.map((a) => (
                <AppRow key={a.id} projectId={projectId} app={a} />
              ))}
            </ul>
          )}
        </PageState>
      </section>

      <section className="space-y-2">
        <Eyebrow>Databases</Eyebrow>
        <PageState
          query={dbs}
          empty={
            <EmptyState
              title="No databases in this environment"
              hint="A managed database (PostgreSQL, MySQL, MongoDB, Redis …) is provisioned and backed up by CypherPanel. Create one from the API for now — the database screens arrive in the next slice."
            />
          }
        >
          {(list) => (
            <ul className="divide-y divide-border rounded-md border border-border bg-surface">
              {list.map((d) => (
                <DbRow key={d.id} db={d} />
              ))}
            </ul>
          )}
        </PageState>
      </section>
    </div>
  );
}

function AppRow({ projectId, app }: { projectId: string; app: Application }) {
  return (
    <li>
      <Link
        to="/projects/$projectId/applications/$appId"
        params={{ projectId, appId: app.id }}
        className="flex items-center justify-between gap-3 px-4 py-3 hover:bg-raised"
      >
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-sm font-medium text-text">{app.name}</span>
          {app.route.domain && <span className="mono truncate text-xs text-text-faint">{app.route.domain}</span>}
        </span>
        <StatusBadge status={app.status} className="shrink-0" />
      </Link>
    </li>
  );
}

function DbRow({ db }: { db: Database }) {
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="flex min-w-0 items-center gap-2.5">
        <DatabaseIcon className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-sm font-medium text-text">{db.name}</span>
          <span className="mono truncate text-xs text-text-faint">
            {db.engine} {db.version} · created {relativeTime(db.created_at)}
          </span>
        </span>
      </span>
      <StatusBadge status={db.status} className="shrink-0" />
    </li>
  );
}

// Simple by default, power underneath (ui-principles §6): the create form asks
// only what a first-timer must answer; every working default is visible.
function NewAppDialog({ envId, primary }: { envId: string; primary?: boolean }) {
  const navigate = useNavigate();
  const { projectId } = Route.useParams();
  const servers = useListServers();
  const [name, setName] = useState("");
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("main");
  const [domain, setDomain] = useState("");
  const [serverId, setServerId] = useState("");
  const [port, setPort] = useState("8080");
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
        source: { kind: "github", repo, branch },
        build: { kind: "dockerfile", dockerfile_path: dockerfile, context },
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
          <Button variant={primary ? "primary" : "secondary"} size="sm">
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
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          <Plus className="h-3.5 w-3.5" /> New application
        </Button>
      </DialogTrigger>
      <DialogContent title="Deploy an application" description="CypherPanel clones the repository, builds the Dockerfile, and keeps the app running at your domain.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name">
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />}
          </Field>
          <Field label="Repository" hint="A GitHub repository, owner/name or full URL. Private repos need a deploy key (Settings → Deploy keys).">
            {(id) => <Input id={id} required value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="acme/web" className="mono" />}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Branch">
              {(id) => <Input id={id} required value={branch} onChange={(e) => setBranch(e.target.value)} className="mono" />}
            </Field>
            <Field label="Port" hint="The port your app listens on.">
              {(id) => <Input id={id} required inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} className="mono" />}
            </Field>
          </div>
          <Field label="Domain" hint="Your app will be live at this domain (leave empty for a service without a public URL).">
            {(id) => <Input id={id} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" className="mono" />}
          </Field>
          {enrolled.length > 1 && (
            <Field label="Server">
              {(id) => (
                <select
                  id={id}
                  value={chosenServer}
                  onChange={(e) => setServerId(e.target.value)}
                  className="h-8 w-full rounded-md border border-border bg-surface px-2 text-sm text-text"
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
          <AdvancedSection>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Dockerfile path">
                {(id) => <Input id={id} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} className="mono" />}
              </Field>
              <Field label="Build context">
                {(id) => <Input id={id} value={context} onChange={(e) => setContext(e.target.value)} className="mono" />}
              </Field>
            </div>
          </AdvancedSection>
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
              {create.isPending ? "Creating…" : "Create application"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
