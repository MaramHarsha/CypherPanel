// Application detail layout: header (name, domain, status) + slice-2 tabs
// (Overview · Deployments · Logs · Env vars · Settings). Previews and
// scheduled-tasks tabs arrive with slice 4 — no stub tabs.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { useGetApplication } from "@/api/gen/applications/applications";
import { useGetProject } from "@/api/gen/projects/projects";
import { StatusBadge } from "@/components/status-badge";
import { useCrumbs } from "@/lib/crumbs";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId")({
  component: ApplicationLayout,
});

const TABS = [
  { to: "", label: "Overview" },
  { to: "deployments", label: "Deployments" },
  { to: "logs", label: "Logs" },
  { to: "env", label: "Env vars" },
  { to: "settings", label: "Settings" },
] as const;

function ApplicationLayout() {
  const { projectId, appId } = Route.useParams();
  const project = useGetProject(projectId);
  const app = useGetApplication(appId);

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: app.data?.name ?? appId },
  ]);

  const domain = app.data?.route.domain;
  const https = app.data?.route.https ?? true;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-3">
          <h1 className="truncate text-base font-semibold text-text">{app.data?.name ?? "…"}</h1>
          <StatusBadge status={app.data?.status} />
        </div>
        {domain && (
          <a
            href={`${https ? "https" : "http"}://${domain}`}
            target="_blank"
            rel="noreferrer"
            className="mono inline-flex items-center gap-1 text-xs text-text-mid hover:text-accent"
          >
            {domain} <ExternalLink className="h-3 w-3" aria-hidden />
          </a>
        )}
      </div>

      <nav className="flex gap-0.5 overflow-x-auto border-b border-border" aria-label="Application">
        {TABS.map((t) => (
          <Link
            key={t.label}
            from={Route.fullPath}
            to={t.to === "" ? "." : t.to}
            activeOptions={{ exact: t.to === "" }}
            className="-mb-px whitespace-nowrap border-b-2 border-transparent px-3 py-2 text-[13px] text-text-mid hover:text-text"
            activeProps={{ className: cn("-mb-px border-accent text-text") }}
          >
            {t.label}
          </Link>
        ))}
      </nav>

      <Outlet />
    </div>
  );
}
