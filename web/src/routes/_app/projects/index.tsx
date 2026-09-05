// Projects landing — the no-dashboard bet (web-ui-design.md §3): each row
// carries an aggregated status rollup, worst-first, red visible from across
// the room. Mission Control renders it as a broadsheet table: an ink rule
// above the first row, hairlines between the rest, and a tint bleeding off the
// left edge of anything broken.
//
// On a phone (canvas 14b) the same list is the on-call subset: no masthead, an
// eyebrow that says how it is sorted, and one card per project with the status
// word on the name row and a single mono line under it. Same data, same order.
import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListApplicationsQueryOptions } from "@/api/gen/applications/applications";
import { getListDatabasesQueryOptions } from "@/api/gen/databases/databases";
import { useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import type { Project } from "@/api/gen/model";
import { CreateProjectDialog } from "@/components/create-project-dialog";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { normalizeStatus, StatusDot, StatusPill, StatusWord, type Status } from "@/components/status-badge";
import { useCrumbs } from "@/lib/crumbs";
import { useRowNavigation } from "@/lib/keys";
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

// The three broadsheet columns, from `sm` up. Below it every row is a card
// and the column header row hides with the grid, since headers over a stack
// of cards are noise.
const GRID = "sm:grid sm:grid-cols-[2fr_1.2fr_1fr] sm:items-center sm:gap-4";

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
  // Canvas 14g TAB ORDER: one stop per row; j/k and ↑↓ walk the list, Enter opens.
  const rowNav = useRowNavigation();

  return (
    <>
      {/* 14b draws no masthead on a phone: the eyebrow below is the title, and
          creating a project is not what an on-call glance is for — the empty
          state still offers it, so a fresh panel is never a dead end. */}
      <PageHeader className="max-sm:hidden" title="Projects" actions={<CreateProjectDialog />} />
      {/* 1a hangs the first row close under the masthead rule — the ink line
          above it is the separator, so a full 24px band would read as a gap. */}
      <PageBody className="pt-0 pb-9 sm:pt-2">
        <div className="pb-2.5 pt-4 sm:hidden">
          <h1 className="sr-only">Projects</h1>
          <Eyebrow>Projects · worst first</Eyebrow>
        </div>
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
                <div className={cn(GRID, "eyebrow hidden px-2 pb-2.5")}>
                  <span>Project</span>
                  <span>Status rollup</span>
                  <span>Resources</span>
                </div>
                <ul ref={rowNav} className="max-sm:space-y-2.5">
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

  // The rollup, said two ways: the pill's sentence for the broadsheet ("all
  // running", "2 deploying") and the bare word 14b sets on a phone card's name
  // row ("RUNNING", "DEPLOYING"), where a count beside a healthy word is noise
  // and only a problem earns its number.
  const label = (() => {
    if (statuses.length === 0) return "empty";
    if (errors > 0) return `${errors} ${errors === 1 ? "error" : "errors"}`;
    if (degraded > 0) return `${degraded} degraded`;
    if (deploying > 0) return `${deploying} deploying`;
    if (stopped === statuses.length) return `${stopped} stopped`;
    return "all running";
  })();
  const word =
    statuses.length === 0 || errors > 0 || degraded > 0
      ? label
      : deploying > 0
        ? "deploying"
        : stopped === statuses.length
          ? "stopped"
          : "running";

  const resources = `${apps.length} app${apps.length === 1 ? "" : "s"}${
    dbs.length > 0 ? ` · ${dbs.length} db${dbs.length === 1 ? "" : "s"}` : ""
  }`;
  const rollup = statuses.length === 0 ? "unknown" : worst;

  return (
    <li data-row>
      <Link
        to="/projects/$projectId"
        params={{ projectId: project.id }}
        className={cn(
          // A phone card (14b): 10px corners, the surface colour, 14px of
          // padding, and a red-tinted 1.5px line when something is broken.
          "block max-sm:rounded-[10px] max-sm:bg-surface max-sm:p-3.5",
          errors > 0 ? "max-sm:border-[1.5px] max-sm:border-status-error/40" : "max-sm:border max-sm:border-border",
          // The broadsheet row (1a) from `sm` up.
          GRID,
          "sm:rounded-md sm:border-t sm:px-2 sm:py-5 sm:hover:bg-raised",
          first ? "sm:border-t-[1.5px] sm:border-border-strong" : "sm:border-border",
          errors > 0 && "sm:bg-linear-to-r sm:from-status-error/[0.06] sm:to-transparent sm:to-60%",
        )}
      >
        <span className="flex min-w-0 items-center gap-[9px] sm:gap-3.5">
          {statuses.length > 0 ? (
            <StatusDot status={worst} className="max-sm:size-[9px]" />
          ) : (
            <span className="w-2.5" aria-hidden />
          )}
          <span className="min-w-0 flex-1">
            <span className="block truncate text-[15px] font-semibold sm:text-[19px] sm:tracking-tight">
              {project.name}
            </span>
            <span className="mt-0.5 hidden truncate font-mono text-[11.5px] text-text-faint sm:block">
              {(envs.data ?? []).map((e) => e.name).join(" · ") || "no environments"}
            </span>
          </span>
          {/* 14b: the status word rides the name row; the marker at the row's
              start gives it its shape, so the word needs no pill. */}
          <StatusWord status={rollup} className="ml-auto shrink-0 sm:hidden">
            {word}
          </StatusWord>
        </span>

        {/* 14b's one mono line: what is wrong, in the agent's words, or what
            is here. The canvas adds an age ("18 min", "2 h ago") and the live
            deploy stage — neither exists on the project list yet, and a made
            up timestamp is worse than none. */}
        <span className="mt-[5px] block truncate font-mono text-[11px] text-text-faint sm:hidden">
          {detail || resources}
        </span>

        <span className="hidden min-w-0 sm:block">
          <StatusPill status={rollup}>{label}</StatusPill>
          {detail && <span className="mt-1.5 block truncate text-[11.5px] text-text-faint">{detail}</span>}
        </span>

        <span className="hidden font-mono text-xs text-text-dim sm:block">{resources}</span>
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
