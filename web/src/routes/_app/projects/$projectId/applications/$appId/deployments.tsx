// Application · Deployments: the pipeline list, deploy/rollback actions, and
// the deployment drawer — live build log + the celebrated stage animation
// (web-ui-design.md §4). Never blocks navigation during a deploy.
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Rocket, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { useGetApplication } from "@/api/gen/applications/applications";
import {
  getStreamDeploymentLogsUrl,
  useDeployApplication,
  useGetDeployment,
  useListDeployments,
  useRollbackDeployment,
} from "@/api/gen/deployments/deployments";
import type { Deployment } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { EmptyState } from "@/components/empty-state";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { PipelineStages } from "@/components/pipeline-stages";
import { Button } from "@/components/ui/button";
import { Drawer } from "@/components/ui/drawer";
import { relativeTime, absoluteTime } from "@/lib/time";

interface DeploymentsSearch {
  dep?: string;
}

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/deployments")({
  validateSearch: (s: Record<string, unknown>): DeploymentsSearch =>
    typeof s.dep === "string" ? { dep: s.dep } : {},
  component: DeploymentsTab,
});

function DeploymentsTab() {
  const { appId } = Route.useParams();
  const { dep } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const deployments = useListDeployments(appId);
  const app = useGetApplication(appId);

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        toast.success("Deploy started");
        void navigate({ search: { dep: d.id } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Deploy failed to start"),
    },
  });

  const active = (deployments.data ?? []).some((d) => !isTerminal(d.status));

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Deployments</Eyebrow>
        <Button
          variant="primary"
          size="sm"
          disabled={deploy.isPending || active}
          onClick={() => deploy.mutate({ id: appId, data: {} })}
        >
          <Rocket className="h-3.5 w-3.5" /> Deploy {app.data?.source.branch ?? ""}
        </Button>
      </div>

      <PageState
        query={deployments}
        empty={
          <EmptyState
            title="No deployments yet"
            hint="Deploy builds the repository's Dockerfile and rolls it out with a health-gated, zero-downtime switch. Push-to-deploy is on the Overview tab."
            action={
              <Button variant="primary" size="sm" disabled={deploy.isPending} onClick={() => deploy.mutate({ id: appId, data: {} })}>
                <Rocket className="h-3.5 w-3.5" /> Deploy now
              </Button>
            }
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border rounded-md border border-border bg-surface">
            {list.map((d) => (
              <DeploymentRow key={d.id} deployment={d} onOpen={() => void navigate({ search: { dep: d.id } })} />
            ))}
          </ul>
        )}
      </PageState>

      <DeploymentDrawer depId={dep ?? null} onClose={() => void navigate({ search: {} })} />
    </div>
  );
}

function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

function DeploymentRow({ deployment: d, onOpen }: { deployment: Deployment; onOpen: () => void }) {
  const { appId } = Route.useParams();
  const rollback = useRollbackDeployment({
    mutation: {
      onSuccess: () => toast.success("Rollback started — the previous revision is coming back"),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Rollback failed to start"),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <button type="button" onClick={onOpen} className="flex min-w-0 flex-1 flex-col items-start gap-1 text-left">
        <span className="flex items-center gap-2">
          <span className="mono text-xs text-text-mid">{d.revision_id}</span>
          <span className="mono text-[11px] text-text-faint">{d.trigger}</span>
        </span>
        <PipelineStages status={d.status} detail={d.detail} />
      </button>
      <span className="flex shrink-0 items-center gap-2">
        <span className="mono text-xs text-text-faint" title={absoluteTime(d.created_at)}>
          {relativeTime(d.created_at)}
        </span>
        {d.status === "succeeded" && (
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Roll back to ${d.revision_id}`}
            disabled={rollback.isPending}
            onClick={() => rollback.mutate({ id: d.id })}
          >
            <Undo2 className="h-3.5 w-3.5" /> Roll back
          </Button>
        )}
      </span>
      {appId !== d.application_id && null}
    </li>
  );
}

function DeploymentDrawer({ depId, onClose }: { depId: string | null; onClose: () => void }) {
  const dep = useGetDeployment(depId ?? "", { query: { enabled: depId !== null } });
  const d = dep.data;

  return (
    <Drawer
      open={depId !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
      label="Deployment detail"
      title={<span className="mono">{depId ?? ""}</span>}
      wide
    >
      {d && (
        <div className="flex h-full min-h-0 flex-col gap-4 p-4">
          <div className="space-y-2">
            <PipelineStages status={d.status} detail={d.detail} />
            {d.status === "failed" && d.detail && (
              <p className="rounded-md border border-danger/30 bg-danger/5 px-3 py-2 text-[13px] text-text">{d.detail}</p>
            )}
            <p className="mono text-xs text-text-faint">
              revision {d.revision_id} · {d.trigger} · started {relativeTime(d.created_at)}
              {d.finished_at ? ` · finished ${relativeTime(d.finished_at)}` : ""}
            </p>
          </div>
          <LogViewer url={getStreamDeploymentLogsUrl(d.id)} className="min-h-0 flex-1" />
        </div>
      )}
    </Drawer>
  );
}
