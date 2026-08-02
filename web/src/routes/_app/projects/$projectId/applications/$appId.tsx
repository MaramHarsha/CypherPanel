// Application detail layout: masthead (name, status, the deploy action) plus
// the resource tabs. "Deploy now" lives here rather than on the Deployments
// tab because it is the app's primary action from every tab — you should never
// have to navigate to start a deploy.
import { createFileRoute, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { ExternalLink, Rocket } from "lucide-react";
import { toast } from "sonner";
import { useGetApplication } from "@/api/gen/applications/applications";
import { useDeployApplication, useListDeployments } from "@/api/gen/deployments/deployments";
import { useGetProject } from "@/api/gen/projects/projects";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
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
  { to: "previews", label: "Previews" },
  { to: "tasks", label: "Tasks" },
  { to: "settings", label: "Settings" },
] as const;

/** A deployment is still moving until the plane records a terminal outcome. */
export function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

function ApplicationLayout() {
  const { projectId, appId } = Route.useParams();
  const project = useGetProject(projectId);
  const app = useGetApplication(appId);

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: app.data?.name ?? appId },
  ]);

  // Without this the masthead renders "…" forever over a live tab strip, and
  // the real error hides inside whichever tab is open (ui-principles §1).
  if (app.isError) {
    return (
      <ResourceGone
        kind="application"
        error={app.error}
        backTo={`/projects/${projectId}`}
        backLabel="Back to the project"
      />
    );
  }

  const domain = app.data?.route.domain;
  const https = app.data?.route.https ?? true;
  const status = app.data?.status;

  return (
    <>
      <PageHeader
        size="sm"
        title={app.data?.name ?? "…"}
        badge={
          <span className="flex items-center gap-2">
            <StatusDot status={status} />
            <span className="font-mono text-[11px] font-medium uppercase tracking-wide text-text-mid">
              {status ?? "unknown"}
            </span>
            {domain && (
              <a
                href={`${https ? "https" : "http"}://${domain}`}
                target="_blank"
                rel="noreferrer"
                className="ml-1.5 inline-flex items-center gap-1 font-mono text-[12px] text-accent hover:underline"
              >
                {domain} <ExternalLink className="h-3 w-3" aria-hidden />
              </a>
            )}
          </span>
        }
        actions={<DeployButton appId={appId} branch={app.data?.source.branch} />}
        below={
          <nav className="-mb-px flex gap-5 overflow-x-auto" aria-label="Application">
            {TABS.map((t) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to === "" ? "." : t.to}
                activeOptions={{ exact: t.to === "" }}
                className="whitespace-nowrap border-b-2 border-transparent px-0.5 py-2.5 text-[13px] text-text-mid hover:text-text"
                activeProps={{ className: cn("border-border-strong font-semibold text-text") }}
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

/** Disabled while a deploy is in flight — the plane, not the button, is the
 *  source of truth for whether one is running (ADR-005). */
function DeployButton({ appId, branch }: { appId: string; branch: string | undefined }) {
  const navigate = useNavigate();
  const { projectId } = Route.useParams();
  const deployments = useListDeployments(appId);
  const active = (deployments.data ?? []).some((d) => !isTerminal(d.status));

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        toast.success("Deploy started");
        void navigate({
          to: "/projects/$projectId/applications/$appId/deployments",
          params: { projectId, appId },
          search: { dep: d.id },
        });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Deploy failed to start"),
    },
  });

  return (
    <Button
      variant="primary"
      disabled={deploy.isPending || active}
      onClick={() => deploy.mutate({ id: appId, data: {} })}
      title={active ? "A deploy is already running" : undefined}
    >
      <Rocket className="h-3.5 w-3.5" />
      {active ? "Deploying…" : `Deploy${branch ? ` ${branch}` : " now"}`}
    </Button>
  );
}
