// ConnectionDialog — canvas 13aj (dark twin of 9l): "+ Add registry / drain /
// notifier → one connection pattern, always testable". A host or URL, a
// credential you write but never read back, a handful of chips, a result
// banner, and a footer of `↗ Test` beside `Save`.
//
// The shell is generic on purpose. Registries (6d/13d) and log drains (6f/13f)
// have no API yet, so only the notifier variant is built here — but it is built
// as a variant of the shell, not as its own modal, so the next connection type
// adds fields and a test call rather than a second dialog.
//
// One honest deviation from the board. 13aj tests BEFORE saving and paints
// "✓ Test passed — authenticated, 2 repositories visible". The panel has no
// endpoint that tests an unsaved config, and POST /notifiers/{id}/test answers
// 202 whether or not the channel accepted the message (a channel failure is
// logged, not surfaced). So the order is SAVE, THEN TEST: Save commits and
// moves the dialog to a test step; the banner says a delivery was attempted,
// never that it passed; and the copy says where the real answer is — in the
// channel. A green "passed" here would be a lie the operator discovers at 2am.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type ChangeEvent, type FormEvent, type ReactNode } from "react";
import { getListNotifiersQueryKey, useCreateNotifier, useTestNotifier } from "@/api/gen/notifiers/notifiers";
import type { CreateNotifierRequestChannel, CreateNotifierRequestEventsItem, Notifier } from "@/api/gen/model";
import { ActionButton, useMutationActionState, type ActionState } from "@/components/ui/action-button";
import { Dialog, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

// ─── The shell ───────────────────────────────────────────────────────────────

/**
 * `passed` is reserved for a test whose outcome the API actually reports
 * (13aj's green banner). `sent` is the honest tone for a fire-and-forget test —
 * neutral, because the panel does not know how it went. `failed` is the red
 * variant the board implies.
 */
export interface ConnectionTestResult {
  tone: "passed" | "sent" | "failed";
  message: string;
}

const TONE: Record<ConnectionTestResult["tone"], string> = {
  passed: "border-status-running/35 bg-status-running/[0.06] text-status-running",
  sent: "border-border bg-raised text-text-mid",
  failed: "border-status-error/35 bg-status-error/[0.06] text-danger",
};

export function TestResultBanner({ result }: { result: ConnectionTestResult }) {
  return (
    <p
      role="status"
      className={cn("flex items-start gap-[9px] rounded-md border px-[13px] py-[9px] text-[12.5px] leading-snug", TONE[result.tone])}
    >
      <span aria-hidden className="shrink-0">
        {result.tone === "failed" ? "✕" : "✓"}
      </span>
      <span>{result.message}</span>
    </p>
  );
}

/**
 * 13aj's scope chips — `pull ✓` in a 1.5px ink ring, `push` muted beside it.
 * A pressed toggle, not a checkbox: the board draws them as pills and the ✓
 * is part of the label, so a screen reader hears "pressed" rather than a
 * second glyph.
 */
export function ChoiceChip({
  selected,
  onToggle,
  children,
  className,
}: {
  selected: boolean;
  onToggle: () => void;
  children: ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onToggle}
      // 1.5px in both states: the board thins the muted ring to 1px, but a
      // ring that changes width nudges every chip beside it on each toggle.
      className={cn(
        "rounded-full border-[1.5px] bg-surface px-3 py-1 text-[12px] transition-colors",
        selected
          ? "border-border-strong font-semibold text-text"
          : "border-border-input text-text-mid hover:border-text-faint hover:text-text",
        className,
      )}
    >
      {children}
      {selected && <span aria-hidden> ✓</span>}
    </button>
  );
}

/** One footer pill, in the 10b vocabulary. */
export interface ConnectionAction {
  label: ReactNode;
  state: ActionState;
  busyLabel: string;
  successLabel?: string;
  failedLabel?: string;
  /** Names why the pill is inert — "Save first — a test sends through the saved notifier". */
  disabledReason?: string;
  onClick?: () => void;
}

interface ConnectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: ReactNode;
  title: string;
  description?: string;
  /** Mono breadcrumb above the title — where this connection will live. */
  eyebrow?: ReactNode;
  onSubmit: (e: FormEvent<HTMLFormElement>) => void;
  /** The fields. */
  children: ReactNode;
  /** Submit-time refusal, in the server's own words. */
  error?: string | null;
  /** The test's outcome, painted between the fields and the footer (13aj). */
  result?: ConnectionTestResult | null;
  /** The one faint line that states a limitation the board does not have. */
  note?: ReactNode;
  test: ConnectionAction;
  /** `submit` by default; a `button` primary closes a finished dialog. */
  primary: ConnectionAction & { type?: "submit" | "button" };
}

