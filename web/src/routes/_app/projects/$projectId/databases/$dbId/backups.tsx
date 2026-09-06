// Database · Backups: schedules (create/run-now/delete), their run history,
// and restore — the loudest destructive action in the product, gated on a
// blast-radius confirm (ui-principles §2) and followed by the blocking popup
// of canvas 10d.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { History, Play, Plus, RotateCcw, Trash2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import {
  getListBackupHistoryQueryKey,
  getListDatabaseBackupsQueryKey,
  useCreateDatabaseBackup,
  useDeleteDatabaseBackup,
  usePatchDatabaseBackup,
  useListBackupHistory,
  useListDatabaseBackups,
  useRestoreDatabase,
  useRunBackupNow,
} from "@/api/gen/backups/backups";
import { useListBackupTargets } from "@/api/gen/backups/backups";
import { getGetDatabaseQueryKey, useGetDatabase } from "@/api/gen/databases/databases";
import type { BackupRecord, DatabaseBackup } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { formatBytes, startRestoreWatch } from "@/components/db-restore-progress";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { CronField } from "@/components/cron-field";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SkeletonRows, useSkeletonDelay } from "@/components/ui/skeleton";
import { relativeTime, absoluteTime } from "@/lib/time";
import { toastError, toastFailed, toastSuccess, toastWorking, type ToastId } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/backups")({
  component: BackupsTab,
});

function BackupsTab() {
  const { dbId } = Route.useParams();
  const schedules = useListDatabaseBackups(dbId);
  const targets = useListBackupTargets();
  // Restore is a typed-name confirm, and the name it asks for is the
  // database's own — so the history rows need it. The layout above already
  // holds this query, so reading it here costs a cache hit, not a request.
  const db = useGetDatabase(dbId);

  const hasTargets = (targets.data ?? []).length > 0;
  const dbName = db.data?.name ?? "";

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <Eyebrow>Backup schedules</Eyebrow>
        {hasTargets && <ScheduleDialog dbId={dbId} />}
      </div>

      <PageState
        query={schedules}
        // A schedule card is one row: the cron and its meta on the left, the
        // three controls on the right.
        skeletonColumns="1fr auto"
        skeletonRows={2}
        empty={
          hasTargets ? (
            <EmptyState
              title="No backup schedule"
              hint="Schedule automatic backups to an S3 target, with a retention window. You can also back up on demand once a schedule exists."
              action={<ScheduleDialog dbId={dbId} primary />}
            />
          ) : (
            <EmptyState
              title="Add a backup target first"
              hint="Backups need somewhere to go. Add an S3-compatible target in Settings → Backup targets, then schedule backups here."
              // The prerequisite lives on another page, so the verb is the
              // way there (15a): a sentence naming a tab is not a next step.
              action={
                <Link to="/settings/backup-targets">
                  <Button variant="primary">Add a backup target</Button>
                </Link>
              }
            />
          )
        }
      >
        {(list) => (
          <div className="space-y-4">
            {list.map((s) => (
              <ScheduleCard key={s.id} dbId={dbId} dbName={dbName} schedule={s} />
            ))}
          </div>
        )}
      </PageState>
    </div>
  );
}

