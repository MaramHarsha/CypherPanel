// Application · Storage → volume backups (volume-backups.md, canvas 13g's
// "in backups ✓" chip made real).
//
// What this is NOT, said first because it is the part that gets someone hurt:
// a volume archive is a tar of a live directory, not a snapshot. A process
// writing while the tar runs produces an archive of a half-written file. That
// is fine for uploads, generated assets and caches, and it is not fine for a
// database — which is why managed databases keep their own engine-level dumps
// and this card says so instead of implying parity.
//
// One schedule per application, covering every volume flagged `backed_up`.
// Two cadences for two directories of the same application is two
// applications; splitting the schedule per volume would have made "when does
// this app get backed up" a question with N answers.
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { History, Play, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  getGetVolumeBackupQueryKey,
  getListVolumeBackupRecordsQueryKey,
  useDeleteVolumeBackup,
  useGetVolumeBackup,
  useListVolumeBackupRecords,
  useRunVolumeBackup,
  useSetVolumeBackup,
} from "@/api/gen/applications/applications";
import { useGetMe } from "@/api/gen/auth/auth";
import { useListBackupTargets } from "@/api/gen/backups/backups";
import type { AppVolume, VolumeBackup, VolumeBackupRecord } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CronField } from "@/components/cron-field";
import { formatBytes } from "@/components/db-restore-progress";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { SkeletonRows, useSkeletonDelay } from "@/components/ui/skeleton";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export function VolumeBackupCard({ appId, volumes }: { appId: string; volumes: AppVolume[] }) {
  const schedule = useGetVolumeBackup(appId);
  const targets = useListBackupTargets();
  const flagged = volumes.filter((v) => v.backed_up);

  // An application with no volumes at all is not being told about a feature
  // that cannot apply to it — the volume list above already has the verb.
  if (volumes.length === 0) return null;

  const hasTargets = (targets.data ?? []).length > 0;
  const current = schedule.data ?? null;

  return (
    <section className="space-y-2.5 pt-2">
      <div className="flex items-center gap-3">
        <Eyebrow>Volume backups</Eyebrow>
        {current && hasTargets && (
          <span className="ml-auto">
            <ScheduleDialog appId={appId} schedule={current} />
          </span>
        )}
      </div>

      {!hasTargets ? (
        <div className="rounded-lg border border-border bg-surface">
          <EmptyState
            glyph={null}
            className="py-5"
            title="Add a backup target first"
            hint="Archives need somewhere to go. Add an S3-compatible target, then schedule volume backups here."
            action={
              <Link to="/settings/backup-targets">
                <Button variant="primary">Add a backup target</Button>
              </Link>
            }
          />
        </div>
      ) : !current ? (
        <div className="rounded-lg border border-border bg-surface">
          <EmptyState
            glyph={null}
            className="py-5"
            title="No volume backup schedule"
            hint="Archive the volumes marked “in backups” to an S3 target on a cadence, keeping the last few. Uploads and generated assets, not databases — a tar of a live directory is a copy, not a snapshot."
            action={<ScheduleDialog appId={appId} primary />}
          />
        </div>
      ) : (
        <ScheduleBody appId={appId} schedule={current} flagged={flagged} />
      )}
    </section>
  );
}

