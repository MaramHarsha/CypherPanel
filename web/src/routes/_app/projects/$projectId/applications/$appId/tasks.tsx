// Application · Scheduled tasks: cron jobs that run inside the app's own
// container (ADR-011). Command is argv, never a shell string. Each task shows
// its run history with exit codes and output tails.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { History, Plus, Trash2 } from "lucide-react";
import { useId, useMemo, useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import {
  getListScheduledTasksQueryKey,
  useCreateScheduledTask,
  useDeleteScheduledTask,
  useListScheduledTaskRuns,
  useListScheduledTasks,
} from "@/api/gen/scheduled-tasks/scheduled-tasks";
import type { ScheduledTask, ScheduledTaskRun } from "@/api/gen/model";
import { ArgvInput } from "@/components/argv-input";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CronField, cronNextRuns } from "@/components/cron-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime, absoluteTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/tasks")({
  component: TasksTab,
});

function TasksTab() {
  const { appId } = Route.useParams();
  const tasks = useListScheduledTasks(appId);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <Eyebrow>Scheduled tasks</Eyebrow>
        <NewTaskDialog appId={appId} />
      </div>
      <PageState
        query={tasks}
        empty={
          <EmptyState
            title="No scheduled tasks"
            hint="Run a command inside this app on a schedule — migrations, cleanups, reports. It runs in the app's own container, on cron."
            action={<NewTaskDialog appId={appId} primary />}
          />
        }
      >
        {(list) => (
          <div className="space-y-4">
            {list.map((t) => (
              <TaskCard key={t.id} appId={appId} task={t} />
            ))}
          </div>
        )}
      </PageState>
    </div>
  );
}

function TaskCard({ appId, task }: { appId: string; task: ScheduledTask }) {
  const qc = useQueryClient();
  const [showRuns, setShowRuns] = useState(false);
  const del = useDeleteScheduledTask({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListScheduledTasksQueryKey(appId) });
        toastSuccess("Task removed");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the task", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <div className="rounded-lg border border-border bg-surface">
      <div className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[13px] font-medium text-text">{task.name}</span>
            {!task.enabled && <span className="mono text-[11px] text-text-faint">paused</span>}
          </div>
          <p className="mono truncate text-xs text-text-faint">
            {task.schedule} · {task.command.join(" ")}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          <Button size="sm" variant="ghost" aria-pressed={showRuns} onClick={() => setShowRuns((v) => !v)}>
            <History className="h-3.5 w-3.5" /> Runs
          </Button>
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" aria-label={`Delete ${task.name}`}>
                <Trash2 className="h-3.5 w-3.5 text-danger" />
              </Button>
            }
            title={`Delete "${task.name}"?`}
            // The list reads as consequences rather than removals, because the
            // second one is a survival — the canvas keeps a survival in the
            // entry it qualifies instead of dropping it after the box (13af).
            lead="Deleting this task:"
            blastRadius={[
              "nothing runs on this schedule again",
              "runs already recorded stay in history until they age out",
            ]}
            confirmName={task.name}
            actionLabel="Delete task"
            pending={del.isPending}
            onConfirm={() => del.mutate({ id: task.id })}
          />
        </div>
      </div>
      {showRuns && <RunsList taskId={task.id} />}
    </div>
  );
}

