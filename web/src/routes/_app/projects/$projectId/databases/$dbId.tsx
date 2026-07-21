// Managed database detail layout: header + tabs (Overview · Backups ·
// Connection · Settings), mirroring the application layout.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useGetDatabase } from "@/api/gen/databases/databases";
import { useGetProject } from "@/api/gen/projects/projects";
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

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: db.data?.name ?? dbId },
  ]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-3">
          <h1 className="truncate text-base font-semibold text-text">{db.data?.name ?? "…"}</h1>
          <StatusBadge status={db.data?.status} />
        </div>
        {db.data && (
          <span className="mono text-xs text-text-faint">
            {db.data.engine} {db.data.version}
          </span>
        )}
      </div>

      <nav className="flex gap-0.5 overflow-x-auto border-b border-border" aria-label="Database">
        {TABS.map((t) => (
          <Link
            key={t.label}
            from={Route.fullPath}
            to={t.to === "" ? "." : t.to}
            activeOptions={{ exact: t.to === "" }}
            className="-mb-px whitespace-nowrap border-b-2 border-transparent px-3 py-2 text-[13px] text-text-mid hover:text-text"
            activeProps={{ className: "-mb-px border-accent text-text" }}
          >
            {t.label}
          </Link>
        ))}
      </nav>

      <Outlet />
    </div>
  );
}