function ScheduleCard({ dbId, dbName, schedule }: { dbId: string; dbName: string; schedule: DatabaseBackup }) {
  const qc = useQueryClient();
  const [showHistory, setShowHistory] = useState(false);
  // The run "Back up now" started, while its working toast is up (10c).
  const [running, setRunning] = useState<{ recordId: string; toastId: ToastId } | null>(null);

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
    void qc.invalidateQueries({ queryKey: getListBackupHistoryQueryKey(dbId, schedule.id) });
  };

  const runNow = useRunBackupNow({
    mutation: {
      onSuccess: (res) => {
        invalidate();
        // The 202 carries the record it created, in `running`; the agent's
        // terminal event flips it to succeeded/failed. A working toast is the
        // honest shape for that — it morphs in place when the record does.
        setRunning({ recordId: res.record.id, toastId: toastWorking(`Backing up ${dbName || "the database"}…`) });
      },
      onError: (e: unknown, vars) => toastFailed("Backup didn't start", e, { retry: () => runNow.mutate(vars) }),
    },
  });
  const runState = useMutationActionState(runNow);

  // A second observer on the history, polling only while a run is owed an
  // outcome: SSE has no event for backup records, so the fallback poll is the
  // only way the toast finds out.
  const watched = useListBackupHistory(dbId, schedule.id, {
    query: { enabled: running !== null, refetchInterval: 2_000 },
  });
  const { mutate: runAgain } = runNow;
  useEffect(() => {
    if (!running) return;
    const rec = watched.data?.find((r) => r.id === running.recordId);
    if (!rec || rec.status === "running") return;
    setRunning(null);
    // The schedule's own last-run line and the open history both change.
    void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
    void qc.invalidateQueries({ queryKey: getListBackupHistoryQueryKey(dbId, schedule.id) });
    if (rec.status === "succeeded") {
      toastSuccess(
        {
          title: `Backed up ${dbName || "the database"}`,
          detail: rec.size_bytes > 0 ? `${formatBytes(rec.size_bytes)} uploaded to the target.` : undefined,
        },
        running.toastId,
      );
    } else {
      toastError(
        {
          title: `Backup of ${dbName || "the database"} failed`,
          detail: rec.detail || "The agent reported a failure.",
          actions: [{ label: "Retry", onClick: () => runAgain({ id: dbId, bakId: schedule.id }) }],
        },
        running.toastId,
      );
    }
  }, [running, watched.data, qc, runAgain, dbId, dbName, schedule.id]);

  const del = useDeleteDatabaseBackup({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
        toastSuccess("Schedule removed");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the schedule", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <div className="rounded-lg border border-border bg-surface">
      <div className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="mono text-[13px] text-text">{schedule.schedule}</span>
            {!schedule.enabled && <span className="mono text-[11px] text-text-faint">paused</span>}
          </div>
          <p className="mono text-xs text-text-faint">
            keep {schedule.retention_count} · last run{" "}
            {schedule.last_run_at ? (
              <span title={absoluteTime(schedule.last_run_at)}>
                {relativeTime(schedule.last_run_at)}
                {schedule.last_status ? ` (${schedule.last_status})` : ""}
              </span>
            ) : (
              "never"
            )}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <ActionButton
            size="sm"
            variant="secondary"
            state={runState}
            busyLabel="Starting…"
            successLabel="Started"
            disabledReason={running ? "A backup is already running" : undefined}
            onClick={() => runNow.mutate({ id: dbId, bakId: schedule.id })}
          >
            <Play className="h-3.5 w-3.5" /> Back up now
          </ActionButton>
          <Button size="sm" variant="ghost" aria-pressed={showHistory} onClick={() => setShowHistory((v) => !v)}>
            <History className="h-3.5 w-3.5" /> History
          </Button>
          {/* Pausing keeps the schedule, its target and its history and stops
              the next tick — which is what an operator wants during a
              migration, and is not what deleting does. */}
          <PauseSchedule dbId={dbId} schedule={schedule} />
          <ScheduleDialog dbId={dbId} schedule={schedule} />
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" aria-label="Delete schedule">
                <Trash2 className="h-3.5 w-3.5 text-danger" />
              </Button>
            }
            title="Delete this backup schedule?"
            blastRadius="Stops future automatic backups on this schedule. Backup files already in your S3 target are not deleted."
            actionLabel="Delete schedule"
            pending={del.isPending}
            pendingLabel="Deleting…"
            onConfirm={() => del.mutate({ id: dbId, bakId: schedule.id })}
          />
        </div>
      </div>
      {showHistory && <HistoryList dbId={dbId} dbName={dbName} bakId={schedule.id} />}
    </div>
  );
}

