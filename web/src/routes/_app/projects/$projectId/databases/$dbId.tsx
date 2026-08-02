// Managed database detail layout: masthead + tabs (Overview · Backups ·
// Connection · Settings), mirroring the application layout so the two resource
// types are learned once.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useGetDatabase } from "@/api/gen/databases/databases";
import { useGetProject } from "@/api/gen/projects/projects";
import { PageBody, PageHeader } from "@/components/page-header";
import { StatusDot } from "@/components/status-badge";
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
    <>
      <PageHeader
        size="sm"
        title={db.data?.name ?? "…"}
        badge={
          <span className="flex items-center gap-2">
            <StatusDot status={db.data?.status} />
            <span className="font-mono text-[11px] font-medium uppercase tracking-wide text-text-mid">
              {db.data?.status ?? "unknown"}
            </span>
            {db.data && (
              <span className="ml-1.5 font-mono text-[12px] text-text-faint">
                {db.data.engine} {db.data.version}
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
    </>
  );
}
