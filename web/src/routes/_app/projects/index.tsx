// Projects landing — the no-dashboard bet (web-ui-design.md §3): each row
// carries an aggregated status rollup, worst-first, red visible from across
// the room. Mission Control renders it as a broadsheet table: an ink rule
// above the first row, hairlines between the rest, and a tint bleeding off the
// left edge of anything broken.
import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListApplicationsQueryOptions } from "@/api/gen/applications/applications";
import { getListDatabasesQueryOptions } from "@/api/gen/databases/databases";
import { useCreateProject, useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import type { Project } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { HeaderStat, PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill, type Status } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { useTeamScope } from "@/lib/team";
import { cn } from "@/lib/utils";

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

/** What a project row reports upward so the page can sort and total. */
interface Rollup {
  rank: number;
  apps: number;
  dbs: number;
  errors: number;
}

// Below `sm` the table collapses to a stacked card (ui-principles §7); the
// column header row hides with it, since headers over a stack are noise.
const GRID = "flex flex-col gap-2 sm:grid sm:grid-cols-[2fr_1.2fr_1fr] sm:items-center sm:gap-6";

function ProjectsPage() {
  useCrumbs([{ label: "projects" }]);
  const projects = useListProjects();
  const { teamId } = useTeamScope();
  const [rollups, setRollups] = useState<Record<string, Rollup>>({});

  const totals = useMemo(() => {
    return Object.values(rollups).reduce(
      (acc, r) => ({ apps: acc.apps + r.apps, dbs: acc.dbs + r.dbs, errors: acc.errors + r.errors }),
      { apps: 0, dbs: 0, errors: 0 },
    );
  }, [rollups]);

  return (
    <>
      <PageHeader
        title="Projects"
        actions={
          <>
            <HeaderStat value={totals.apps} label="apps" />
            <HeaderStat value={totals.dbs} label="databases" />
            {totals.errors > 0 && <HeaderStat value={totals.errors} label="errors" tone="error" />}
            <CreateProjectDialog />
          </>
        }
      />
      <PageBody>
        <PageState
          query={projects}
          isEmpty={(list) => list.filter((p) => !teamId || p.team_id === teamId).length === 0}
          empty={
            <EmptyState
              emphasis
              title="Create your first project"
              hint="A project groups the environments and apps for one product. Create one, then deploy your first app into it."
              action={<CreateProjectDialog primary />}
            />
          }
        >
          {(list) => {
            const scoped = list.filter((p) => !teamId || p.team_id === teamId);
            const sorted = [...scoped].sort(
              (a, b) => (rollups[b.id]?.rank ?? 0) - (rollups[a.id]?.rank ?? 0),
            );
            return (
              <div>
                <div className={cn(GRID, "eyebrow hidden px-1 pb-2.5 sm:grid")}>
                  <span>Project</span>
                  <span>Status rollup</span>
                  <span>Resources</span>
                </div>
                <ul>
                  {sorted.map((p, i) => (
                    <ProjectRow
                      key={p.id}
                      project={p}
                      first={i === 0}
                      onRollup={(r) =>
                        setRollups((m) =>
                          m[p.id]?.rank === r.rank &&
                          m[p.id]?.apps === r.apps &&
                          m[p.id]?.dbs === r.dbs &&
                          m[p.id]?.errors === r.errors
                            ? m
                            : { ...m, [p.id]: r },
                        )
                      }
                    />
                  ))}
                </ul>
              </div>
            );
          }}
        </PageState>
      </PageBody>
    </>
  );
}

function ProjectRow({
  project,
  first,
  onRollup,
}: {
  project: Project;
  first: boolean;
  onRollup: (r: Rollup) => void;
}) {
  const envs = useListEnvironments(project.id);
  const envIds = useMemo(() => (envs.data ?? []).map((e) => e.id), [envs.data]);

  const appQueries = useQueries({ queries: envIds.map((id) => getListApplicationsQueryOptions(id)) });
  const dbQueries = useQueries({ queries: envIds.map((id) => getListDatabasesQueryOptions(id)) });

  const apps = useMemo(() => appQueries.flatMap((q) => q.data ?? []), [appQueries]);
  const dbs = useMemo(() => dbQueries.flatMap((q) => q.data ?? []), [dbQueries]);

  const statuses = useMemo(
    () => [...apps.map((a) => normalizeStatus(a.status)), ...dbs.map((d) => normalizeStatus(d.status))],
    [apps, dbs],
  );
  const worst = statuses.reduce<Status>((acc, s) => (SEVERITY[s] > SEVERITY[acc] ? s : acc), "stopped");
  const errors = statuses.filter((s) => s === "error").length;
  const degraded = statuses.filter((s) => s === "degraded").length;
  const deploying = statuses.filter((s) => s === "deploying").length;
  const stopped = statuses.filter((s) => s === "stopped").length;

  const rank = statuses.length === 0 ? 0 : SEVERITY[worst];
  useEffect(
    () => onRollup({ rank, apps: apps.length, dbs: dbs.length, errors }),
    [rank, apps.length, dbs.length, errors, onRollup],
  );

  // The one-line "what is actually wrong" under the pill — the agent's own
  // words, never our paraphrase (ui-principles §1).
  const detail =
    apps.find((a) => normalizeStatus(a.status) === worst && a.status_detail)?.status_detail ??
    dbs.find((d) => normalizeStatus(d.status) === worst && d.status_detail)?.status_detail;

  const label = (() => {
    if (statuses.length === 0) return "empty";
    if (errors > 0) return `${errors} ${errors === 1 ? "error" : "errors"}`;
    if (degraded > 0) return `${degraded} degraded`;
    if (deploying > 0) return `${deploying} deploying`;
    if (stopped === statuses.length) return `${stopped} stopped`;
    return "all running";
  })();

  return (
    <li>
      <Link
        to="/projects/$projectId"
        params={{ projectId: project.id }}
        className={cn(
          GRID,
          "border-t px-1 py-5 hover:bg-raised/60",
          first ? "border-t-[1.5px] border-border-strong" : "border-border",
          errors > 0 && "bg-linear-to-r from-status-error/[0.06] to-transparent to-60%",
        )}
      >
        <span className="flex min-w-0 items-center gap-3.5">
          {statuses.length > 0 ? <StatusDot status={worst} /> : <span className="w-2.5" aria-hidden />}
          <span className="min-w-0">
            <span className="block truncate text-[19px] font-semibold tracking-tight">{project.name}</span>
            <span className="mt-0.5 block truncate font-mono text-[11.5px] text-text-faint">
              {(envs.data ?? []).map((e) => e.name).join(" · ") || "no environments"}
            </span>
          </span>
        </span>

        <span className="min-w-0">
          <StatusPill status={statuses.length === 0 ? "unknown" : worst}>{label}</StatusPill>
          {detail && <span className="mt-1.5 block truncate text-[11.5px] text-text-faint">{detail}</span>}
        </span>

        <span className="font-mono text-xs text-text-mid">
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
      onSuccess: (res) => void navigate({ to: "/projects/$projectId", params: { projectId: res.project.id } }),
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
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New project
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Create a project"
        description="A project groups environments (production, staging, previews) for one product."
      >
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="acme"
              />
            )}
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
