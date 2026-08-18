// Application · Deployments: the pipeline list plus the live deploy drawer —
// an ink panel in both themes, because what it frames is a log (4e token
// table). Never blocks navigation during a deploy: the state lives on the
// server and this is a window onto it (ui-principles §3).
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Rocket, Undo2 } from "lucide-react";
import { toast } from "sonner";
import { ConfirmRollback } from "@/components/confirm-rollback";
import {
  getStreamDeploymentLogsUrl,
  useDeployApplication,
  useGetDeployment,
  useListDeployments,
  useRollbackDeployment,
} from "@/api/gen/deployments/deployments";
import { useGetApplication } from "@/api/gen/applications/applications";
import type { Deployment } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { PipelineStages } from "@/components/pipeline-stages";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Drawer } from "@/components/ui/drawer";
import { relativeTime, absoluteTime } from "@/lib/time";
import { cn } from "@/lib/utils";

interface DeploymentsSearch {
  dep?: string;
}

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/deployments")({
  validateSearch: (s: Record<string, unknown>): DeploymentsSearch =>
    typeof s.dep === "string" ? { dep: s.dep } : {},
  component: DeploymentsTab,
});

function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

/** Deployment status → the shared status vocabulary (ui-principles §5). */
function toStatus(d: Deployment, isNewest: boolean): string {
  if (d.status === "failed") return "error";
  if (!isTerminal(d.status)) return "deploying";
  return isNewest ? "running" : "stopped";
}

/** The word for what this deployment is now — serving, superseded, failed. */
function outcome(d: Deployment, isNewest: boolean): string {
  if (d.status === "failed") return "failed";
  if (!isTerminal(d.status)) return d.status;
  return isNewest ? "serving" : "superseded";
}

function shortRev(id: string): string {
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function DeploymentsTab() {
  const { appId } = Route.useParams();
  const { dep } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const deployments = useListDeployments(appId);
  // Only for the rollback confirm's title — the layout above already has this
  // cached, so this is a cache read rather than a second request.
  const appName = useGetApplication(appId).data?.name ?? "this application";

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        toast.success("Deploy started");
        void navigate({ search: { dep: d.id } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Deploy failed to start"),
    },
  });

  // "Serving" is the newest succeeded deployment — everything older that also
  // succeeded has been superseded by it.
  const newestSucceeded = (deployments.data ?? []).find((d) => d.status === "succeeded")?.id;

  return (
    <>
      <PageState
        query={deployments}
        empty={
          <EmptyState
            emphasis
            title="No deployments yet"
            hint="Deploy builds the repository's Dockerfile and rolls it out with a health-gated, zero-downtime switch. Push-to-deploy is on the Overview tab."
            action={
              <Button
                variant="primary"
                size="lg"
                disabled={deploy.isPending}
                onClick={() => deploy.mutate({ id: appId, data: {} })}
              >
                <Rocket className="h-3.5 w-3.5" /> Deploy now
              </Button>
            }
          />
        }
      >
        {(list) => (
          <ul>
            {list.map((d, i) => (
              <DeploymentRow
                key={d.id}
                deployment={d}
                first={i === 0}
                isNewest={d.id === newestSucceeded}
                appName={appName}
                serving={list.find((x) => x.id === newestSucceeded)}
                onOpen={() => void navigate({ search: { dep: d.id } })}
              />
            ))}
          </ul>
        )}
      </PageState>

      <DeploymentDrawer depId={dep ?? null} onClose={() => void navigate({ search: {} })} />
    </>
  );
}

function DeploymentRow({
  deployment: d,
  first,
  isNewest,
  appName,
  serving,
  onOpen,
}: {
  deployment: Deployment;
  first: boolean;
  isNewest: boolean;
  /** For the rollback confirm's title and its NOW row (canvas 9g). */
  appName: string;
  serving?: Deployment;
  onOpen: () => void;
}) {
  const rollback = useRollbackDeployment({
    mutation: {
      onSuccess: () => toast.success("Rollback started — the previous revision is coming back"),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Rollback failed to start"),
    },
  });

  const status = toStatus(d, isNewest);
  const live = !isTerminal(d.status);

  return (
    <li
      className={cn(
        "flex items-center gap-4 border-t py-4 pr-2",
        first ? "border-t-[1.5px] border-border-strong" : "border-border",
        live && "bg-linear-to-r from-status-deploying/[0.05] to-transparent to-70%",
        d.status === "failed" && "bg-linear-to-r from-status-error/[0.04] to-transparent to-70%",
      )}
    >
      <StatusDot status={status} />
      <button type="button" onClick={onOpen} className="min-w-0 flex-1 text-left">
        <span className="flex flex-wrap items-baseline gap-x-2.5">
          <span className="font-mono text-[13px] font-medium">{shortRev(d.revision_id)}</span>
          {d.detail && <span className="min-w-0 truncate text-[13px] text-text-mid">{d.detail}</span>}
        </span>
        <span className="mt-1 block text-[11.5px] text-text-faint">
          {d.trigger} · <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
          {d.finished_at && ` · finished ${relativeTime(d.finished_at)}`}
        </span>
        {live && <PipelineStages status={d.status} detail={d.detail} className="mt-2" />}
      </button>
      <span className="flex shrink-0 items-center gap-3">
        <span
          className={cn(
            "font-mono text-[11px] font-medium uppercase tracking-wide",
            status === "error" ? "text-danger" : status === "deploying" ? "text-status-deploying" : "text-text-faint",
          )}
        >
          {outcome(d, isNewest)}
        </span>
        {d.status === "succeeded" && !isNewest && (
          // Canvas 9g/13ae: never straight from the row. The confirm is where
          // the two revisions are put side by side and where "env vars don't
          // rewind" is said — both of which are unavailable from a button.
          <ConfirmRollback
            trigger={
              <Button size="sm" variant="ghost" aria-label={`Roll back to ${shortRev(d.revision_id)}`}>
                <Undo2 className="h-3.5 w-3.5" /> Roll back
              </Button>
            }
            appName={appName}
            now={{ rev: shortRev(serving?.revision_id ?? ""), detail: serving?.detail ?? undefined }}
            target={{ rev: shortRev(d.revision_id), detail: d.detail ?? undefined }}
            pending={rollback.isPending}
            onConfirm={() => rollback.mutate({ id: d.id })}
          />
        )}
      </span>
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
      title={
        <span className="flex items-baseline gap-2.5">
          <span className="font-mono">{d ? shortRev(d.revision_id) : ""}</span>
          <span className="text-[12px] font-normal text-[#8a8375]">deployment</span>
        </span>
      }
      wide
      tone="ink"
    >
      {d && (
        <div className="flex h-full min-h-0 flex-col gap-4 p-5">
          <PipelineStages status={d.status} detail={d.detail} tone="ink" />
          {d.status === "failed" && d.detail && (
            <p className="rounded-md border border-[#ff6a5e]/40 bg-[#ff6a5e]/10 px-3 py-2.5 text-[13px] text-[#ff9d94]">
              {d.detail}
            </p>
          )}
          <LogViewer url={getStreamDeploymentLogsUrl(d.id)} className="min-h-0 flex-1" />
          <p className="font-mono text-[11.5px] text-[#8a8375]">
            {d.trigger} · started <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
            {d.finished_at ? ` · finished ${relativeTime(d.finished_at)}` : ""}
          </p>
          {!isTerminal(d.status) && (
            <p className="text-[12px] text-[#8a8375]">
              Zero-downtime rollout — the old revision keeps serving until the new one is provably healthy.
            </p>
          )}
        </div>
      )}
    </Drawer>
  );
}