function HistoryList({ dbId, dbName, bakId }: { dbId: string; dbName: string; bakId: string }) {
  const history = useListBackupHistory(dbId, bakId);
  // The shape of a history row — status, stamp · size, the restore control —
  // and only past 200 ms (canvas 10e), not a sentence that flashes.
  const showSkeleton = useSkeletonDelay(history.isPending);
  return (
    <div className="border-t border-border px-4">
      <PageState
        query={history}
        loading={showSkeleton ? <SkeletonRows columns="auto 1fr auto" rows={2} /> : null}
        empty={
          // Nested evidence, not a page: no glyph, and the verb it points at
          // — "Back up now" — is already on the card head above it.
          <EmptyState
            glyph={null}
            className="py-3"
            title="No backups have run yet"
            hint="Back up now runs one immediately; the schedule takes care of the rest."
          />
        }
      >
        {(records) => (
          <ul className="divide-y divide-border">
            {records.map((r) => (
              <RecordRow key={r.id} dbId={dbId} dbName={dbName} record={r} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function RecordRow({ dbId, dbName, record: r }: { dbId: string; dbName: string; record: BackupRecord }) {
  const qc = useQueryClient();
  const restore = useRestoreDatabase({
    mutation: {
      // The 202 is the whole answer the panel gets: the agent reports one
      // terminal event to the control plane and nothing to the browser. The
      // layout's restore watch turns that into 10d's popup — the source
      // backup, the offline consequence, why there is no cancel — and holds
      // it until the agent's next status report says where the database is.
      onSuccess: () => {
        startRestoreWatch(dbId, r);
        // The container may restart under the dump, so the status the
        // overview is holding is stale the instant this returns. Ask for it
        // again rather than trusting the event stream to say so — it may be
        // reconnecting, and a database shown as running while it is offline
        // is exactly the state we must not display.
        void qc.invalidateQueries({ queryKey: getGetDatabaseQueryKey(dbId) });
      },
      onError: (e: unknown, vars) => toastFailed("Restore didn't start", e, { retry: () => restore.mutate(vars) }),
    },
  });
  const succeeded = r.status === "succeeded";
  return (
    <li className="flex items-center justify-between gap-3 py-2">
      <span className="flex min-w-0 items-center gap-2">
        <StatusBadge status={succeeded ? "running" : r.status === "failed" ? "error" : "deploying"} />
        <span className="mono text-xs text-text-faint" title={absoluteTime(r.started_at)}>
          {relativeTime(r.started_at)}
          {r.size_bytes > 0 ? ` · ${formatBytes(r.size_bytes)}` : ""}
          {r.detail ? ` · ${r.detail}` : ""}
        </span>
      </span>
      {/* Held back until the database's name is in hand: the typed-name
          confirm is what makes the operator's fingers name the target, and a
          confirm with nothing to type arms itself on open. */}
      {succeeded && dbName !== "" && (
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost">
              <RotateCcw className="h-3.5 w-3.5" /> Restore
            </Button>
          }
          title="Restore from this backup?"
          lead="Restoring replaces this database:"
          blastRadius={[
            "every row written since this backup — permanently lost",
            "the database restarts and is offline during the restore",
            "the backup file in S3 is not modified",
          ]}
          confirmName={dbName}
          actionLabel="Restore database"
          pending={restore.isPending}
          pendingLabel="Restoring…"
          onConfirm={() => restore.mutate({ id: dbId, data: { backup_record_id: r.id, confirm: true } })}
        />
      )}
    </li>
  );
}

/** Pause and resume, as the one-field patch it is. */
function PauseSchedule({ dbId, schedule }: { dbId: string; schedule: DatabaseBackup }) {
  const qc = useQueryClient();
  const patch = usePatchDatabaseBackup({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
        toastSuccess(schedule.enabled ? "Schedule paused" : "Schedule resumed");
      },
      onError: (e: unknown) => toastFailed("Could not change the schedule", e),
    },
  });
  return (
    <ActionButton
      size="sm"
      variant="ghost"
      state={patch.isPending ? "busy" : "idle"}
      busyLabel={schedule.enabled ? "Pausing…" : "Resuming…"}
      onClick={() => patch.mutate({ id: dbId, bakId: schedule.id, data: { enabled: !schedule.enabled } })}
    >
      {schedule.enabled ? "Pause" : "Resume"}
    </ActionButton>
  );
}

/**
 * New schedule, or edit one. Nothing here is sealed — a target, a cron
 * expression and a retention count — so the edit form is simply the create form
 * seeded, and every field is safe to show as it is stored.
 *
 * Changing the retention count downward prunes on the NEXT run rather than
 * immediately, which is worth saying: "keep 3" typed on a database with ten
 * backups does not delete seven files the moment you press Save.
 */
function ScheduleDialog({ dbId, schedule: existing, primary }: { dbId: string; schedule?: DatabaseBackup; primary?: boolean }) {
  const editing = existing !== undefined;
  const qc = useQueryClient();
  const targets = useListBackupTargets();
  const [open, setOpen] = useState(false);
  const [targetId, setTargetId] = useState(existing?.target_id ?? "");
  const [schedule, setSchedule] = useState(existing?.schedule ?? "0 3 * * *");
  const [retention, setRetention] = useState(String(existing?.retention_count ?? 7));
  const [error, setError] = useState<string | null>(null);

  const chosen = targetId || targets.data?.[0]?.id || "";

  const done = (verb: string) => {
    void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
    toastSuccess(verb);
    setError(null);
    // The card it made is the thing to look at now, not the form again.
    setOpen(false);
  };

  const create = useCreateDatabaseBackup({
    mutation: {
      onSuccess: () => done("Backup schedule created"),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the schedule"),
    },
  });
  const patch = usePatchDatabaseBackup({
    mutation: {
      onSuccess: () => done("Backup schedule saved"),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the schedule"),
    },
  });
  const createState = useMutationActionState(editing ? patch : create);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (chosen === "" || create.isPending || patch.isPending) return;
    const data = { target_id: chosen, schedule, retention_count: Number(retention) };
    if (editing) {
      // `enabled` is deliberately absent: pausing lives on the card, and an
      // edit must not silently resume a schedule someone paused.
      patch.mutate({ id: dbId, bakId: existing.id, data });
      return;
    }
    create.mutate({ id: dbId, data: { ...data, enabled: true } });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {editing ? (
          <Button size="sm" variant="ghost">
            Edit
          </Button>
        ) : (
          <Button variant="primary" size={primary ? "lg" : "md"}>
            <Plus className="h-3.5 w-3.5" /> New schedule
          </Button>
        )}
      </DialogTrigger>
      <DialogContent
        title={editing ? "Edit this schedule" : "Schedule backups"}
        description="CypherPanel runs the backup on this schedule and uploads it to your S3 target."
      >
        <form onSubmit={submit} className="space-y-4">
          <Field label="Target">
            {(id) => (
              <select
                id={id}
                value={chosen}
                onChange={(e) => setTargetId(e.target.value)}
                className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text"
              >
                {(targets.data ?? []).map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <div>
            <p className="mb-1 text-[13px] font-medium text-text">Schedule</p>
            <InlineHint>When to run, as a cron expression. The default is every day at 3am.</InlineHint>
            <div className="mt-1.5">
              <CronField value={schedule} onChange={setSchedule} />
            </div>
          </div>
          <Field
            label="Keep"
            hint={
              editing
                ? "How many recent backups to retain. Lowering it prunes on the next run, not now."
                : "How many recent backups to retain. Older ones are pruned automatically."
            }
          >
            {(id) => <Input id={id} inputMode="numeric" value={retention} onChange={(e) => setRetention(e.target.value)} className="mono w-24" />}
          </Field>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              state={createState}
              busyLabel={editing ? "Saving…" : "Creating…"}
              successLabel={editing ? "Saved" : "Created"}
              disabledReason={chosen === "" ? "Add a backup target first" : undefined}
            >
              {editing ? "Save schedule" : "Create schedule"}
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
