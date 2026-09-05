// Project settings · General (canvas 12c): the project's own facts, and the one
// destructive act that belongs to the project rather than to anything in it.
//
// The board draws this tab as a form — Name, Slug, Team, Default environment,
// an ink Save. The API has no way to change any of it: /projects/{id} is GET
// and DELETE only, Project carries no slug, and no environment is marked as
// the default. A form whose every field is disabled is a dead end dressed as
// a control, so until an update operation exists the facts are read-only
// facts, in the read-only vocabulary the rest of the panel uses.
//
// The danger zone is where the board and the backend disagree most, and the
// backend wins. 12c's card says the confirm will list "3 apps, 1 database, 2
// previews" — a cascade. handleDeleteProject refuses with 409 while any
// application or database remains, because a managed database is torn down by
// a two-phase flow keyed on its own row and cascading it away would leave the
// container and its data volume running with nothing that knows they exist.
// So the card counts what is inside, names it, links to it, and only offers
// the typed-name confirm once the project is empty.
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { getListApplicationsQueryOptions } from "@/api/gen/applications/applications";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListDatabasesQueryOptions } from "@/api/gen/databases/databases";
import { getGetProjectQueryKey, getListProjectsQueryKey, useDeleteProject, useGetProject } from "@/api/gen/projects/projects";
import type { ProjectDetail } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Skeleton, SkeletonForm, useSkeletonDelay } from "@/components/ui/skeleton";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/settings/")({ component: GeneralTab });

function GeneralTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const me = useGetMe();

  // Canvas 12c datelines this page from the project, not from the projects
  // list: the trail names where you are inside the project, and PROJECTS is
  // already one click away in the top bar.
  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings" },
  ]);

  // Deleting needs admin on the project's team; a panel owner has it
  // everywhere (teams.RoleForProject). Resolved here so the pill can say so
  // instead of letting the server's 403 explain it afterwards.
  const team = me.data?.teams.find((t) => t.id === project.data?.project.team_id);
  const role: Role | undefined = me.data?.role === "owner" ? "owner" : team?.role;

  // The single-resource skeleton (10e), behind the same 200 ms gate every list
  // gets — PageState paints a custom `loading` immediately, so the gate is
  // applied here.
  const showSkeleton = useSkeletonDelay(project.isPending);

  return (
    <div className="max-w-2xl">
      <PageState query={project} loading={showSkeleton ? <SkeletonForm fields={4} columns={1} /> : null}>
        {(detail) => (
          <>
            <ProjectFacts detail={detail} teamName={team?.name} />
            <DangerZone projectId={projectId} detail={detail} canDelete={atLeast(role, "admin")} />
          </>
        )}
      </PageState>
    </div>
  );
}

function ProjectFacts({ detail, teamName }: { detail: ProjectDetail; teamName: string | undefined }) {
  const p = detail.project;
  const envs = detail.environments;
  return (
    <>
      <FactCard title="Project">
        <Fact label="Name">{p.name}</Fact>
        <Fact label="Team">{teamName ?? p.team_id}</Fact>
        <Fact label="Environments">{envs.map((e) => e.name).join(" · ") || "none yet"}</Fact>
        <Fact label="Created">
          <span title={absoluteTime(p.created_at)}>{relativeTime(p.created_at)}</span>
        </Fact>
        <Fact label="ID">{p.id}</Fact>
      </FactCard>
      <p className="mt-2 text-xs leading-relaxed text-text-faint">Name and team are fixed once a project is created.</p>
    </>
  );
}

