// Project settings · Notifiers: on these events, deliver to this channel. Config
// is write-only (masked hint after saving); the Test button is first-class —
// a notifier that silently never fires is the common footgun (notifications.md).
//
// This is the first tab of the project-settings layout; the masthead and the
// page gutters belong to settings.tsx, so the tab renders only its own section.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Plus, Send, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getListNotifiersQueryKey,
  useCreateNotifier,
  useDeleteNotifier,
  useListNotifiers,
  useTestNotifier,
} from "@/api/gen/notifiers/notifiers";
import {
  getGetProjectQueryKey,
  getListProjectsQueryKey,
  useDeleteProject,
  useGetProject,
} from "@/api/gen/projects/projects";
import type { Notifier } from "@/api/gen/model";
import { ApiError } from "@/api/client";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/projects/$projectId/settings/")({ component: NotifiersTab });

const EVENTS = [
  { key: "deploy.succeeded", label: "Deploy succeeded" },
  { key: "deploy.failed", label: "Deploy failed" },
  { key: "backup.succeeded", label: "Backup succeeded" },
  { key: "backup.failed", label: "Backup failed" },
] as const;

const CHANNELS = ["discord", "slack", "telegram", "email"] as const;
type Channel = (typeof CHANNELS)[number];

function NotifiersTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const notifiers = useListNotifiers(projectId);

  // Canvas 12c datelines this page from the project, not from the projects
  // list: the trail names where you are inside the project, and PROJECTS is
  // already one click away in the top bar.
  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings" },
  ]);

  return (
    <div className="max-w-2xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Notifiers</Eyebrow>
        <NewNotifierDialog projectId={projectId} />
      </div>
      <InlineHint>
        Get a message when a deploy or backup in this project succeeds or fails — on Discord, Slack, Telegram, or email.
      </InlineHint>
      <PageState
        query={notifiers}
        empty={
          <EmptyState
            title="No notifiers"
            hint="Add one to hear about failures without watching the panel."
            action={<NewNotifierDialog projectId={projectId} primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((n) => (
              <NotifierRow key={n.id} projectId={projectId} notifier={n} />
            ))}
          </ul>
        )}
      </PageState>

      <DangerZone projectId={projectId} name={project.data?.project.name ?? ""} />
    </div>
  );
}

/** Deleting a project takes its environments, applications and databases with
 *  it, so the API refuses while any remain — a managed database's container and
 *  data volume are torn down from its own row, which the cascade would remove
 *  first. The refusal is surfaced here rather than as a toast, because it is an
 *  instruction, not a notification. */
function DangerZone({ projectId, name }: { projectId: string; name: string }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [blocked, setBlocked] = useState<string | null>(null);

  const del = useDeleteProject({
    mutation: {
      onSuccess: () => {
        // /projects renders from cache, and nothing streams project changes
        // in — so the row we just deleted would still be sitting there when we
        // land. Drop both the list and this project's own entry first, then
        // navigate, so the page we arrive at is one we can vouch for.
        void qc.invalidateQueries({ queryKey: getListProjectsQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(projectId) });
        toast.success(`Deleted ${name}`);
        void navigate({ to: "/projects" });
      },
      onError: (e: unknown) => {
        if (e instanceof ApiError && e.status === 409) {
          setBlocked(e.message);
          return;
        }
        toast.error(e instanceof Error ? e.message : "Could not delete the project");
      },
    },
  });

  return (
    // 12c draws this as one plain white card behind a heavy red rule — no
    // eyebrow: "Delete this project" already says which zone this is, and a
    // second heading only pushes the sentence that matters further down.
    <section className="mt-8 rounded-lg border-[1.5px] border-status-error/40 bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3.5">
        <div className="min-w-0">
          <p className="text-[13.5px] font-semibold text-text">Delete this project</p>
          <p className="mt-[3px] text-xs leading-relaxed text-text-mid">
            Removes the project and its environments. Delete its applications and databases first.
          </p>
        </div>
        <ConfirmDestructive
          trigger={<Button variant="danger">Delete</Button>}
          title={`Delete ${name}?`}
          blastRadius="Deletes this project and every environment in it, along with their notifiers and webhook endpoints. It cannot be undone. Applications and databases must be deleted first — their containers and data volumes are torn down individually."
          confirmName={name}
          actionLabel="Delete project"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: projectId })}
        />
      </div>
      {blocked && (
        <p role="alert" className="mt-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {blocked}
        </p>
      )}
    </section>
  );
}

