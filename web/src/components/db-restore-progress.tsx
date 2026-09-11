// Restore in flight — design canvas `10d` (dark twin `13an`), wired to the
// restore record the control plane now keeps.
//
// This file used to guess. `POST /databases/{id}/restore` answered 202 with
// nothing in it, the agent reported one terminal event that only reached the
// plane's log, and `Database.status` has no "restoring" value — so the popup
// inferred progress from the database going offline and coming back, and held
// that inference in a module store that a page reload threw away.
//
// None of that is true any more. The 202 carries a `DatabaseRestore`, and the
// record has exactly what the canvas draws: a `status`, the `step` the agent
// has reached (fetching · stopping · applying · restarting), and
// `bytes_done`/`bytes_total` while the dump is going in. So the popup shows
// what is happening rather than a story told over a 202, the outcome is the
// restore's own rather than the container's, and — because the SERVER holds
// the record — a reload no longer forgets: the watch finds a running restore
// by asking, which is what an operator who refreshed mid-restore expects.
//
// Two things the canvas asks for are still deliberately absent:
//
//   · No cancel. Stopping a half-applied restore corrupts the database, so
//     there is no route and no button — and the footer says why rather than
//     leaving the absence to be noticed.
//   · No byte bar where bytes mean nothing. An engine restart is not measured
//     in bytes, and `bytes_total` is zero for those steps; a bar that moved for
//     them would be drawing something nobody counted.
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { useListDatabaseRestores } from "@/api/gen/backups/backups";
import { useGetDatabase } from "@/api/gen/databases/databases";
import { useGetServer } from "@/api/gen/servers/servers";
import type { DatabaseRestore } from "@/api/gen/model";
import { BlockingProgress, type ProgressStep } from "@/components/ui/blocking-progress";
import { toastError, toastSuccess } from "@/lib/toast";

/**
 * The running restore on this database, or undefined. It is a query rather
 * than a module store now: the server is the one that knows, so every tab —
 * and every reload, and a second browser — sees the same answer.
 *
 * Polled while one is running, because a restore's progress is not something
 * the event stream carries. Off again the moment it is not.
 */
export function useRestoreInFlight(dbId: string): DatabaseRestore | undefined {
  const restores = useListDatabaseRestores(dbId, {
    query: {
      // A 501 on a panel with restores disabled is an answer, not a fault, and
      // retrying it three times only delays a null.
      retry: false,
      refetchInterval: (q) => (q.state.data?.some((r) => r.status === "running") ? 2_000 : false),
    },
  });
  return (restores.data ?? []).find((r) => r.status === "running");
}

// ── copy ─────────────────────────────────────────────────────────────────

/** 10d's footer, verbatim: why there is no cancel, and where to look next. */
export const RESTORE_NO_CANCEL =
  "No cancel — stopping a half-applied restore corrupts the database. Closing this popup doesn't stop it; progress continues on the database card.";

/** "212 MB" — whole numbers stay whole, the rest keep one decimal. */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  const s = v >= 100 ? v.toFixed(0) : v.toFixed(1).replace(/\.0$/, "");
  return `${s} ${units[i]}`;
}

/**
 * What a restore is called while it is happening, from the record's own step.
 * Used by the masthead, which has room for a phrase and not for a step list.
 */
export function restoreStepLabel(r: DatabaseRestore): string {
  switch (r.step) {
    case "fetching":
      return "fetching the backup";
    case "stopping":
      return "stopping the container";
    case "applying":
      return r.bytes_total > 0
        ? `applying the dump · ${formatBytes(r.bytes_done)} of ${formatBytes(r.bytes_total)}`
        : "applying the dump";
    case "restarting":
      return "restarting · re-checking health";
    default:
      return "restoring";
  }
}

// The agent's four phases, in the order the canvas draws them. The record
// names which one it is in, so the list is the truth rather than a guess.
const STEPS = [
  { key: "fetching", label: "fetching the backup · verifying the checksum" },
  { key: "stopping", label: "stopping the container · draining connections" },
  { key: "applying", label: "placing the dump" },
  { key: "restarting", label: "restarting · re-checking health" },
] as const;

