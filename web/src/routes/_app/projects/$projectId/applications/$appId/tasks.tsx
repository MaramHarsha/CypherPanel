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
  useUpdateScheduledTask,
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
        <TaskDialog appId={appId} />
      </div>
      <PageState
        query={tasks}
        empty={
          <EmptyState
            title="No scheduled tasks"
            hint="Run a command inside this app on a schedule — migrations, cleanups, reports. It runs in the app's own container, on cron."
            action={<TaskDialog appId={appId} primary />}
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
          {/* Pausing is the reversible half of deleting, and the card already
              says "paused" — so the way back from that word is on the card
              rather than three clicks into an edit form. */}
          <PauseToggle appId={appId} task={task} />
          <TaskDialog appId={appId} task={task} />
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

/**
 * Pause and resume, as an update that changes exactly one field. It is not a
 * separate route and does not need to be: a paused task keeps its schedule, its
 * command and its run history, and the only thing that changes is whether the
 * next tick fires. That is the difference between silencing a job for an
 * afternoon and losing how it was set up.
 */
function PauseToggle({ appId, task }: { appId: string; task: ScheduledTask }) {
  const qc = useQueryClient();
  const update = useUpdateScheduledTask({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListScheduledTasksQueryKey(appId) });
        toastSuccess(task.enabled ? `${task.name} paused` : `${task.name} resumed`);
      },
      onError: (e: unknown) => toastFailed(task.enabled ? "Could not pause the task" : "Could not resume the task", e),
    },
  });
  return (
    <ActionButton
      size="sm"
      variant="ghost"
      state={update.isPending ? "busy" : "idle"}
      busyLabel={task.enabled ? "Pausing…" : "Resuming…"}
      onClick={() =>
        update.mutate({
          id: task.id,
          data: { name: task.name, schedule: task.schedule, command: task.command, enabled: !task.enabled },
        })
      }
    >
      {task.enabled ? "Pause" : "Resume"}
    </ActionButton>
  );
}

/**
 * New task, or edit one. `PUT /scheduled-tasks/{id}` replaces the task
 * wholesale, so the edit form is the create form seeded from what is there —
 * there is no partial update to model and no field that means "leave this
 * alone". Nothing here is sealed, so nothing starts empty.
 */
function TaskDialog({ appId, task, primary }: { appId: string; task?: ScheduledTask; primary?: boolean }) {
  const editing = task !== undefined;
  const qc = useQueryClient();
  // Already in the cache from the shell — this only reads the zone the operator
  // chose to read timestamps in, so the cron preview can speak it too.
  const me = useGetMe();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(task?.name ?? "");
  const [schedule, setSchedule] = useState(task?.schedule ?? DEFAULT_SCHEDULE);
  const [command, setCommand] = useState<string[]>(task?.command ?? [""]);
  const commandLabelId = useId();

  const done = (verb: string) => {
    void qc.invalidateQueries({ queryKey: getListScheduledTasksQueryKey(appId) });
    toastSuccess(verb);
    setOpen(false);
    resetForm();
  };

  const create = useCreateScheduledTask({
    mutation: {
      // The modal's job ends when the task exists: it closes onto the list,
      // which the invalidation refreshes, and the toast names what happened.
      onSuccess: () => done("Scheduled task created"),
      // The pill turns to "✕ Retry"; the toast carries the why and the next step (10c).
      onError: (e: unknown, vars) => toastFailed("Could not create the task", e, { retry: () => create.mutate(vars) }),
    },
  });
  const update = useUpdateScheduledTask({
    mutation: {
      onSuccess: () => done("Scheduled task saved"),
      onError: (e: unknown, vars) => toastFailed("Could not save the task", e, { retry: () => update.mutate(vars) }),
    },
  });
  const submitState = useMutationActionState(editing ? update : create);

  function resetForm() {
    setName(task?.name ?? "");
    setSchedule(task?.schedule ?? DEFAULT_SCHEDULE);
    setCommand(task?.command ?? [""]);
  }

  // Opening resets the mutation as well as the fields, so a reopened modal
  // never inherits the last attempt's "✓ Created" or "✕ Retry" pill.
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      create.reset();
      update.reset();
      return;
    }
    resetForm();
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
    const data = {
      name: name.trim(),
      schedule: schedule.trim(),
      command: argv,
      // An edit must not silently resume a paused task: the pause lives on the
      // card, and this form does not touch it.
      enabled: task?.enabled ?? true,
    };
    if (editing) {
      update.mutate({ id: task.id, data });
      return;
    }
    create.mutate({ id: appId, data });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        {editing ? (
          <Button size="sm" variant="ghost">
            Edit
          </Button>
        ) : (
          <Button variant="primary" size={primary ? "lg" : "md"}>
            <Plus className="h-3.5 w-3.5" /> New task
          </Button>
        )}
      </DialogTrigger>
      {/* No description under the title: canvas 9c puts the one thing that
          needs saying — where the command runs — in the footer note, next to
          the button that commits to it. */}
      <DialogContent title={editing ? `Edit ${task.name}` : "New scheduled task"}>
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
              busyLabel={editing ? "Saving…" : "Creating…"}
              successLabel={editing ? "Saved" : "Created"}
              failedLabel="Retry"
              disabledReason={disabledReason}
            >
              {editing ? "Save task" : "Create task"}
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
