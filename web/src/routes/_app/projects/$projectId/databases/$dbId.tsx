// Managed database detail layout: masthead + tabs (Overview · Backups ·
// Connection · Settings), mirroring the application layout so the two resource
// types are learned once.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useGetDatabase } from "@/api/gen/databases/databases";
import { useGetProject } from "@/api/gen/projects/projects";
import { DatabaseRestoreWatch, restoreSourceLabel, useRestoreInFlight } from "@/components/db-restore-progress";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { StatusBadge } from "@/components/status-badge";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId")({
  component: DatabaseLayout,
});

const TABS = [
  { to: "", label: "Overview" },
  { to: "backups", label: "Backups" },
  { to: "connection", label: "Connection" },
  { to: "settings", label: "Settings" },
] as const;

function DatabaseLayout() {
  const { projectId, dbId } = Route.useParams();
  const project = useGetProject(projectId);
  const db = useGetDatabase(dbId);
  const restoring = useRestoreInFlight(dbId);

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: db.data?.name ?? dbId },
  ]);

  if (db.isError) {
    return (
      <ResourceGone
        kind="managed database"
        error={db.error}
        backTo={`/projects/${projectId}`}
        backLabel="Back to the project"
      />
    );
  }

  return (
    <>
      <PageHeader
        size="sm"
        title={db.data?.name ?? "…"}
        badge={
          // The state word is carried in the status colour, not just the dot
          // beside it: a stopped or errored database has to read as one from
          // across the room, and a neutral word next to a red dot argues with
          // itself. StatusBadge also speaks the shared vocabulary, so a
          // provisioning database says "deploying" like everything else.
          <span className="flex flex-wrap items-center gap-2">
            <StatusBadge status={db.data?.status} />
            {db.data && (
              <span className="ml-1.5 font-mono text-[12px] text-text-faint">
                {db.data.engine} {db.data.version}
              </span>
            )}
            {/* 10d's footer promises "progress continues on the database
                card". The badge stays the agent's observed status — a SQL
                restore runs inside a container that keeps reporting running,
                and the badge must not pretend otherwise — so the restore gets
                its own words beside it until the agent's next report. */}
            {restoring && (
              <span
                role="status"
                className="ml-1.5 flex items-center gap-1.5 font-mono text-[11.5px] text-status-deploying"
              >
                <span aria-hidden className="animate-status-pulse">
                  ▸
                </span>
                restoring from {restoreSourceLabel(restoring.backup)} · offline during the restore · no cancel
              </span>
            )}
          </span>
        }
        below={
          <nav className="-mb-px flex gap-5 overflow-x-auto" aria-label="Database">
            {TABS.map((t) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to === "" ? "." : t.to}
                activeOptions={{ exact: t.to === "" }}
                className="whitespace-nowrap border-b-2 border-transparent px-0.5 py-2.5 text-[13px] text-text-mid hover:text-text"
                activeProps={{ className: "border-border-strong font-semibold text-text" }}
              >
                {t.label}
              </Link>
            ))}
          </nav>
        }
      />
      {/* The layout owns the page gutters so every tab is inset identically. */}
      <PageBody>
        <Outlet />
      </PageBody>
      {/* One restore watch per database, whichever tab started it: it holds
          the 10d popup open until the agent reports, and clears the line in
          the masthead above when it does. */}
      <DatabaseRestoreWatch projectId={projectId} dbId={dbId} />
    </>
  );
}