function restoreSteps(r: DatabaseRestore, serverName: string): { steps: ProgressStep[]; progress: number } {
  const at = STEPS.findIndex((s) => s.key === r.step);
  // An unreported step means the work reached the agent's queue and nothing
  // more has come back — which is a real state at the very start.
  const index = at === -1 ? 0 : at;
  const steps: ProgressStep[] = [
    { label: `accepted · handed to the agent on ${serverName}`, state: "done" },
    ...STEPS.map((s, i) => ({
      label:
        s.key === "applying" && r.bytes_total > 0
          ? `${s.label} · ${formatBytes(r.bytes_done)} of ${formatBytes(r.bytes_total)}`
          : s.label,
      state: (i < index ? "done" : i === index ? "active" : "pending") as ProgressStep["state"],
    })),
  ];
  // Half credit for the step in flight, as the create popup's bar does — with
  // one exception: while the dump is going in and the agent is counting bytes,
  // the bar tracks the bytes, because that is the step that actually takes the
  // time and the only one anyone watches.
  const done = index + (r.step === "applying" && r.bytes_total > 0 ? r.bytes_done / r.bytes_total : 0.5);
  return { steps, progress: (1 + done) / (STEPS.length + 1) };
}

// ── the watch ────────────────────────────────────────────────────────────

/**
 * Mounted once by the database layout. It owns the popup and the exit rule, so
 * every tab under the database shares one restore watch and the masthead reads
 * the same record through `useRestoreInFlight`.
 *
 * The exit is now the restore's own terminal status rather than the container's
 * next status report — so a restore that failed while the container is happily
 * running says "the restore failed", which is the sentence that matters, and a
 * restore that succeeded says so instead of "the database reports running".
 */
export function DatabaseRestoreWatch({ projectId, dbId }: { projectId: string; dbId: string }) {
  const navigate = useNavigate();
  const restores = useListDatabaseRestores(dbId, {
    query: {
      retry: false,
      refetchInterval: (q) => (q.state.data?.some((r) => r.status === "running") ? 2_000 : false),
    },
  });
  const list = restores.data ?? [];
  const inFlight = list.find((r) => r.status === "running");
  const db = useGetDatabase(dbId);
  const serverId = db.data?.server_id ?? "";
  const server = useGetServer(serverId, { query: { enabled: inFlight !== undefined && serverId !== "" } });
  const serverName = server.data?.name ?? "the server";

  // Only a restore this tab actually watched running is worth a toast. A tab
  // opened after the fact would otherwise announce yesterday's restore on
  // mount, which is the one thing a notification must never do.
  const watching = useRef<string | null>(null);
  useEffect(() => {
    if (inFlight) {
      watching.current = inFlight.id;
      return;
    }
    const id = watching.current;
    if (!id) return;
    const finished = list.find((r) => r.id === id);
    if (!finished || finished.status === "running") return;
    watching.current = null;
    const name = db.data?.name ?? "the database";
    if (finished.status === "succeeded") {
      toastSuccess({
        title: `${name} restored`,
        detail: finished.bytes_total > 0 ? `${formatBytes(finished.bytes_total)} applied and the container is back.` : undefined,
      });
      return;
    }
    toastError({
      title: `Restoring ${name} failed`,
      // Never contains secrets, so it is shown as the agent wrote it.
      detail: finished.detail || "The agent reported a failure. The database may hold a partial restore.",
      actions: [
        {
          label: "Open overview",
          onClick: () => void navigate({ to: "/projects/$projectId/databases/$dbId", params: { projectId, dbId } }),
        },
      ],
    });
  }, [inFlight, list, db.data?.name, dbId, projectId, navigate]);

  if (!inFlight) return null;
  const { steps, progress } = restoreSteps(inFlight, serverName);
  return (
    <BlockingProgress
      open
      title={`Restoring ${db.data?.name ?? "the database"}…`}
      detail="The database is offline while the dump goes in."
      steps={steps}
      progress={progress}
      noCancelReason={RESTORE_NO_CANCEL}
    />
  );
}
