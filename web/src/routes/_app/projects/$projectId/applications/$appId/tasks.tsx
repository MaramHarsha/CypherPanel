// Application · Scheduled tasks: cron jobs that run inside the app's own
// container (ADR-011). Command is argv, never a shell string. Each task shows
// its run history with exit codes and output tails.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { History, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
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
import { CronField } from "@/components/cron-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime, absoluteTime } from "@/lib/time";

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
        toast.success("Task removed");
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not remove the task"),
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

function NewTaskDialog({ appId, primary }: { appId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [schedule, setSchedule] = useState("0 * * * *");
  const [command, setCommand] = useState<string[]>([""]);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateScheduledTask({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListScheduledTasksQueryKey(appId) });
        toast.success("Scheduled task created");
        setError(null);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the task"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const argv = command.map((a) => a.trim()).filter((a) => a !== "");
    if (argv.length === 0) {
      setError("Enter at least the command to run");
      return;
    }
    create.mutate({ id: appId, data: { name, schedule, command: argv, enabled: true } });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New task
        </Button>
      </DialogTrigger>
      {/* No description under the title: canvas 9c puts the one thing that
          needs saying — where the command runs — in the footer note, next to
          the button that commits to it. */}
      <DialogContent title="New scheduled task">
        <form onSubmit={submit} className="space-y-4">
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
          {/* CronField and ArgvInput each own their control's accessible name,
              so these are the label's text without the htmlFor Field gives. */}
          <div>
            <p className="mb-[5px] text-[12px] font-semibold text-text">
              Schedule <span className="font-normal text-text-faint">· cron, server time UTC</span>
            </p>
            <CronField value={schedule} onChange={setSchedule} />
          </div>
          <div>
            <p className="mb-[5px] text-[12px] font-semibold text-text">
              Command <span className="font-normal text-text-faint">· argv, never a shell string</span>
            </p>
            <ArgvInput value={command} onChange={setCommand} />
          </div>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2.5">
            <span className="mr-auto text-[11.5px] text-text-faint">runs in the app's own container</span>
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create task"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
