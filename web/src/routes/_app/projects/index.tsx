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
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill, type Status } from "@/components/status-badge";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
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

// Below `sm` the table collapses to a stacked card (ui-principles §7); the
// column header row hides with it, since headers over a stack are noise.
const GRID = "flex flex-col gap-2 sm:grid sm:grid-cols-[2fr_1.2fr_1fr] sm:items-center sm:gap-4";

function ProjectsPage() {
  const projects = useListProjects();
  const { teamId } = useTeamScope();
  const scope = useScopedTeamName();
  // The trail is rooted in the team, always (ui-principles §4) — MERIDIAN-STUDIO
  // / PROJECTS. Spanning every team the user can see, there is no root to name,
  // so the trail simply starts at Projects rather than inventing one.
  useCrumbs(scope ? [{ label: scope }, { label: "projects" }] : [{ label: "projects" }]);
  // Each row computes its own worst status from its own queries and reports the
  // severity up, so the page can order the list without duplicating the fetch.
  const [ranks, setRanks] = useState<Record<string, number>>({});

  return (
    <>
      <PageHeader title="Projects" actions={<CreateProjectDialog />} />
      {/* 1a hangs the first row close under the masthead rule — the ink line
          above it is the separator, so a full 24px band would read as a gap. */}
      <PageBody className="pt-2 pb-9">
        <PageState
          query={projects}
          // The skeleton mirrors this page's own grid (10e), so nothing moves
          // when the rows arrive.
          skeletonColumns="2fr 1.2fr 1fr"
          skeletonDot
          isEmpty={(list) => list.filter((p) => !teamId || p.team_id === teamId).length === 0}
          empty={
            <EmptyState
              emphasis
              title="Create your first project"
              hint="A project groups the environments and apps for one product. Create one, then deploy your first app into it."
              action={<CreateProjectDialog />}
            />
          }
        >
          {(list) => {
            const scoped = list.filter((p) => !teamId || p.team_id === teamId);
            const sorted = [...scoped].sort((a, b) => (ranks[b.id] ?? 0) - (ranks[a.id] ?? 0));
            return (
              <div>
                <div className={cn(GRID, "eyebrow hidden px-2 pb-2.5 sm:grid")}>
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
                      onRank={(rank) => setRanks((m) => (m[p.id] === rank ? m : { ...m, [p.id]: rank }))}
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
  onRank,
}: {
  project: Project;
  first: boolean;
  onRank: (rank: number) => void;
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
  useEffect(() => onRank(rank), [rank, onRank]);

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
          "border-t rounded-md px-2 py-5 hover:bg-raised",
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

        <span className="font-mono text-xs text-text-dim">
          {apps.length} app{apps.length === 1 ? "" : "s"}
          {dbs.length > 0 && ` · ${dbs.length} db${dbs.length === 1 ? "" : "s"}`}
        </span>
      </Link>
    </li>
  );
}

/**
 * The team the page is speaking for. `null` scope means "every team I can see",
 * which has no single name — except for the common case of belonging to exactly
 * one, where the scope is unset only because there was never a choice to make.
 * The top-bar chip reads it the same way, so the two never disagree.
 */
function useScopedTeamName(): string | undefined {
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const teams = me.data?.teams ?? [];
  return teams.find((t) => t.id === teamId)?.name ?? (teams.length === 1 ? teams[0]?.name : undefined);
}

function CreateProjectDialog() {
  const navigate = useNavigate();
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const teams = me.data?.teams ?? [];
  const [name, setName] = useState("");
  const [team, setTeam] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = useCreateProject({
    mutation: {
      onSuccess: (res) => void navigate({ to: "/projects/$projectId", params: { projectId: res.project.id } }),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the project"),
    },
  });

  // Which team a project lands in is not recoverable by renaming it later, so
  // the choice is on screen rather than inferred from the current scope; the
  // scope is only the default. A scope left over from a team the user has since
  // left is ignored, or the select would show one team and submit another.
  const scoped = teams.some((t) => t.id === teamId) ? teamId : undefined;
  const chosen = team || scoped || teams[0]?.id;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate({ data: { name, ...(chosen ? { team_id: chosen } : {}) } });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size="lg">
          <Plus className="h-3.5 w-3.5" /> New project
        </Button>
      </DialogTrigger>
      <DialogContent
        className="max-w-[400px]"
        title="New project"
        description="A project groups apps and databases that ship together. Every project starts with a production environment."
      >
        <form onSubmit={submit} className="space-y-4">
          {/* A project name is prose, not a machine value — the one place the
              mono default is wrong (input.tsx). */}
          <Field label="Name" error={error ?? undefined}>
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                className="font-sans"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Atlas CRM"
              />
            )}
          </Field>
          {teams.length > 0 && (
            <Field label="Team">
              {(id) => (
                <Select
                  id={id}
                  className="font-sans"
                  value={chosen ?? ""}
                  onChange={(e) => setTeam(e.target.value)}
                >
                  {teams.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )}
          <div className="flex items-center justify-end gap-2.5 pt-1">
            <DialogClose asChild>
              <Button variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="accent"
              size="lg"
              state={create.isPending ? "busy" : "idle"}
              busyLabel="Creating…"
              disabledReason={name.trim() === "" ? "Name the project first" : undefined}
            >
              Create project →
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