function ScheduleBody({
  appId,
  schedule,
  flagged,
}: {
  appId: string;
  schedule: VolumeBackup;
  flagged: AppVolume[];
}) {
  const qc = useQueryClient();
  const [showHistory, setShowHistory] = useState(false);

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: getGetVolumeBackupQueryKey(appId) });
    void qc.invalidateQueries({ queryKey: getListVolumeBackupRecordsQueryKey(appId) });
  };

  const run = useRunVolumeBackup({
    mutation: {
      onSuccess: (recs) => {
        invalidate();
        setShowHistory(true);
        toastSuccess({
          title: recs.length === 1 ? "Archiving 1 volume" : `Archiving ${recs.length} volumes`,
          detail: "The agent reports each one as it finishes — the history below updates in place.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Backup didn't start", e, { retry: () => run.mutate(vars) }),
    },
  });
  const runState = useMutationActionState(run);

  const del = useDeleteVolumeBackup({
    mutation: {
      onSuccess: () => {
        invalidate();
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
            <span className="mono text-[13px] text-text">{schedule.schedule || "manual only"}</span>
            {!schedule.enabled && <span className="mono text-[11px] text-text-faint">paused</span>}
          </div>
          <p className="mono text-xs text-text-faint">
            keep {schedule.retention_count} · {flagged.length} of the volumes above · last run{" "}
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
            // A run with nothing flagged copies nothing. Saying so beats a
            // success toast for zero volumes.
            disabledReason={flagged.length === 0 ? "No volume above is marked for backups" : undefined}
            onClick={() => run.mutate({ id: appId })}
          >
            <Play className="h-3.5 w-3.5" /> Back up now
          </ActionButton>
          <Button size="sm" variant="ghost" aria-pressed={showHistory} onClick={() => setShowHistory((v) => !v)}>
            <History className="h-3.5 w-3.5" /> History
          </Button>
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" aria-label="Delete schedule">
                <Trash2 className="h-3.5 w-3.5 text-danger" />
              </Button>
            }
            title="Delete this volume backup schedule?"
            blastRadius="Stops future archives and forgets the cadence. Archives already in your S3 target are left alone — nothing in the bucket is deleted."
            actionLabel="Delete schedule"
            pending={del.isPending}
            pendingLabel="Deleting…"
            onConfirm={() => del.mutate({ id: appId })}
          />
        </div>
      </div>
      {showHistory && <HistoryList appId={appId} />}
    </div>
  );
}

function HistoryList({ appId }: { appId: string }) {
  // One archive that is still running keeps the list polling: the agent's
  // outcome arrives as a bus event the plane records, and the browser has no
  // stream for it.
  const history = useListVolumeBackupRecords(appId, {
    query: {
      refetchInterval: (q) => ((q.state.data ?? []).some((r) => r.status === "running") ? 2_000 : false),
    },
  });
  const showSkeleton = useSkeletonDelay(history.isPending);
  return (
    <div className="border-t border-border px-4">
      <PageState
        query={history}
        loading={showSkeleton ? <SkeletonRows columns="auto 1fr auto" rows={2} /> : null}
        empty={
          <EmptyState
            glyph={null}
            className="py-3"
            title="Nothing archived yet"
            hint="Back up now runs one immediately; the schedule takes care of the rest."
          />
        }
      >
        {(records) => (
          <ul className="divide-y divide-border">
            {records.map((r) => (
              <RecordRow key={r.id} record={r} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function RecordRow({ record: r }: { record: VolumeBackupRecord }) {
  return (
    <li className="flex items-center justify-between gap-3 py-2.5">
      <span className="flex min-w-0 items-center gap-2">
        <StatusBadge status={r.status} />
        <span className="mono truncate text-[12.5px] text-text">{r.volume_name}</span>
      </span>
      <span className="mono shrink-0 text-[11.5px] text-text-faint" title={absoluteTime(r.started_at)}>
        {relativeTime(r.started_at)}
        {r.size_bytes > 0 && ` · ${formatBytes(r.size_bytes)}`}
        {r.status === "failed" && r.detail ? ` · ${r.detail}` : ""}
      </span>
    </li>
  );
}

function ScheduleDialog({
  appId,
  schedule,
  primary,
}: {
  appId: string;
  schedule?: VolumeBackup;
  primary?: boolean;
}) {
  const qc = useQueryClient();
  const me = useGetMe();
  const targets = useListBackupTargets();
  const [open, setOpen] = useState(false);
  const [targetId, setTargetId] = useState(schedule?.target_id ?? "");
  const [cron, setCron] = useState(schedule?.schedule ?? "0 4 * * *");
  const [retention, setRetention] = useState(String(schedule?.retention_count ?? 7));
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [error, setError] = useState<string | null>(null);

  const list = targets.data ?? [];
  const effectiveTarget = targetId || list[0]?.id || "";

  const save = useSetVolumeBackup({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetVolumeBackupQueryKey(appId) });
        setOpen(false);
        toastSuccess(schedule ? "Schedule updated" : "Schedule created");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the schedule"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const keep = Number.parseInt(retention, 10);
    if (!effectiveTarget) {
      setError("Pick a backup target.");
      return;
    }
    if (!Number.isFinite(keep) || keep < 1 || keep > 365) {
      setError("Keep between 1 and 365 archives per volume.");
      return;
    }
    setError(null);
    save.mutate({
      id: appId,
      data: { target_id: effectiveTarget, schedule: cron.trim(), retention_count: keep, enabled },
    });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          {schedule ? "Edit schedule" : <><Plus className="h-3.5 w-3.5" aria-hidden /> Schedule backups</>}
        </Button>
      </DialogTrigger>
      <DialogContent
        title={schedule ? "Edit volume backup schedule" : "Schedule volume backups"}
        description="Every volume marked “in backups” is archived on this cadence, to the target below."
      >
        <form onSubmit={submit} className="space-y-4">
          <Field label="Backup target">
            {(id, describedBy) => (
              <select
                id={id}
                aria-describedby={describedBy}
                value={effectiveTarget}
                onChange={(e) => setTargetId(e.target.value)}
                className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
              >
                {list.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} — {t.bucket}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <Field label="Schedule" hint="Leave empty to run only when you press “Back up now”.">
            {(id, describedBy) => (
              <CronField
                id={id}
                describedBy={describedBy}
                value={cron}
                onChange={setCron}
                viewerZone={me.data?.timezone}
              />
            )}
          </Field>
          <Field
            label="Keep"
            qualifier="· archives per volume"
            hint="Older archives are deleted from the bucket after a run succeeds — never before, so a failed run cannot cost you the copy you still have."
          >
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                type="number"
                min={1}
                max={365}
                value={retention}
                onChange={(e) => setRetention(e.target.value)}
                className="mono w-28"
              />
            )}
          </Field>
          <label className="flex items-center gap-2.5 text-[13px] text-text">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.currentTarget.checked)}
              className="size-3.5 accent-accent"
            />
            Run on the schedule
          </label>
          {/* The one thing an operator must not assume about this feature. */}
          <p className="text-[12px] leading-[1.5] text-text-faint">
            A volume archive is a tar of a live directory, not a snapshot. Managed databases keep their own dumps —
            back those up on the database's own Backups tab.
          </p>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={save.isPending ? "busy" : "idle"}
              busyLabel="Saving…"
            >
              {schedule ? "Save schedule" : "Create schedule"}
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
