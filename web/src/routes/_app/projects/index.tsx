// Projects landing — the no-dashboard bet (web-ui-design.md §3): each row
// carries an aggregated status rollup, worst-first, red visible from across
// the room.
import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListApplicationsQueryOptions } from "@/api/gen/applications/applications";
import { getListDatabasesQueryOptions } from "@/api/gen/databases/databases";
import {
  useCreateProject,
  useListEnvironments,
  useListProjects,
} from "@/api/gen/projects/projects";
import type { Project } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, type Status } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { useTeamScope } from "@/lib/team";

export const Route = createFileRoute("/_app/projects/")({ component: ProjectsPage });

/** Severity for worst-first sorting: loud problems float to the top. */
const SEVERITY: Record<Status, number> = {
  error: 5,
  degraded: 4,
  deploying: 3,
  unknown: 2,
  running: 1,
  stopped: 0,
};

function ProjectsPage() {
  useCrumbs([{ label: "projects" }]);
  const projects = useListProjects();
  const { teamId } = useTeamScope();
  const [ranks, setRanks] = useState<Record<string, number>>({});

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <Eyebrow>Projects</Eyebrow>
        <CreateProjectDialog />
      </div>
      <PageState
        query={projects}
        isEmpty={(list) => list.filter((p) => !teamId || p.team_id === teamId).length === 0}
        empty={
          <EmptyState
            title="No projects yet"
            hint="A project groups the environments and apps for one product. Create one, then deploy your first app into it."
            action={<CreateProjectDialog primary />}
          />
        }
      >
        {(list) => {
          const scoped = list.filter((p) => !teamId || p.team_id === teamId);
          const sorted = [...scoped].sort((a, b) => (ranks[b.id] ?? 0) - (ranks[a.id] ?? 0));
          return (
            <ul className="divide-y divide-border rounded-md border border-border bg-surface">
              {sorted.map((p) => (
                <ProjectRow key={p.id} project={p} onRank={(r) => setRanks((m) => (m[p.id] === r ? m : { ...m, [p.id]: r }))} />
              ))}
            </ul>
          );
        }}
      </PageState>
    </div>
  );
}

function ProjectRow({ project, onRank }: { project: Project; onRank: (rank: number) => void }) {
  const envs = useListEnvironments(project.id);
  const envIds = useMemo(() => (envs.data ?? []).map((e) => e.id), [envs.data]);

  const appQueries = useQueries({
    queries: envIds.map((id) => getListApplicationsQueryOptions(id)),
  });
  const dbQueries = useQueries({
    queries: envIds.map((id) => getListDatabasesQueryOptions(id)),
  });

  const { apps, dbs } = useMemo(() => {
    return {
      apps: appQueries.flatMap((q) => q.data ?? []),
      dbs: dbQueries.flatMap((q) => q.data ?? []),
    };
  }, [appQueries, dbQueries]);

  const statuses = useMemo(
    () => [...apps.map((a) => normalizeStatus(a.status)), ...dbs.map((d) => normalizeStatus(d.status))],
    [apps, dbs],
  );
  const worst = statuses.reduce<Status>((acc, s) => (SEVERITY[s] > SEVERITY[acc] ? s : acc), "stopped");
  const problems = statuses.filter((s) => s === "error").length;
  const degraded = statuses.filter((s) => s === "degraded").length;
  const deploying = statuses.filter((s) => s === "deploying").length;

  const rank = statuses.length === 0 ? 0 : SEVERITY[worst];
  useEffect(() => onRank(rank), [rank, onRank]);

  return (
    <li>
      <Link
        to="/projects/$projectId"
        params={{ projectId: project.id }}
        className="flex items-center justify-between gap-3 px-4 py-3 hover:bg-raised"
      >
        <span className="flex min-w-0 items-center gap-2.5">
          {statuses.length > 0 && <StatusDot status={worst} />}
          <span className="truncate text-sm font-medium text-text">{project.name}</span>
        </span>
        <span className="mono shrink-0 text-xs text-text-faint">
          {problems > 0 && <span className="text-status-error">{problems} error{problems > 1 ? "s" : ""} · </span>}
          {degraded > 0 && <span className="text-status-degraded">{degraded} degraded · </span>}
          {deploying > 0 && <span className="text-status-deploying">{deploying} deploying · </span>}
          {apps.length} app{apps.length === 1 ? "" : "s"}
          {dbs.length > 0 && ` · ${dbs.length} db${dbs.length === 1 ? "" : "s"}`}
        </span>
      </Link>
    </li>
  );
}

function CreateProjectDialog({ primary }: { primary?: boolean }) {
  const navigate = useNavigate();
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = useCreateProject({
    mutation: {
      onSuccess: (p) => void navigate({ to: "/projects/$projectId", params: { projectId: p.id } }),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the project"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const team = teamId ?? me.data?.teams[0]?.id;
    create.mutate({ data: { name, ...(team ? { team_id: team } : {}) } });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          <Plus className="h-3.5 w-3.5" /> New project
        </Button>
      </DialogTrigger>
      <DialogContent title="Create a project" description="A project groups environments (production, staging, previews) for one product.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="acme" />}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === ""}>
              {create.isPending ? "Creating…" : "Create project"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
