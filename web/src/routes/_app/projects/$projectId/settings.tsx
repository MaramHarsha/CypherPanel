// Project settings · Notifiers: on these events, deliver to this channel. Config
// is write-only (masked hint after saving); the Test button is first-class —
// a notifier that silently never fires is the common footgun (notifications.md).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
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
import { useGetProject } from "@/api/gen/projects/projects";
import type { Notifier } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/projects/$projectId/settings")({ component: ProjectSettings });

const EVENTS = [
  { key: "deploy.succeeded", label: "Deploy succeeded" },
  { key: "deploy.failed", label: "Deploy failed" },
  { key: "backup.succeeded", label: "Backup succeeded" },
  { key: "backup.failed", label: "Backup failed" },
] as const;

const CHANNELS = ["discord", "slack", "telegram", "email"] as const;
type Channel = (typeof CHANNELS)[number];

function ProjectSettings() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const notifiers = useListNotifiers(projectId);

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings" },
  ]);

  return (
    <div className="max-w-xl space-y-4">
      <div className="flex items-center justify-between">
        <Eyebrow>Notifiers</Eyebrow>
        <NewNotifierDialog projectId={projectId} />
      </div>
      <p className="text-[13px] text-text-mid">
        Get a message when a deploy or backup in this project succeeds or fails — on Discord, Slack, Telegram, or email.
      </p>
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
          <ul className="divide-y divide-border rounded-md border border-border bg-surface">
            {list.map((n) => (
              <NotifierRow key={n.id} projectId={projectId} notifier={n} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
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
        <Button variant={primary ? "primary" : "secondary"} size="sm">
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
                className="h-8 w-full rounded-md border border-border bg-surface px-2 text-sm text-text"
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
