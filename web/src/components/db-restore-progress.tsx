// Restore in flight — design canvas `10d` (dark twin `13an`), wired to what the
// control plane can actually see.
//
// POST /databases/{id}/restore answers 202 with nothing in it: the plane hands
// the work to the agent, the agent reports one terminal event back to the
// plane, and the plane only logs it. `Database.status` has no "restoring"
// value. So the popup this file drives shows the plane's own reading of the
// record and nothing else — the same rule db-provisioning-steps.tsx follows:
//
//   1. accepted — the 202 means the work reached the agent's queue;
//   2. fetching + applying — the agent's phase, which it does not report;
//   3. the agent's next status report — the one signal the browser will get.
//
// The popup closes on that report. `running` hands the operator back with a
// toast that says exactly what was observed (the database *reports running* —
// not "restored"); `error` becomes a persistent error toast carrying the
// agent's own words; a `stopped`/`provisioning` report means the container
// went down for the restore (the Redis/Valkey path), so the popup waits for
// the healthy report that follows.
//
// The in-flight record lives in a module store rather than in a query: the
// server does not hold it (that is the gap), so a reload forgets it — which is
// honest, because after a reload the panel genuinely cannot tell.
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useSyncExternalStore } from "react";
import { useGetDatabase } from "@/api/gen/databases/databases";
import { useGetServer } from "@/api/gen/servers/servers";
import type { BackupRecord } from "@/api/gen/model";
import { BlockingProgress, type ProgressStep } from "@/components/ui/blocking-progress";
import { toastError, toastSuccess } from "@/lib/toast";

export type RestoreSource = Pick<BackupRecord, "id" | "started_at" | "size_bytes">;

export interface RestoreInFlight {
  dbId: string;
  /** The record being applied — "From today 04:00 (212 MB)". */
  backup: RestoreSource;
  /** Client clock at the 202. The first fetch completed after it sets `baseline`. */
  acceptedAt: number;
  /**
   * `updated_at` of the record as first fetched after the 202. Every agent
   * report bumps it (SetDatabaseObservedStatus writes `updated_at = now()`),
   * so a later value is a report made since the restore was handed over.
   */
  baseline: string | null;
  /** The agent has reported the database off since the restore began. */
  sawOffline: boolean;
}

// ── store ────────────────────────────────────────────────────────────────

let entries: ReadonlyMap<string, RestoreInFlight> = new Map();
const listeners = new Set<() => void>();

function write(next: ReadonlyMap<string, RestoreInFlight>) {
  entries = next;
  listeners.forEach((l) => l());
}

function patch(dbId: string, changes: Partial<RestoreInFlight>) {
  const current = entries.get(dbId);
  if (!current) return;
  const next = new Map(entries);
  next.set(dbId, { ...current, ...changes });
  write(next);
}