export function ConnectionDialog({
  open,
  onOpenChange,
  trigger,
  title,
  description,
  eyebrow,
  onSubmit,
  children,
  error,
  result,
  note,
  test,
  primary,
}: ConnectionDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent title={title} description={description} eyebrow={eyebrow}>
        <form onSubmit={onSubmit} className="space-y-3">
          {children}

          {error && (
            <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
              {error}
            </p>
          )}
          {result && <TestResultBanner result={result} />}
          {note && <p className="text-[11.5px] leading-relaxed text-text-faint">{note}</p>}

          {/* 13aj's footer: outline `↗ Test`, then the primary pill. No Cancel —
              the ✕ on the title line closes it, and a connection form is not a
              destructive decision that needs an explicit way out. */}
          <div className="flex items-center justify-end gap-2.5 pt-1">
            <ActionButton
              variant="secondary"
              state={test.state}
              busyLabel={test.busyLabel}
              successLabel={test.successLabel}
              failedLabel={test.failedLabel}
              disabledReason={test.disabledReason}
              onClick={test.onClick}
            >
              {test.label}
            </ActionButton>
            <ActionButton
              type={primary.type ?? "submit"}
              variant="primary"
              state={primary.state}
              busyLabel={primary.busyLabel}
              successLabel={primary.successLabel}
              failedLabel={primary.failedLabel}
              disabledReason={primary.disabledReason}
              onClick={primary.onClick}
            >
              {primary.label}
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ─── The notifier variant (2m) ───────────────────────────────────────────────

const CHANNELS: { value: CreateNotifierRequestChannel; label: string }[] = [
  { value: "discord", label: "Discord" },
  { value: "slack", label: "Slack" },
  { value: "telegram", label: "Telegram" },
  { value: "email", label: "Email" },
];

const EVENTS: { key: CreateNotifierRequestEventsItem; label: string }[] = [
  { key: "deploy.succeeded", label: "deploy.succeeded" },
  { key: "deploy.failed", label: "deploy.failed" },
  { key: "backup.succeeded", label: "backup.succeeded" },
  { key: "backup.failed", label: "backup.failed" },
];

const TEST_NOTE =
  "Tests send through the saved notifier: the panel attempts one delivery and logs any channel failure rather than surfacing it, so confirm in the channel itself.";

export function NotifierConnectionDialog({ projectId, trigger }: { projectId: string; trigger: ReactNode }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  // Set once Save commits; its presence is what switches the dialog to the
  // test step. The fields behind it are gone by then — nothing stale to resubmit.
  const [saved, setSaved] = useState<Notifier | null>(null);
  const [name, setName] = useState("");
  const [channel, setChannel] = useState<CreateNotifierRequestChannel>("discord");
  const [events, setEvents] = useState<Set<CreateNotifierRequestEventsItem>>(() => new Set(["deploy.failed"]));
  const [cfg, setCfg] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ConnectionTestResult | null>(null);

  const create = useCreateNotifier({
    mutation: {
      onSuccess: (n) => {
        void qc.invalidateQueries({ queryKey: getListNotifiersQueryKey(projectId) });
        setError(null);
        setSaved(n);
        toastSuccess(`${n.name} added`);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the notifier"),
    },
  });
  const test = useTestNotifier({
    mutation: {
      onSuccess: () =>
        setResult({
          tone: "sent",
          message: `Test sent to ${saved?.config_hint ?? "the channel"}. A channel failure is logged, not surfaced — check the channel itself.`,
        }),
      onError: (e: unknown) =>
        setResult({
          tone: "failed",
          message: `Test could not be sent — ${e instanceof Error ? e.message : "the panel did not answer"}.`,
        }),
    },
  });
  const saveState = useMutationActionState(create);
  const testState = useMutationActionState(test);

  const reset = () => {
    setSaved(null);
    setName("");
    setChannel("discord");
    setEvents(new Set(["deploy.failed"]));
    setCfg({});
    setError(null);
    setResult(null);
    create.reset();
    test.reset();
  };

  const setField = (k: string) => (e: ChangeEvent<HTMLInputElement>) => setCfg((c) => ({ ...c, [k]: e.target.value }));

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (saved) return;
    if (events.size === 0) {
      setError("Pick at least one event to notify on");
      return;
    }
    const config = channel === "email" ? { ...cfg, smtp_port: Number(cfg.smtp_port ?? "587") } : cfg;
    create.mutate({ id: projectId, data: { name, channel, events: [...events], config, enabled: true } });
  };

  return (
    <ConnectionDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
      trigger={trigger}
      title={saved ? "Notifier added" : "Add a notifier"}
      description={saved ? "Send a test to confirm the wiring before you rely on it." : "Where to send it, and which events matter."}
      onSubmit={submit}
      error={error}
      result={result}
      note={saved ? undefined : TEST_NOTE}
      test={{
        label: "↗ Test",
        state: testState,
        busyLabel: "Sending…",
        successLabel: "Sent",
        disabledReason: saved ? undefined : "Save first — a test sends through the saved notifier",
        onClick: () => {
          if (!saved) return;
          setResult(null);
          test.mutate({ id: saved.id });
        },
      }}
      primary={
        saved
          ? { label: "Done", state: "idle", busyLabel: "Closing…", type: "button", onClick: () => setOpen(false) }
          : { label: "Save", state: saveState, busyLabel: "Saving…", successLabel: "Saved" }
      }
    >
      {saved ? (
        <SavedSummary notifier={saved} />
      ) : (
        <>
          <Field label="Name" qualifier="· how it appears in the list">
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="team-alerts" />}
          </Field>
          <Field label="Channel">
            {(id) => (
              <Select
                id={id}
                value={channel}
                className="font-sans"
                onChange={(e) => {
                  setChannel(e.target.value as CreateNotifierRequestChannel);
                  setCfg({});
                }}
              >
                {CHANNELS.map((c) => (
                  <option key={c.value} value={c.value}>
                    {c.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>

          <ChannelFields channel={channel} value={cfg} onField={setField} />

          <fieldset>
            <legend className="block text-[12px] font-semibold text-text">
              Notify on <span className="font-normal text-text-faint">· pick at least one</span>
            </legend>
            <div className="mt-1.5 flex flex-wrap gap-[7px]">
              {EVENTS.map((ev) => (
                <ChoiceChip
                  key={ev.key}
                  selected={events.has(ev.key)}
                  onToggle={() =>
                    setEvents((prev) => {
                      const next = new Set(prev);
                      if (next.has(ev.key)) next.delete(ev.key);
                      else next.add(ev.key);
                      return next;
                    })
                  }
                  className="font-mono"
                >
                  {ev.label}
                </ChoiceChip>
              ))}
            </div>
          </fieldset>
        </>
      )}
    </ConnectionDialog>
  );
}

/** What was saved, in the masked terms the API hands back — never the secret. */
function SavedSummary({ notifier: n }: { notifier: Notifier }) {
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3">
      <p className="flex items-center gap-2">
        <span className="text-[13px] font-semibold text-text">{n.name}</span>
        <span className="mono text-[11px] text-text-faint">{n.channel}</span>
      </p>
      <p className="mono mt-0.5 truncate text-[12px] text-text-faint" title={n.config_hint}>
        {n.config_hint}
      </p>
      <p className="mt-1.5 flex flex-wrap gap-1.5">
        {n.events.map((ev) => (
          <span key={ev} className="mono rounded bg-raised px-2 py-px text-[10.5px] text-text">
            {ev}
          </span>
        ))}
      </p>
    </div>
  );
}

function ChannelFields({
  channel,
  value,
  onField,
}: {
  channel: CreateNotifierRequestChannel;
  value: Record<string, string>;
  onField: (k: string) => (e: ChangeEvent<HTMLInputElement>) => void;
}) {
  if (channel === "discord" || channel === "slack") {
    return (
      <Field
        label="Webhook URL"
        qualifier="· write-only"
        hint={`Create an incoming webhook in ${channel === "discord" ? "Discord" : "Slack"} and paste its URL. It is sealed on save and shown back masked.`}
      >
        {(id, describedBy) => (
          <Input
            id={id}
            aria-describedby={describedBy}
            required
            value={value.webhook_url ?? ""}
            onChange={onField("webhook_url")}
            placeholder="https://…"
            autoComplete="off"
          />
        )}
      </Field>
    );
  }
  if (channel === "telegram") {
    return (
      <div className="grid grid-cols-2 gap-3">
        <Field label="Bot token" qualifier="· write-only" hint="From @BotFather.">
          {(id, describedBy) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              type="password"
              required
              value={value.bot_token ?? ""}
              onChange={onField("bot_token")}
              autoComplete="new-password"
            />
          )}
        </Field>
        <Field label="Chat ID">{(id) => <Input id={id} required value={value.chat_id ?? ""} onChange={onField("chat_id")} />}</Field>
      </div>
    );
  }
  // email
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <Field label="SMTP host">{(id) => <Input id={id} required value={value.smtp_host ?? ""} onChange={onField("smtp_host")} />}</Field>
        <Field label="SMTP port">
          {(id) => <Input id={id} value={value.smtp_port ?? "587"} onChange={onField("smtp_port")} inputMode="numeric" />}
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Username">
          {(id) => <Input id={id} value={value.username ?? ""} onChange={onField("username")} autoComplete="off" />}
        </Field>
        <Field label="Password" qualifier="· write-only">
          {(id) => (
            <Input
              id={id}
              type="password"
              value={value.password ?? ""}
              onChange={onField("password")}
              autoComplete="new-password"
            />
          )}
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="From">{(id) => <Input id={id} required value={value.from ?? ""} onChange={onField("from")} placeholder="alerts@acme.com" />}</Field>
        <Field label="To" hint="Comma-separated.">
          {(id, describedBy) => <Input id={id} aria-describedby={describedBy} required value={value.to ?? ""} onChange={onField("to")} />}
        </Field>
      </div>
    </div>
  );
}
