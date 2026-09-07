// Settings · Log drains (log-drains.md §7, canvas 13f).
//
// The panel keeps a WINDOW of runtime logs — 24 hours, 512 MiB, oldest-first
// discard. That is the right size for "what happened at 03:00" and the wrong
// size for "what did this endpoint return last Tuesday". A drain hands the
// lines to something whose job is keeping them.
//
// Two pieces of copy here are load-bearing rather than decorative:
//
//   The health words. All five states are shown, including the two that look
//   quiet for opposite reasons — IDLE is healthy with nothing to ship, FAILING
//   is a pipeline that has stopped. A drain is never auto-disabled, because
//   that turns a visible failure into a silent one that does not resume.
//
//   The dropped count. A drain down a week resumes at the oldest message still
//   held, having lost the rest. Saying how many is the difference between an
//   archive an operator can trust and one they only think they have.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useListBackupTargets } from "@/api/gen/backups/backups";
import {
  getListLogDrainsQueryKey,
  useCreateLogDrain,
  useDeleteLogDrain,
  useListLogDrains,
  useUpdateLogDrain,
} from "@/api/gen/panel/panel";
import { useListProjects } from "@/api/gen/projects/projects";
import type { LogDrain } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/log-drains")({ component: LogDrainsTab });

type Health = { label: string; tone: string; dot: boolean; why: string };

const UNKNOWN_HEALTH: Health = {
  label: "IDLE",
  tone: "text-text-faint",
  dot: false,
  why: "Enabled and healthy — nothing to ship.",
};

const HEALTH: Record<string, Health> = {
  shipping: { label: "SHIPPING", tone: "text-status-running", dot: true, why: "A batch was accepted recently." },
  idle: { label: "IDLE", tone: "text-text-faint", dot: false, why: "Enabled and healthy — nothing to ship." },
  retrying: {
    label: "RETRYING",
    tone: "text-status-degraded-text",
    dot: true,
    why: "Failing for under five minutes. Lines are held on the panel's stream meanwhile.",
  },
  failing: {
    label: "FAILING",
    tone: "text-danger",
    dot: true,
    why: "Failing for five minutes or more. It keeps retrying — a drain is never switched off for you.",
  },
  disabled: { label: "DISABLED", tone: "text-text-faint", dot: false, why: "Switched off. Nothing is attempted." },
};

const SELECT =
  "w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none";

function LogDrainsTab() {
  useCrumbs([{ label: "settings" }, { label: "log drains" }]);
  const drains = useListLogDrains({ query: { refetchInterval: 15_000 } });

  return (
    <div className="max-w-3xl space-y-4">
      <div className="flex items-center gap-3">
        <Eyebrow>Log drains</Eyebrow>
        <span className="ml-auto">
          <DrainDialog />
        </span>
      </div>

      <PageState
        query={drains}
        skeletonColumns="1fr auto"
        skeletonRows={2}
        empty={
          <EmptyState
            glyph="⇥"
            title="No log drains"
            hint="The panel keeps 24 hours of application logs, then discards the oldest. A drain streams them onward to Loki, syslog or an S3 bucket — somewhere whose job is keeping them."
            action={<DrainDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((d: LogDrain) => (
              <DrainRow key={d.id} drain={d} />
            ))}
          </ul>
        )}
      </PageState>

      {/* Said once, on the page, rather than discovered by an operator who
          wondered where their database logs went. */}
      <p className="text-[11.5px] leading-[1.5] text-text-faint">
        A drain ships application logs. Managed databases and Compose stacks are not on the stream it reads yet.
        Lines are what the panel saw: the agent strips a trailing carriage return and drops empty lines before they
        reach the panel at all, so the drain and the panel's own log pane never disagree.
      </p>
    </div>
  );
}

function DrainRow({ drain: d }: { drain: LogDrain }) {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: getListLogDrainsQueryKey() });
  const health = HEALTH[d.health] ?? UNKNOWN_HEALTH;

  const toggle = useUpdateLogDrain({
    mutation: {
      onSuccess: (r) => {
        invalidate();
        toastSuccess(r.enabled ? "Drain resumed" : "Drain paused");
      },
      onError: (e: unknown, vars) => toastFailed("Could not change the drain", e, { retry: () => toggle.mutate(vars) }),
    },
  });
  const del = useDeleteLogDrain({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess("Drain deleted");
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the drain", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-text">{d.name}</span>
          <span className={cn("mono text-[10.5px]", health.tone)} title={health.why}>
            {health.dot && "● "}
            {health.label}
          </span>
        </div>
        <p className="mono mt-0.5 text-[11px] text-text-faint">
          {d.kind} · {d.config_hint} · {d.project_id ? "one project" : "all projects"}
          {d.last_shipped_at && ` · last shipped ${relativeTime(d.last_shipped_at)}`}
        </p>
        {d.last_error && (
          <p className="mt-1 text-[12px] leading-[1.5] text-danger">
            {d.last_error}
            {d.last_error_at && (
              <span className="text-text-faint"> · since {relativeTime(d.last_error_at)}</span>
            )}
          </p>
        )}
        {d.dropped_lines > 0 && (
          // The number an operator needs before they trust the archive.
          <p className="mt-1 text-[12px] leading-[1.5] text-status-degraded-text">
            {d.dropped_lines.toLocaleString()} lines aged out of the panel's window before this drain could ship
            them. They are gone.
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <ActionButton
          size="sm"
          variant="ghost"
          state={toggle.isPending ? "busy" : "idle"}
          busyLabel="…"
          onClick={() => toggle.mutate({ id: d.id, data: { enabled: !d.enabled } })}
        >
          {d.enabled ? "Pause" : "Resume"}
        </ActionButton>
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" aria-label={`Delete ${d.name}`}>
              <Trash2 className="h-3.5 w-3.5 text-danger" aria-hidden />
            </Button>
          }
          title={`Delete the drain ${d.name}?`}
          lead="Deleting this drain:"
          blastRadius={[
            "no further lines are shipped to this destination",
            "its cursor is removed, so re-creating it later starts from whatever is still in the panel's 24-hour window",
            "everything already delivered stays where it was delivered — nothing at the destination is touched",
          ]}
          confirmName={d.name}
          actionLabel="Delete drain"
          pendingLabel="Deleting…"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: d.id })}
        />
      </div>
    </li>
  );
}

function DrainDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"loki" | "syslog" | "s3">("loki");
  const [projectId, setProjectId] = useState("");
  const [targetId, setTargetId] = useState("");
  const [url, setUrl] = useState("");
  const [headers, setHeaders] = useState("");
  const [address, setAddress] = useState("");
  const [prefix, setPrefix] = useState("");
  const [error, setError] = useState<string | null>(null);

  const projects = useListProjects({ query: { enabled: open } });
  const targets = useListBackupTargets({ query: { enabled: open && kind === "s3" } });

  const create = useCreateLogDrain({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListLogDrainsQueryKey() });
        setOpen(false);
        toastSuccess({ title: "Drain created", detail: "It starts shipping within a few seconds." });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the drain"),
    },
  });

  const config = () => {
    if (kind === "loki") {
      const parsed: Record<string, string> = {};
      for (const line of headers.split("\n")) {
        const [k, ...rest] = line.split(":");
        if (k?.trim() && rest.length > 0) parsed[k.trim()] = rest.join(":").trim();
      }
      return { url: url.trim(), ...(Object.keys(parsed).length > 0 ? { headers: parsed } : {}) };
    }
    if (kind === "syslog") return { address: address.trim() };
    return prefix.trim() ? { prefix: prefix.trim() } : {};
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      data: {
        name: name.trim(),
        kind,
        project_id: projectId,
        target_id: kind === "s3" ? targetId : "",
        config: config(),
        enabled: true,
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> New drain
        </Button>
      </DialogTrigger>
      <DialogContent
        title="New log drain"
        description="Where application logs go after the panel's own 24-hour window."
      >
        <form onSubmit={submit} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" hint="Lowercase letters, digits and dashes.">
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="loki-main"
                  className="mono"
                  spellCheck={false}
                />
              )}
            </Field>
            <Field label="Destination">
              {(id) => (
                <select id={id} value={kind} onChange={(e) => setKind(e.target.value as typeof kind)} className={SELECT}>
                  <option value="loki">Loki</option>
                  <option value="syslog">Syslog (TCP)</option>
                  <option value="s3">S3 archive</option>
                </select>
              )}
            </Field>
          </div>

          <Field
            label="Scope"
            hint="Nothing finer than a project: every shipped line already carries its environment as a label, so the destination filters better than we can."
          >
            {(id) => (
              <select id={id} value={projectId} onChange={(e) => setProjectId(e.target.value)} className={SELECT}>
                <option value="">All projects</option>
                {(projects.data ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
          </Field>

          {kind === "loki" && (
            <>
              <Field label="Push URL">
                {(id) => (
                  <Input
                    id={id}
                    required
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    placeholder="https://loki.example.com/loki/api/v1/push"
                    className="mono"
                    spellCheck={false}
                  />
                )}
              </Field>
              <Field
                label="Headers"
                qualifier="· optional, one per line"
                hint="X-Scope-OrgID, an Authorization bearer, whatever your deployment needs. The whole config is sealed, so this is treated as a credential either way."
              >
                {(id, describedBy) => (
                  <textarea
                    id={id}
                    aria-describedby={describedBy}
                    value={headers}
                    onChange={(e) => setHeaders(e.target.value)}
                    rows={3}
                    spellCheck={false}
                    placeholder={"X-Scope-OrgID: acme\nAuthorization: Bearer …"}
                    className="w-full rounded-md border border-border-input bg-surface px-3 py-2 font-mono text-[12.5px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
                  />
                )}
              </Field>
            </>
          )}

          {kind === "syslog" && (
            <Field label="Address" hint="host:port. RFC 5424 frames over TCP, on a connection kept open between batches.">
              {(id, describedBy) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  required
                  value={address}
                  onChange={(e) => setAddress(e.target.value)}
                  placeholder="logs.example.com:514"
                  className="mono"
                  spellCheck={false}
                />
              )}
            </Field>
          )}

          {kind === "s3" && (
            <>
              <Field
                label="Backup target"
                hint="An S3 drain writes to a target you already have. There is no second set of keys to rotate."
              >
                {(id) => (
                  <select id={id} value={targetId} onChange={(e) => setTargetId(e.target.value)} className={SELECT}>
                    <option value="">Pick a target…</option>
                    {(targets.data ?? []).map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name} — {t.bucket}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
              <Field label="Prefix" qualifier="· optional" hint="Under the target's own path prefix. Objects are JSON lines, one per batch.">
                {(id) => (
                  <Input
                    id={id}
                    value={prefix}
                    onChange={(e) => setPrefix(e.target.value)}
                    placeholder="logs"
                    className="mono"
                    spellCheck={false}
                  />
                )}
              </Field>
            </>
          )}

          {/* The property that makes a drain safe to add to a busy panel. */}
          <p className="text-[12px] leading-[1.5] text-text-faint">
            A drain never slows a deploy — it reads the log stream and nothing waits on it. If the destination goes
            down, lines stay in the panel's own 24-hour window until it comes back, and anything older than that is
            counted as lost rather than quietly forgotten.
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
              state={create.isPending ? "busy" : "idle"}
              busyLabel="Creating…"
            >
              Create drain
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