function end(dbId: string) {
  if (!entries.has(dbId)) return;
  const next = new Map(entries);
  next.delete(dbId);
  write(next);
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Call on the restore's 202. `DatabaseRestoreWatch` opens the popup from it. */
export function startRestoreWatch(dbId: string, backup: RestoreSource) {
  const next = new Map(entries);
  next.set(dbId, {
    dbId,
    backup: { id: backup.id, started_at: backup.started_at, size_bytes: backup.size_bytes },
    acceptedAt: Date.now(),
    baseline: null,
    sawOffline: false,
  });
  write(next);
}

/** The restore this tab started on `dbId`, while the popup is still owed a report. */
export function useRestoreInFlight(dbId: string): RestoreInFlight | undefined {
  return useSyncExternalStore(subscribe, () => entries.get(dbId));
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
 * "today 04:00", "yesterday 04:00", "3 Sep 04:00" — the way 10d names the
 * backup. The viewer's clock, because "today" is a word about the reader's
 * day; the row it came from still carries the absolute UTC stamp on hover.
 */
export function backupMoment(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "an earlier backup";
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const dayOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((dayOf(new Date()) - dayOf(d)) / 86_400_000);
  if (days === 0) return `today ${time}`;
  if (days === 1) return `yesterday ${time}`;
  return `${d.toLocaleDateString(undefined, { day: "numeric", month: "short" })} ${time}`;
}

/** "today 04:00 (212 MB)" — the popup's second line and the masthead's. */
export function restoreSourceLabel(b: RestoreSource): string {
  const when = backupMoment(b.started_at);
  return b.size_bytes > 0 ? `${when} (${formatBytes(b.size_bytes)})` : when;
}

/**
 * The plane-side steps. The canvas draws the agent's four (stop + drain,
 * fetch + checksum, apply with bytes, restart + health); none of them reach
 * the plane, so those would be a story told over a 202. These three are true.
 * The bar is half credit for the step in flight, as the create popup's is —
 * it tracks the list and stops where the work stops.
 */
function restoreSteps(serverName: string, sawOffline: boolean): { steps: ProgressStep[]; progress: number } {
  const accepted: ProgressStep = { label: `accepted · handed to the agent on ${serverName}`, state: "done" };
  if (sawOffline) {
    return {
      steps: [
        accepted,
        { label: "fetched the backup · container stopped for the restore", state: "done" },
        { label: "placing the dump · restarting · re-checking health", state: "active" },
      ],
      progress: 2.5 / 3,
    };
  }
  return {
    steps: [
      accepted,
      { label: "fetching the backup · applying the dump", state: "active" },
      { label: "the agent's next status report", state: "pending" },
    ],
    progress: 1.5 / 3,
  };
}

// ── the watch ────────────────────────────────────────────────────────────

/**
 * Mounted once by the database layout. Owns the polling, the exit rule and
 * the popup, so every tab under the database shares one restore watch and the
 * masthead can read the same record through `useRestoreInFlight`.
 */
export function DatabaseRestoreWatch({ projectId, dbId }: { projectId: string; dbId: string }) {
  const inFlight = useRestoreInFlight(dbId);
  const navigate = useNavigate();
  // Every agent report also arrives as an SSE invalidation, but the popup's
  // exit is exactly one report and the stream's reconnect fallback polls at
  // 5 s — so while a restore is owed a report, ask every 2 s regardless. The
  // options object is built conditionally: an explicit `undefined` would
  // override the QueryClient's fallback poll rather than inherit it.
  const db = useGetDatabase(dbId, inFlight ? { query: { refetchInterval: 2_000 } } : undefined);
  const serverId = db.data?.server_id ?? "";
  const server = useGetServer(serverId, { query: { enabled: inFlight !== undefined && serverId !== "" } });
  const serverName = server.data?.name ?? "the server";

  useEffect(() => {
    if (!inFlight || !db.data) return;
    const d = db.data;

    if (inFlight.baseline === null) {
      // Only a fetch that completed after the 202 can be the baseline: the
      // cached record may predate a report the agent made just before the
      // click, and counting that one as "since the restore" would close the
      // popup on a status that says nothing about it.
      if (db.dataUpdatedAt > inFlight.acceptedAt) patch(dbId, { baseline: d.updated_at });
      return;
    }
    if (Date.parse(d.updated_at) <= Date.parse(inFlight.baseline)) return;

    const source = restoreSourceLabel(inFlight.backup);
    if (d.status === "error") {
      end(dbId);
      toastError({
        title: `${d.name} is in error after the restore`,
        detail: d.status_detail || "The agent reported an error — the restore may not have applied.",
        actions: [
          {
            label: "Open overview",
            onClick: () => void navigate({ to: "/projects/$projectId/databases/$dbId", params: { projectId, dbId } }),
          },
        ],
      });
      return;
    }
    if (d.status === "running") {
      end(dbId);
      // What was observed, in those words. The agent reports the restore's
      // own outcome to the control plane's log and nowhere the panel can
      // read, so "running" is the most this toast may claim.
      toastSuccess(
        inFlight.sawOffline
          ? {
              title: `${d.name} is back — restarted and healthy`,
              detail: `The container came back after the restore from ${source}. The agent logs the restore's own outcome on the control plane only.`,
            }
          : {
              title: `${d.name} reports running`,
              detail: `Restore from ${source} handed to the agent. It reports the restore's outcome to the control plane only, so check the data before relying on it.`,
            },
      );
      return;
    }
    // stopped / provisioning / unknown: the container is down for the restore.
    if (!inFlight.sawOffline) patch(dbId, { sawOffline: true });
  }, [inFlight, db.data, db.dataUpdatedAt, dbId, projectId, navigate]);

  if (!inFlight || !db.data) return null;
  const { steps, progress } = restoreSteps(serverName, inFlight.sawOffline);
  return (
    <BlockingProgress
      open
      title={`Restoring ${db.data.name}…`}
      detail={`From ${restoreSourceLabel(inFlight.backup)}. The database is offline during the restore.`}
      steps={steps}
      progress={progress}
      noCancelReason={RESTORE_NO_CANCEL}
    />
  );
}
