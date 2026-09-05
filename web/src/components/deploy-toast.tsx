// The deploy toast — canvas 10c's "working" arc, applied to the one job in the
// panel that outlives its own request. POST /deploy and POST /rollback answer
// 202 in milliseconds; the pipeline resolves minutes later. A success toast on
// the 202 ("Deploy started") says the wrong thing at the wrong time, and a
// second toast when the rollout ends reads as two events. So the toast is a
// WORKING toast that watches the deployment and morphs in place — "Deploying
// a81f3e0 · rolling out…" becomes "Deployed a81f3e0" or "Deploy a81f3e0
// failed", the way 10c draws it and the way canvas 14a asks the prototype's
// golden journey to feel.
//
// The watcher lives INSIDE the toast. Its title is a component that subscribes
// to the deployment through the generated hook — the Toaster sits under the
// QueryClientProvider and the router — so the toast keeps resolving after the
// operator has navigated away from the page that started the deploy. A toast
// that could only resolve while one tab stayed mounted would hang for exactly
// the person who went to look at something else, which is most of them.
import { useRouter } from "@tanstack/react-router";
import { useEffect } from "react";
import { useGetDeployment } from "@/api/gen/deployments/deployments";
import type { Deployment } from "@/api/gen/model";
import { failedStage, stageWord } from "@/components/pipeline-stages";
import { toastError, toastSuccess, toastWorking } from "@/lib/toast";

export interface DeployToastOptions {
  /** A rollback is a deploy with a different sentence: it names the revision it returns to. */
  kind: "deploy" | "rollback";
  /** Where "Open log" leads — the Deployments tab with this deployment's panel open. */
  projectId: string;
  appId: string;
}

function isTerminal(status: string): boolean {
  return status === "succeeded" || status === "failed";
}

function shortRev(id: string): string {
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function took(from: string, to: string | null | undefined): string | null {
  if (!to) return null;
  const ms = Date.parse(to) - Date.parse(from);
  if (!Number.isFinite(ms) || ms < 0) return null;
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

/**
 * Show the working toast for a deployment the plane has just accepted, and
 * let it resolve itself. One toast per deployment — calling this twice for the
 * same id updates the toast in place rather than stacking a second.
 */
export function toastDeployment(dep: Deployment, opts: DeployToastOptions): void {
  const id = `deployment-${dep.id}`;
  toastWorking({ title: <DeploymentWatch depId={dep.id} toastId={id} rev={shortRev(dep.revision_id)} {...opts} /> }, id);
}

function DeploymentWatch({
  depId,
  toastId,
  rev,
  kind,
  projectId,
  appId,
}: DeployToastOptions & { depId: string; toastId: string; rev: string }) {
  const router = useRouter();
  const dep = useGetDeployment(depId, {
    query: {
      // Polled while live, whatever the events stream is doing: that stream
      // invalidates the application and its lists, not a deployment's own key,
      // so without this the watcher would learn the outcome only from the
      // stream-down fallback poll — which is to say, usually never.
      refetchInterval: (q) => (q.state.data && isTerminal(q.state.data.status) ? false : 3_000),
    },
  });

  const d = dep.data;
  const status = d?.status;
  const detail = d?.detail;
  const finishedAt = d?.finished_at;
  const createdAt = d?.created_at;
  const lost = dep.isError;

  useEffect(() => {
    const openLog = {
      label: "Open log",
      onClick: () =>
        void router.navigate({
          to: "/projects/$projectId/applications/$appId/deployments",
          params: { projectId, appId },
          search: { dep: depId },
        }),
    };
    if (lost) {
      // The record went away under us (retention, a deleted app). A working
      // toast that can never resolve would be a lie about the deploy, so it
      // becomes an error that says exactly what is known.
      toastError(
        { title: "Lost track of this deploy", detail: "Its record is gone — the Deployments tab has what remains.", actions: [openLog] },
        toastId,
      );
      return;
    }
    if (!status || !isTerminal(status) || !createdAt) return;
    if (status === "succeeded") {
      const t = took(createdAt, finishedAt);
      toastSuccess(
        kind === "rollback"
          ? { title: `Rolled back to ${rev}`, detail: "no rebuild · env vars unchanged" }
          : { title: `Deployed ${rev}`, detail: t ? `serving · ${t}` : "serving" },
        toastId,
      );
      return;
    }
    // The deployment's own sentence first; the stage it died in when it has none.
    toastError(
      {
        title: kind === "rollback" ? `Rollback to ${rev} failed` : `Deploy ${rev} failed`,
        detail: detail || `Failed at ${failedStage(detail)}`,
        actions: [openLog],
      },
      toastId,
    );
  }, [lost, status, detail, finishedAt, createdAt, kind, rev, toastId, depId, projectId, appId, router]);

  const word = status ? stageWord(status) : "";
  return kind === "rollback" ? (
    <>
      Rolling back to {rev}
      {word ? ` · ${word}` : ""}…
    </>
  ) : (
    <>
      Deploying {rev}
      {word ? ` · ${word}` : ""}…
    </>
  );
}