function RunsList({ taskId }: { taskId: string }) {
  const runs = useListScheduledTaskRuns(taskId);
  return (
    <div className="border-t border-border">
      <PageState
        query={runs}
        loading={<div className="px-4 py-3 text-xs text-text-faint">Loading runs…</div>}
        empty={<div className="px-4 py-3 text-xs text-text-faint">This task hasn't run yet.</div>}
      >
        {(list) => (
          <ul className="divide-y divide-border">
            {list.map((r) => (
              <RunRow key={r.id} run={r} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function RunRow({ run: r }: { run: ScheduledTaskRun }) {
  const ok = r.status === "succeeded";
  return (
    <li className="px-4 py-2">
      <div className="flex items-center justify-between gap-3">
        <span className="flex items-center gap-2">
          <StatusBadge status={ok ? "running" : r.status === "failed" ? "error" : "deploying"} />
          <span className="mono text-xs text-text-faint" title={absoluteTime(r.started_at)}>
            {relativeTime(r.started_at)}
            {typeof r.exit_code === "number" ? ` · exit ${r.exit_code}` : ""}
          </span>
        </span>
      </div>
      {r.output_tail && (
        <pre className="mono mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all rounded bg-bg p-2 text-[11px] text-text-mid">
          {r.output_tail}
        </pre>
      )}
    </li>
  );
}

const DEFAULT_SCHEDULE = "0 * * * *";

function NewTaskDialog({ appId, primary }: { appId: string; primary?: boolean }) {
  const qc = useQueryClient();
  // Already in the cache from the shell — this only reads the zone the operator
  // chose to read timestamps in, so the cron preview can speak it too.
  const me = useGetMe();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [schedule, setSchedule] = useState(DEFAULT_SCHEDULE);
  const [command, setCommand] = useState<string[]>([""]);
  const commandLabelId = useId();

  const create = useCreateScheduledTask({
    mutation: {
      // The modal's job ends when the task exists: it closes onto the list,
      // which the invalidation refreshes, and the toast names what happened.
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListScheduledTasksQueryKey(appId) });
        toastSuccess("Scheduled task created");
        setOpen(false);
        resetForm();
      },
      // The pill turns to "✕ Retry"; the toast carries the why and the next step (10c).
      onError: (e: unknown, vars) => toastFailed("Could not create the task", e, { retry: () => create.mutate(vars) }),
    },
  });
  const submitState = useMutationActionState(create);

  function resetForm() {
    setName("");
    setSchedule(DEFAULT_SCHEDULE);
    setCommand([""]);
  }

  // Opening resets the mutation as well as the fields, so a reopened modal
  // never inherits the last attempt's "✓ Created" or "✕ Retry" pill.
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) create.reset();
    else resetForm();
  };

  const argv = command.map((a) => a.trim()).filter((a) => a !== "");
  const scheduleOk = useMemo(() => cronNextRuns(schedule, 1).ok, [schedule]);
  // 10b: a pill that cannot be pressed names its reason; the tooltip opens on
  // hover and on focus, and an inert submit swallows Enter in a field too.
  const disabledReason =
    name.trim() === ""
      ? "Name the task first"
      : !scheduleOk
        ? "Fix the schedule first"
        : argv.length === 0
          ? "Enter the command to run"
          : undefined;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (disabledReason) return;
    create.mutate({ id: appId, data: { name: name.trim(), schedule: schedule.trim(), command: argv, enabled: true } });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New task
        </Button>
      </DialogTrigger>
      {/* No description under the title: canvas 9c puts the one thing that
          needs saying — where the command runs — in the footer note, next to
          the button that commits to it. */}
      <DialogContent title="New scheduled task">
        <form onSubmit={submit} className="space-y-3">
          <Field label="Name">
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="nightly-migrate"
                className="font-sans"
              />
            )}
          </Field>
          {/* "server time", not "server time UTC": the agent reads the server's
              own clock and nothing records its zone, so the preview under the
              field says which zone it is shown in rather than the label
              promising one. */}
          <Field label="Schedule" qualifier="· cron, server time">
            {(id, describedBy) => (
              <CronField
                id={id}
                describedBy={describedBy}
                value={schedule}
                onChange={setSchedule}
                viewerZone={me.data?.timezone}
              />
            )}
          </Field>
          {/* A group, not a label: the chips are several inputs with their own
              names, and this heading is what names the set of them. */}
          <div className="space-y-1.5">
            <p id={commandLabelId} className="text-[12px] font-semibold text-text">
              Command <span className="font-normal text-text-faint">· argv, never a shell string</span>
            </p>
            <ArgvInput value={command} onChange={setCommand} labelledBy={commandLabelId} />
          </div>
          <div className="flex items-center gap-2.5 pt-1">
            <span className="mr-auto text-[11.5px] text-text-faint">runs in the app's own container</span>
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              state={submitState}
              busyLabel="Creating…"
              successLabel="Created"
              failedLabel="Retry"
              disabledReason={disabledReason}
            >
              Create task
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
