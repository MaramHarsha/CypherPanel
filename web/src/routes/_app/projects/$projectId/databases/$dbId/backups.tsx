// Database · Backups: schedules (create/run-now/delete), their run history,
// and restore — the loudest destructive action in the product, gated on a
// blast-radius confirm (ui-principles §2).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { History, Play, Plus, RotateCcw, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getListBackupHistoryQueryKey,
  getListDatabaseBackupsQueryKey,
  useCreateDatabaseBackup,
  useDeleteDatabaseBackup,
  useListBackupHistory,
  useListDatabaseBackups,
  useRestoreDatabase,
  useRunBackupNow,
} from "@/api/gen/backups/backups";
import { useListBackupTargets } from "@/api/gen/backups/backups";
import { useGetDatabase } from "@/api/gen/databases/databases";
import type { BackupRecord, DatabaseBackup } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { CronField } from "@/components/cron-field";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime, absoluteTime } from "@/lib/time";

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
        {hasTargets && <NewScheduleDialog dbId={dbId} />}
      </div>

      <PageState
        query={schedules}
        empty={
          hasTargets ? (
            <EmptyState
              title="No backup schedule"
              hint="Schedule automatic backups to an S3 target, with a retention window. You can also back up on demand once a schedule exists."
              action={<NewScheduleDialog dbId={dbId} primary />}
            />
          ) : (
            <EmptyState
              title="Add a backup target first"
              hint="Backups need somewhere to go. Add an S3-compatible target in Settings → Backup targets, then schedule backups here."
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

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
    void qc.invalidateQueries({ queryKey: getListBackupHistoryQueryKey(dbId, schedule.id) });
  };

  const runNow = useRunBackupNow({
    mutation: {
      onSuccess: () => {
        invalidate();
        toast.success("Backup started");
      },
      onError: mutErr,
    },
  });
  const del = useDeleteDatabaseBackup({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
        toast.success("Schedule removed");
      },
      onError: mutErr,
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
          <Button size="sm" variant="secondary" disabled={runNow.isPending} onClick={() => runNow.mutate({ id: dbId, bakId: schedule.id })}>
            <Play className="h-3.5 w-3.5" /> Back up now
          </Button>
          <Button size="sm" variant="ghost" aria-pressed={showHistory} onClick={() => setShowHistory((v) => !v)}>
            <History className="h-3.5 w-3.5" /> History
          </Button>
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
  return (
    <div className="border-t border-border">
      <PageState
        query={history}
        loading={<div className="px-4 py-3 text-xs text-text-faint">Loading history…</div>}
        empty={<div className="px-4 py-3 text-xs text-text-faint">No backups have run yet.</div>}
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
  const restore = useRestoreDatabase({
    mutation: {
      // The panel gets no progress from a restore — the agent reports one
      // terminal event to the control plane and nothing to the browser — so
      // the toast has to carry the whole consequence up front: the database
      // is down while the dump applies, and nobody can call it back.
      onSuccess: () =>
        toast.success(`Restoring ${dbName}…`, {
          description: "The database is offline while the dump is applied, and the restore can't be stopped.",
        }),
      onError: mutErr,
    },
  });
  const succeeded = r.status === "succeeded";
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2">
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
          onConfirm={() => restore.mutate({ id: dbId, data: { backup_record_id: r.id, confirm: true } })}
        />
      )}
    </li>
  );
}

function NewScheduleDialog({ dbId, primary }: { dbId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const targets = useListBackupTargets();
  const [targetId, setTargetId] = useState("");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [retention, setRetention] = useState("7");
  const [error, setError] = useState<string | null>(null);

  const chosen = targetId || targets.data?.[0]?.id || "";

  const create = useCreateDatabaseBackup({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDatabaseBackupsQueryKey(dbId) });
        toast.success("Backup schedule created");
        setError(null);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the schedule"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate({ id: dbId, data: { target_id: chosen, schedule, retention_count: Number(retention), enabled: true } });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New schedule
        </Button>
      </DialogTrigger>
      <DialogContent title="Schedule backups" description="CypherPanel runs the backup on this schedule and uploads it to your S3 target.">
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
          <Field label="Keep" hint="How many recent backups to retain. Older ones are pruned automatically.">
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
            <Button type="submit" variant="primary" disabled={create.isPending || chosen === ""}>
              {create.isPending ? "Creating…" : "Create schedule"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

function mutErr(e: unknown) {
  toast.error(e instanceof Error ? e.message : "Something went wrong");
}