/** "3 apps", "1 database" — the counts the card and the pill both speak in. */
function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function DangerZone({
  projectId,
  detail,
  canDelete,
}: {
  projectId: string;
  detail: ProjectDetail;
  canDelete: boolean;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [blocked, setBlocked] = useState<string | null>(null);
  const name = detail.project.name;
  const envs = detail.environments;
  const envName = (id: string) => envs.find((e) => e.id === id)?.name ?? "";

  // The same per-environment fan-out the projects list uses to count a row's
  // resources — the API has no project-level "what is inside" call.
  const appQueries = useQueries({ queries: envs.map((e) => getListApplicationsQueryOptions(e.id)) });
  const dbQueries = useQueries({ queries: envs.map((e) => getListDatabasesQueryOptions(e.id)) });
  const all = [...appQueries, ...dbQueries];
  const apps = appQueries.flatMap((q) => q.data ?? []);
  const dbs = dbQueries.flatMap((q) => q.data ?? []);
  const counting = all.some((q) => q.isPending);
  const countFailed = !counting && all.some((q) => q.isError);
  const showCounting = useSkeletonDelay(counting);
  const inUse = apps.length + dbs.length > 0;

  const del = useDeleteProject({
    mutation: {
      onSuccess: () => {
        // /projects renders from cache, and nothing streams project changes
        // in — so the row we just deleted would still be sitting there when we
        // land. Drop both the list and this project's own entry first, then
        // navigate, so the page we arrive at is one we can vouch for.
        void qc.invalidateQueries({ queryKey: getListProjectsQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(projectId) });
        toastSuccess(`Deleted ${name}`);
        void navigate({ to: "/projects" });
      },
      onError: (e: unknown) => {
        // The refusal is an instruction, not a notification: it stays on the
        // card. It only reaches here when the counts above were stale — a
        // teammate added something between the count and the click.
        if (e instanceof ApiError && e.status === 409) {
          setBlocked(e.message);
          all.forEach((q) => void q.refetch());
          return;
        }
        toastFailed("Could not delete the project", e);
      },
    },
  });

  const reason = !canDelete
    ? "Deleting a project needs admin on its team — ask an owner"
    : inUse
      ? "Delete the applications and databases inside first"
      : undefined;

  const inside = [apps.length > 0 && count(apps.length, "app", "apps"), dbs.length > 0 && count(dbs.length, "database", "databases")]
    .filter(Boolean)
    .join(" and ");

  return (
    // 12c draws this as one plain white card behind a heavy red rule — no
    // eyebrow: "Delete this project" already says which zone this is, and a
    // second heading only pushes the sentence that matters further down.
    <section className="mt-6 rounded-lg border-[1.5px] border-status-error/40 bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3.5">
        <div className="min-w-0 flex-1">
          <p className="text-[13.5px] font-semibold text-text">Delete this project</p>
          <div className="mt-[3px] text-xs leading-relaxed text-text-mid" aria-live="polite">
            {counting ? (
              showCounting ? (
                <Skeleton className="h-3 w-56 max-w-full" />
              ) : null
            ) : countFailed ? (
              <span className="flex flex-wrap items-center gap-2">
                Couldn't check what's inside this project.
                <ActionButton
                  size="sm"
                  variant="ghost"
                  state={all.some((q) => q.isFetching) ? "busy" : "idle"}
                  busyLabel="Checking…"
                  onClick={() => all.forEach((q) => void q.refetch())}
                >
                  Retry
                </ActionButton>
              </span>
            ) : inUse ? (
              <>
                {inside} {apps.length + dbs.length === 1 ? "is" : "are"} still inside — delete them first, so their containers
                and data volumes are torn down properly.
              </>
            ) : envs.length === 0 ? (
              <>Removes the project. It has no environments yet, so nothing else goes with it.</>
            ) : (
              <>
                Removes the project and its {count(envs.length, "environment", "environments")}. No applications or databases
                are left inside.
              </>
            )}
          </div>
        </div>
        <ConfirmDestructive
          trigger={
            <Button variant="danger" disabledReason={reason}>
              Delete
            </Button>
          }
          title={`Delete ${name}?`}
          blastRadius={[
            envs.length === 0
              ? "This project (it has no environments)"
              : `This project and its ${count(envs.length, "environment", "environments")} (${envs.map((e) => e.name).join(", ")})`,
            "Its notifiers, webhook endpoints and their delivery history, shared variables, and inbox items",
            "Cannot be undone",
          ]}
          confirmName={name}
          actionLabel="Delete project"
          pending={del.isPending}
          pendingLabel="Deleting…"
          onConfirm={() => del.mutate({ id: projectId })}
        />
      </div>

      {/* What stands in the way, each a link to the page it is deleted from.
          The list is the next verb — a count alone would send the operator
          hunting through every environment for the one database left behind. */}
      {inUse && (
        <ul className="mt-3 divide-y divide-border-subtle overflow-hidden rounded-md border border-border">
          {apps.map((a) => (
            <li key={a.id}>
              <Link
                to="/projects/$projectId/applications/$appId"
                params={{ projectId, appId: a.id }}
                className="flex items-center justify-between gap-3 px-3 py-2 hover:bg-raised"
              >
                <span className="mono min-w-0 truncate text-[12.5px] text-text">{a.name}</span>
                <span className="mono shrink-0 text-[11px] text-text-faint">application · {envName(a.environment_id)}</span>
              </Link>
            </li>
          ))}
          {dbs.map((d) => (
            <li key={d.id}>
              <Link
                to="/projects/$projectId/databases/$dbId"
                params={{ projectId, dbId: d.id }}
                className="flex items-center justify-between gap-3 px-3 py-2 hover:bg-raised"
              >
                <span className="mono min-w-0 truncate text-[12.5px] text-text">{d.name}</span>
                <span className="mono shrink-0 text-[11px] text-text-faint">
                  {d.engine} database · {envName(d.environment_id)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}

      {blocked && (
        <p role="alert" className="mt-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {blocked}
        </p>
      )}
    </section>
  );
}