function NotifierRow({ projectId, notifier: n }: { projectId: string; notifier: Notifier }) {
  const qc = useQueryClient();
  const test = useTestNotifier({
    mutation: {
      onSuccess: () => toast.success("Test sent — check the channel"),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Test failed to send"),
    },
  });
  const del = useDeleteNotifier({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListNotifiersQueryKey(projectId) });
        toast.success(`Deleted ${n.name}`);
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the notifier"),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="flex min-w-0 flex-col">
        <span className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-text">{n.name}</span>
          <span className="mono text-[11px] text-text-faint">{n.channel}</span>
          {!n.enabled && <span className="mono text-[11px] text-text-faint">off</span>}
        </span>
        <span className="mono truncate text-xs text-text-faint">
          {n.config_hint} · {n.events.length} event{n.events.length > 1 ? "s" : ""}
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <Button size="sm" variant="ghost" disabled={test.isPending} onClick={() => test.mutate({ id: n.id })}>
          <Send className="h-3.5 w-3.5" /> Test
        </Button>
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" aria-label={`Delete ${n.name}`}>
              <Trash2 className="h-3.5 w-3.5 text-danger" />
            </Button>
          }
          title={`Delete ${n.name}?`}
          blastRadius="Stops these notifications. Nothing else is affected."
          actionLabel="Delete notifier"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: n.id })}
        />
      </span>
    </li>
  );
}

function NewNotifierDialog({ projectId, primary }: { projectId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [channel, setChannel] = useState<Channel>("discord");
  const [events, setEvents] = useState<Set<string>>(new Set(["deploy.failed"]));
  const [cfg, setCfg] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);

  const setField = (k: string) => (e: React.ChangeEvent<HTMLInputElement>) => setCfg((c) => ({ ...c, [k]: e.target.value }));

  const create = useCreateNotifier({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListNotifiersQueryKey(projectId) });
        toast.success("Notifier added");
        setError(null);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not add the notifier"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (events.size === 0) {
      setError("Pick at least one event to notify on");
      return;
    }
    const config = channel === "email" ? { ...cfg, smtp_port: Number(cfg.smtp_port ?? "587") } : cfg;
    create.mutate({
      id: projectId,
      data: {
        name,
        channel: channel as "discord" | "slack" | "telegram" | "email",
        events: [...events] as ("deploy.succeeded" | "deploy.failed" | "backup.succeeded" | "backup.failed")[],
        config,
        enabled: true,
      },
    });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" /> New notifier
        </Button>
      </DialogTrigger>
      <DialogContent title="Add a notifier" description="Where to send it, and which events matter.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name">
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="team-alerts" />}
          </Field>
          <Field label="Channel">
            {(id) => (
              <select
                id={id}
                value={channel}
                onChange={(e) => {
                  setChannel(e.target.value as Channel);
                  setCfg({});
                }}
                className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text"
              >
                {CHANNELS.map((c) => (
                  <option key={c} value={c}>
                    {c}
                  </option>
                ))}
              </select>
            )}
          </Field>

          <ChannelFields channel={channel} value={cfg} onField={setField} />

          <fieldset className="space-y-1.5">
            <legend className="text-[13px] font-medium text-text">Notify on</legend>
            {EVENTS.map((ev) => (
              <label key={ev.key} className="flex items-center gap-2 text-[13px] text-text-mid">
                <input
                  type="checkbox"
                  checked={events.has(ev.key)}
                  onChange={(e) =>
                    setEvents((prev) => {
                      const next = new Set(prev);
                      if (e.target.checked) next.add(ev.key);
                      else next.delete(ev.key);
                      return next;
                    })
                  }
                />
                {ev.label}
              </label>
            ))}
          </fieldset>

          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? "Adding…" : "Add notifier"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ChannelFields({
  channel,
  value,
  onField,
}: {
  channel: Channel;
  value: Record<string, string>;
  onField: (k: string) => (e: React.ChangeEvent<HTMLInputElement>) => void;
}) {
  if (channel === "discord" || channel === "slack") {
    return (
      <Field label="Webhook URL" hint={`Create an incoming webhook in ${channel} and paste its URL.`}>
        {(id) => (
          <Input id={id} required value={value.webhook_url ?? ""} onChange={onField("webhook_url")} className="mono" placeholder="https://…" />
        )}
      </Field>
    );
  }
  if (channel === "telegram") {
    return (
      <div className="grid grid-cols-2 gap-3">
        <Field label="Bot token" hint="From @BotFather.">
          {(id) => <Input id={id} required value={value.bot_token ?? ""} onChange={onField("bot_token")} className="mono" />}
        </Field>
        <Field label="Chat ID">
          {(id) => <Input id={id} required value={value.chat_id ?? ""} onChange={onField("chat_id")} className="mono" />}
        </Field>
      </div>
    );
  }
  // email
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <Field label="SMTP host">{(id) => <Input id={id} required value={value.smtp_host ?? ""} onChange={onField("smtp_host")} className="mono" />}</Field>
        <Field label="SMTP port">{(id) => <Input id={id} value={value.smtp_port ?? "587"} onChange={onField("smtp_port")} className="mono" inputMode="numeric" />}</Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Username">{(id) => <Input id={id} value={value.username ?? ""} onChange={onField("username")} className="mono" autoComplete="off" />}</Field>
        <Field label="Password">{(id) => <Input id={id} type="password" value={value.password ?? ""} onChange={onField("password")} className="mono" autoComplete="off" />}</Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="From">{(id) => <Input id={id} required value={value.from ?? ""} onChange={onField("from")} className="mono" placeholder="alerts@acme.com" />}</Field>
        <Field label="To" hint="Comma-separated.">{(id) => <Input id={id} required value={value.to ?? ""} onChange={onField("to")} className="mono" />}</Field>
      </div>
    </div>
  );
}
