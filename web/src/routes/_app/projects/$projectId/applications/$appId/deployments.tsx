// Application · Deployments: the pipeline list plus the live deploy panel — an
// ink surface in both themes, because what it frames is a log (4e token
// table). Never blocks navigation during a deploy: the state lives on the
// server and this is a window onto it (ui-principles §3).
//
// The canvas draws the panel as a column standing beside the list, not as an
// overlay on top of it — "what is deploying?" and "what else is here?" are the
// same question, so dimming the list to answer one of them is a loss. Below
// the column's own width the same content becomes a sheet (canvas 14c).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Undo2, X } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { ConfirmRollback } from "@/components/confirm-rollback";
import {
  getListDeploymentsQueryKey,
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
import { ActionButton } from "@/components/ui/action-button";
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

/** The width at which the canvas's 460px panel and the list can both stand. */
const COLUMN_AT = 1024;

function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

/** Deployment status → the shared status vocabulary (ui-principles §5). */
function toStatus(d: Deployment, isNewest: boolean): string {
  if (d.status === "failed") return "error";
  if (!isTerminal(d.status)) return "deploying";
  return isNewest ? "running" : "stopped";
}

/**
 * The word for what this deployment is now — serving, superseded, failed, or
 * simply deploying. The list has four labels and no more: which stage a live
 * deploy is in is the stage rail's job, and `rolling_out` is a machine's word
 * for it either way.
 */
function outcome(d: Deployment, isNewest: boolean): string {
  if (d.status === "failed") return "failed";
  if (!isTerminal(d.status)) return "deploying";
  return isNewest ? "serving" : "superseded";
}

/** The right-hand word carries the status colour — it is the one glance that
 *  says which revision is live. */
const OUTCOME_COLOR: Record<string, string> = {
  running: "text-status-running",
  deploying: "text-status-deploying",
  error: "text-danger",
  stopped: "text-status-stopped",
};

function shortRev(id: string): string {
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

/** How long the deploy took — the number worth comparing against the last one.
 *  In flight there is no answer yet, so callers drop the clause rather than
 *  print a running total the row cannot keep current. */
function elapsed(from: string, to: string | null | undefined): string | null {
  if (!to) return null;
  const ms = Date.parse(to) - Date.parse(from);
  if (!Number.isFinite(ms) || ms < 0) return null;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

/** True while the viewport can hold the panel as a column rather than a sheet.
 *  A media query rather than a CSS breakpoint because the two paths are
 *  different elements — one portals a scrim over the page, the other does not. */
function useColumnLayout(): boolean {
  const [wide, setWide] = useState(() => window.matchMedia(`(min-width: ${COLUMN_AT}px)`).matches);
  useEffect(() => {
    const mq = window.matchMedia(`(min-width: ${COLUMN_AT}px)`);
    const sync = () => setWide(mq.matches);
    mq.addEventListener("change", sync);
    return () => mq.removeEventListener("change", sync);
  }, []);
  return wide;
}

function DeploymentsTab() {
  const { appId } = Route.useParams();
  const { dep } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const qc = useQueryClient();
  const deployments = useListDeployments(appId);
  // The layout above already holds this query, so this is a cache read rather
  // than a second request. It supplies the rollback confirm's title and the
  // branch every row's sub-line names.
  const app = useGetApplication(appId).data;
  const appName = app?.name ?? "this application";
  const branch = app?.source.branch;
  const column = useColumnLayout();

  const deploy = useDeployApplication({
    mutation: {
      onSuccess: (d) => {
        toast.success("Deploy started");
        // The new row is not in the list the page is holding, and the only
        // other thing that would refresh it is the SSE stream — which is
        // exactly what is missing when the stream is reconnecting.
        void qc.invalidateQueries({ queryKey: getListDeploymentsQueryKey(appId) });
        void navigate({ search: { dep: d.id } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Deploy failed to start"),
    },
  });

  const list = deployments.data ?? [];
  // "Serving" is the newest succeeded deployment — everything older that also
  // succeeded has been superseded by it.
  const serving = list.find((d) => d.status === "succeeded");
  const selectedAt = list.findIndex((d) => d.id === dep);
  const selected = selectedAt >= 0 ? list[selectedAt] : undefined;
  // Deploys are numbered from the first one, and the list arrives newest-first.
  // Only sound while the list is whole: a paginated /deployments would have to
  // carry the ordinal itself.
  const ordinal = selectedAt >= 0 ? list.length - selectedAt : undefined;

  const close = useCallback(() => {
    const opener = dep;
    void navigate({ search: {} });
    // The column has no radix dialog to return focus for us (canvas 14g).
    if (opener) document.getElementById(`dep-${opener}`)?.focus();
  }, [dep, navigate]);

  useEffect(() => {
    if (!column || !dep) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      // A modal standing on top of the panel owns Escape first (canvas 14g).
      if (e.target instanceof Element && e.target.closest('[role="dialog"]')) return;
      close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [column, dep, close]);

  return (
    <div className="lg:flex lg:items-stretch">
      <div className="min-w-0 flex-1 lg:pr-8">
        <PageState
          query={deployments}
          empty={
            <EmptyState
              emphasis
              title="No deployments yet"
              hint="Deploy builds the repository's Dockerfile and rolls it out with a health-gated, zero-downtime switch. Push-to-deploy is on the Overview tab."
              action={
                <ActionButton
                  variant="primary"
                  size="lg"
                  state={deploy.isPending ? "busy" : "idle"}
                  busyLabel="Deploying…"
                  onClick={() => deploy.mutate({ id: appId, data: {} })}
                >
                  Deploy now
                </ActionButton>
              }
            />
          }
        >
          {(rows) => (
            <ul>
              {rows.map((d, i) => (
                <DeploymentRow
                  key={d.id}
                  deployment={d}
                  appId={appId}
                  first={i === 0}
                  isNewest={d.id === serving?.id}
                  appName={appName}
                  branch={branch}
                  serving={serving}
                  onOpen={() => void navigate({ search: { dep: d.id } })}
                />
              ))}
            </ul>
          )}
        </PageState>
      </div>

      {column ? (
        <aside
          aria-label="Deployment detail"
          className="-my-6 -mr-8 flex min-h-[560px] w-[460px] flex-none flex-col border-l-[1.5px] border-border-strong bg-toast text-toast-text"
        >
          {dep && (
            <div className="flex items-baseline gap-2.5 px-6 pt-[22px]">
              <PanelHead
                depId={dep}
                rev={selected ? shortRev(selected.revision_id) : undefined}
                ordinal={ordinal}
              />
              <button
                type="button"
                aria-label="Close"
                onClick={close}
                className="ml-auto self-start rounded p-1 text-toast-dismiss hover:text-toast-text"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
          )}
          {dep ? (
            <DeployPanel depId={dep} servingRev={serving?.revision_id} className="min-h-0 flex-1" />
          ) : (
            <PanelIdle />
          )}
        </aside>
      ) : (
        <Drawer
          open={dep !== undefined}
          onOpenChange={(o) => {
            if (!o) close();
          }}
          label="Deployment detail"
          title={
            dep ? (
              <PanelHead depId={dep} rev={selected ? shortRev(selected.revision_id) : undefined} ordinal={ordinal} />
            ) : (
              ""
            )
          }
          tone="ink"
        >
          {dep && <DeployPanel depId={dep} servingRev={serving?.revision_id} className="h-full" />}
        </Drawer>
      )}
    </div>
  );
}

function DeploymentRow({
  deployment: d,
  appId,
  first,
  isNewest,
  appName,
  branch,
  serving,
  onOpen,
}: {
  deployment: Deployment;
  /** The list a rollback's new deployment lands in. */
  appId: string;
  first: boolean;
  isNewest: boolean;
  /** For the rollback confirm's title and its NOW row (canvas 9g). */
  appName: string;
  /** Named in the sub-line: two deploys an hour apart usually differ by branch. */
  branch: string | undefined;
  serving?: Deployment;
  onOpen: () => void;
}) {
  const qc = useQueryClient();
  const rollback = useRollbackDeployment({
    mutation: {
      onSuccess: () => {
        // A rollback is a deploy: it adds a row and moves which revision is
        // serving, and the list holding both was fetched before either was
        // true. Same reasoning as the deploy button above.
        void qc.invalidateQueries({ queryKey: getListDeploymentsQueryKey(appId) });
        toast.success("Rollback started — the previous revision is coming back");
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Rollback failed to start"),
    },
  });

  const status = toStatus(d, isNewest);
  const live = !isTerminal(d.status);
  const took = elapsed(d.created_at, d.finished_at);

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
      <button id={`dep-${d.id}`} type="button" onClick={onOpen} className="min-w-0 flex-1 text-left">
        <span className="flex flex-wrap items-baseline gap-x-2.5">
          <span className="font-mono text-[13px] font-medium">{shortRev(d.revision_id)}</span>
          {d.detail && <span className="min-w-0 truncate text-[13px] text-text-dim">{d.detail}</span>}
        </span>
        <span className="mt-[3px] block text-[11.5px] text-text-faint">
          {d.trigger}
          {branch ? ` · ${branch}` : ""} ·{" "}
          <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
          {took ? ` · ${took}` : ""}
        </span>
      </button>
      <span className="flex shrink-0 items-center gap-3">
        <span
          className={cn(
            "font-mono text-[11px] font-medium uppercase tracking-wide",
            OUTCOME_COLOR[status] ?? "text-text-faint",
          )}
        >
          {outcome(d, isNewest)}
          {status === "deploying" && " ▸"}
        </span>
        {d.status === "succeeded" && !isNewest && (
          // Canvas 9g/13ae: never straight from the row. The confirm is where
          // the two revisions are put side by side and where "env vars don't
          // rewind" is said — both of which are unavailable from a button.
          <ConfirmRollback
            trigger={
              <button
                type="button"
                aria-label={`Roll back to ${shortRev(d.revision_id)}`}
                className="inline-flex items-center gap-1.5 rounded-[5px] border border-border-input bg-surface px-2.5 py-[3px] font-mono text-[11px] font-medium text-text transition-colors hover:border-border-strong"
              >
                rollback <Undo2 className="h-3 w-3" aria-hidden />
              </button>
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

/**
 * Head of the panel in both layouts: the revision, then which deploy it was.
 *
 * The revision is read from the deployment itself, not from the list row: a
 * deploy you have just started is addressed by `?dep=` before any list that
 * would name it has come back, and a head that waits for the list is a blank
 * bar standing over a running log. The list row is still preferred when it is
 * there, so nothing refetches for a revision already on screen. The ordinal is
 * a position *in* the list and can come from nowhere else, so it is simply
 * left unsaid until the row arrives.
 */
function PanelHead({
  depId,
  rev,
  ordinal,
}: {
  depId: string;
  rev: string | undefined;
  ordinal: number | undefined;
}) {
  const dep = useGetDeployment(depId);
  const revision = rev ?? (dep.data ? shortRev(dep.data.revision_id) : undefined);
  return (
    <span className="flex items-baseline gap-2.5">
      {revision ? (
        <span className="font-mono text-[14px] font-medium">{revision}</span>
      ) : (
        <span aria-hidden className="h-3 w-[68px] animate-pulse self-center rounded bg-pane-border" />
      )}
      {ordinal !== undefined && (
        <span className="text-[12px] font-normal text-toast-faint">deploy #{ordinal}</span>
      )}
    </span>
  );
}

/** No selection — the panel says what it is for rather than sitting black and
 *  mute, which reads as a fault rather than as a resting state. */
function PanelIdle() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-8 pb-10 text-center">
      <span aria-hidden className="font-mono text-[26px] leading-none text-toast-dismiss">
        ▸
      </span>
      <p className="mt-3 text-[12.5px] leading-[1.6] text-toast-faint">
        Pick a deployment to watch its stages, read its log and see what it replaced.
      </p>
    </div>
  );
}

function DeployPanel({
  depId,
  servingRev,
  className,
}: {
  depId: string;
  servingRev: string | undefined;
  /** How the panel takes its height: the column stretches it, the sheet fills it. */
  className?: string;
}) {
  const dep = useGetDeployment(depId);
  const frame = cn("flex min-h-0 flex-col px-6 pb-[22px]", className);

  // The panel is the one surface an operator stares at while something is
  // happening, so it owes them the same four states as any page: an empty
  // black rectangle would read as the deploy itself having stalled.
  if (dep.isPending) {
    return (
      <div className={frame} aria-busy>
        <div className="mt-[18px] mb-4 h-2.5 animate-pulse rounded bg-pane-border" />
        <div className="min-h-0 flex-1 animate-pulse rounded-md border border-pane-border bg-pane" />
      </div>
    );
  }

  if (dep.isError) {
    return (
      <div className={frame}>
        <div className="mt-[18px] rounded-md border border-pane-error/40 bg-pane-error/10 px-3 py-2.5 text-[12.5px] text-pane-error">
          <p>This deployment didn&rsquo;t load. It may have been pruned by log retention.</p>
          <button
            type="button"
            onClick={() => void dep.refetch()}
            className="mt-2 rounded border border-pane-border px-2.5 py-1 font-mono text-[11px] text-pane-text hover:border-pane-error hover:text-pane-error"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  const d = dep.data;
  const took = elapsed(d.created_at, d.finished_at);

  return (
    <div className={frame}>
      <PipelineStages status={d.status} detail={d.detail} tone="ink" className="mt-[18px] mb-4" />
      {d.status === "failed" && d.detail && (
        <p className="mb-3 rounded-md border border-pane-error/40 bg-pane-error/10 px-3 py-2.5 text-[13px] text-pane-error">
          {d.detail}
        </p>
      )}
      {/* The caret means "more is coming". cypherd parks on the replay stream
          after a deploy ends, so the transport still reports open — only the
          deployment's own status can answer the question. */}
      <LogViewer url={getStreamDeploymentLogsUrl(d.id)} live={!isTerminal(d.status)} className="min-h-0 flex-1" />
      <p className="mt-3 font-mono text-[11.5px] text-toast-faint">
        {d.trigger} · started <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
        {took ? ` · ${took}` : ""}
      </p>
      {/* Only true while a rollout is in flight — and it is the sentence the
          whole pipeline exists to be able to say. */}
      {!isTerminal(d.status) && servingRev && (
        <p className="mt-1 font-mono text-[11.5px] text-toast-faint">
          zero-downtime: {shortRev(servingRev)} serves until {shortRev(d.revision_id)} is healthy
        </p>
      )}
    </div>
  );
}
