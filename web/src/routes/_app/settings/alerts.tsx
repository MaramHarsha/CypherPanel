// Settings · Alerts (threshold-alerts.md §7, canvas 12f).
//
// A rule is a SENTENCE, and the sentence is rendered by the server from the
// row — so this list, the create modal's preview, the Discord message and the
// API all say the same words, and no label typed in March can drift from what
// the rule now does. Nothing here invents its own phrasing for a saved rule.
//
// The BACKTEST is the feature, and it runs before Create does anything: every
// alerting product treats "what number should I type" as the operator's problem
// and hands them nothing to solve it with, while this panel already keeps a
// fortnight of exactly the series the rule reads.
//
// All four states are shown, including the two that deliver nothing. A rule
// quiet because everything is fine and a rule quiet because it has not seen
// data since Tuesday must not look the same.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  backtestAlertRule,
  getListAlertRulesQueryKey,
  useCreateAlertRule,
  useDeleteAlertRule,
  useListAlertRules,
  useSetAlertRuleEnabled,
} from "@/api/gen/panel/panel";
import { useListApplications } from "@/api/gen/applications/applications";
import { useListNotifiers } from "@/api/gen/notifiers/notifiers";
import { useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type { AlertRule, CreateAlertRuleRequest } from "@/api/gen/model";
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

export const Route = createFileRoute("/_app/settings/alerts")({ component: AlertsTab });

type StateCopy = { label: string; tone: string; why: string };

const UNKNOWN_STATE: StateCopy = {
  label: "NO DATA",
  tone: "text-text-faint",
  why: "Not evaluating: the window has gaps, so it can neither fire nor say all-clear.",
};

const STATE_COPY: Record<string, StateCopy> = {
  ok: { label: "OK", tone: "text-status-running", why: "Below the threshold." },
  firing: { label: "FIRING", tone: "text-danger", why: "Above the threshold, and delivered." },
  no_data: {
    label: "NO DATA",
    tone: "text-text-faint",
    why: "Not evaluating: the window has gaps, so it can neither fire nor say all-clear.",
  },
  flapping: {
    label: "HELD",
    tone: "text-status-degraded-text",
    why: "Fired four times in an hour, so it is held until it settles. The threshold or the window is probably too tight.",
  },
};

function AlertsTab() {
  useCrumbs([{ label: "settings" }, { label: "alerts" }]);
  const rules = useListAlertRules();

  return (
    <div className="max-w-3xl space-y-4">
      <div className="flex items-center gap-3">
        <Eyebrow>Alerts</Eyebrow>
        <span className="ml-auto">
          <RuleDialog />
        </span>
      </div>

      <PageState
        query={rules}
        skeletonColumns="1fr auto"
        skeletonRows={3}
        empty={
          <EmptyState
            glyph="◔"
            title="No alert rules"
            hint="Tell a notifier when a server or an application crosses a line and stays there. The panel will show you what a rule would have done over the last week before you save it."
            action={<RuleDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((r: AlertRule) => (
              <RuleRow key={r.id} rule={r} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function RuleRow({ rule }: { rule: AlertRule }) {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: getListAlertRulesQueryKey() });
  const state = STATE_COPY[rule.state] ?? UNKNOWN_STATE;

  const toggle = useSetAlertRuleEnabled({
    mutation: {
      onSuccess: (r) => {
        invalidate();
        toastSuccess(r.enabled ? "Rule resumed" : "Rule paused");
      },
      onError: (e: unknown, vars) => toastFailed("Could not change the rule", e, { retry: () => toggle.mutate(vars) }),
    },
  });
  const del = useDeleteAlertRule({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess("Rule deleted");
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the rule", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        {/* The server's sentence, not a phrasing this page invented. */}
        <p className={cn("text-[13px] text-text", !rule.enabled && "opacity-60")}>{rule.sentence}</p>
        <p className="mono mt-0.5 text-[11px] text-text-faint">
          {rule.enabled ? (
            <>
              <span className={state.tone}>{state.label}</span> since {relativeTime(rule.state_since)} ·{" "}
              <span title={state.why}>{state.why}</span>
            </>
          ) : (
            <>paused · it keeps its history and its notifier and delivers nothing</>
          )}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <ActionButton
          size="sm"
          variant="ghost"
          state={toggle.isPending ? "busy" : "idle"}
          busyLabel="…"
          onClick={() => toggle.mutate({ id: rule.id, data: { enabled: !rule.enabled } })}
        >
          {rule.enabled ? "Pause" : "Resume"}
        </ActionButton>
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" aria-label="Delete rule">
              <Trash2 className="h-3.5 w-3.5 text-danger" aria-hidden />
            </Button>
          }
          title="Delete this alert rule?"
          lead={rule.sentence}
          blastRadius="Nothing will watch this signal after it is gone, and the rule's own history goes with it. Pausing keeps both."
          actionLabel="Delete rule"
          pending={del.isPending}
          pendingLabel="Deleting…"
          onConfirm={() => del.mutate({ id: rule.id })}
        />
      </div>
    </li>
  );
}

type Signal = { value: string; label: string; unit: string; suffix: string; appOnly?: boolean };

const UNKNOWN_STATE_SIGNAL: Signal = { value: "cpu", label: "CPU", unit: "percent", suffix: "%" };

// p95 and request rate are application-only, and the omission is substantive: a
// server's request buckets are the traffic that matched no route, so "p95 on
// this node" would silently mean "p95 of what hit nothing".
const SIGNALS: Signal[] = [
  { value: "cpu", label: "CPU", unit: "percent", suffix: "%" },
  { value: "memory", label: "Memory", unit: "percent", suffix: "%" },
  { value: "disk", label: "Disk", unit: "percent", suffix: "%" },
  { value: "p95_latency_ms", label: "p95 latency", unit: "milliseconds", suffix: "ms", appOnly: true },
  { value: "requests_per_second", label: "Request rate", unit: "per_second", suffix: "/s", appOnly: true },
];

const WINDOWS = [
  { value: 300, label: "5 minutes" },
  { value: 900, label: "15 minutes" },
  { value: 1800, label: "30 minutes" },
  { value: 3600, label: "1 hour" },
];

function RuleDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<"server" | "application">("server");
  const [targetId, setTargetId] = useState("");
  const [signal, setSignal] = useState<string>("cpu");
  const [threshold, setThreshold] = useState("90");
  const [windowSeconds, setWindowSeconds] = useState(900);
  const [notifierId, setNotifierId] = useState("");
  // One project select drives both the application picker and the notifier
  // picker, because notifiers ARE project-scoped while an alert rule is
  // panel-level. That asymmetry is real; hiding it behind an empty dropdown
  // would be worse than asking for the project once.
  const [projectId, setProjectId] = useState("");
  const [envId, setEnvId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [verdict, setVerdict] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);

  const servers = useListServers({ query: { enabled: open && kind === "server" } });
  const projects = useListProjects({ query: { enabled: open } });
  const envs = useListEnvironments(projectId, { query: { enabled: open && projectId !== "" } });
  const apps = useListApplications(envId, { query: { enabled: open && envId !== "" } });
  const notifiers = useListNotifiers(projectId, { query: { enabled: open && projectId !== "" } });

  const eligibleSignals = SIGNALS.filter((s) => kind === "application" || !s.appOnly);
  const chosen = SIGNALS.find((s) => s.value === signal) ?? UNKNOWN_STATE_SIGNAL;

  const body = (): CreateAlertRuleRequest => ({
    target_kind: kind,
    target_id: targetId,
    signal: signal as CreateAlertRuleRequest["signal"],
    threshold: Number(threshold),
    threshold_unit: chosen.unit as CreateAlertRuleRequest["threshold_unit"],
    window_seconds: windowSeconds,
    notifier_id: notifierId,
  });

  const create = useCreateAlertRule({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListAlertRulesQueryKey() });
        setOpen(false);
        setVerdict(null);
        toastSuccess("Alert rule created");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the rule"),
    },
  });

  // The backtest runs on demand rather than on every keystroke: it is a range
  // scan, and firing one per digit typed would be a query storm from a form.
  const check = async () => {
    if (!targetId || !notifierId) {
      setError("Pick a target and a notifier first.");
      return;
    }
    setChecking(true);
    setError(null);
    try {
      const res = await backtestAlertRule(body());
      setVerdict(res.summary);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not run the backtest");
    } finally {
      setChecking(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!targetId) {
      setError("Pick something to watch.");
      return;
    }
    if (!notifierId) {
      setError("Pick where the alert should go.");
      return;
    }
    setError(null);
    create.mutate({ data: body() });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setVerdict(null);
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> New rule
        </Button>
      </DialogTrigger>
      <DialogContent
        title="New alert rule"
        description="Tell a notifier when something crosses a line and stays there. A brief spike does not fire — the whole window has to breach."
      >
        <form onSubmit={submit} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Watch">
              {(id) => (
                <select
                  id={id}
                  value={kind}
                  onChange={(e) => {
                    const next = e.target.value as "server" | "application";
                    setKind(next);
                    setTargetId("");
                    if (next === "server" && (signal === "p95_latency_ms" || signal === "requests_per_second")) {
                      setSignal("cpu");
                    }
                  }}
                  className={SELECT}
                >
                  <option value="server">A server</option>
                  <option value="application">An application</option>
                </select>
              )}
            </Field>
            <Field label="Project" hint="Notifiers belong to a project, and an alert sends to one.">
              {(id) => (
                <select
                  id={id}
                  value={projectId}
                  onChange={(e) => {
                    setProjectId(e.target.value);
                    setEnvId("");
                    setNotifierId("");
                    if (kind === "application") setTargetId("");
                  }}
                  className={SELECT}
                >
                  <option value="">Pick a project…</option>
                  {(projects.data ?? []).map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>

          {kind === "server" ? (
            <Field label="Server">
              {(id) => (
                <select id={id} value={targetId} onChange={(e) => setTargetId(e.target.value)} className={SELECT}>
                  <option value="">Pick one…</option>
                  {(servers.data ?? []).map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Environment">
                {(id) => (
                  <select
                    id={id}
                    value={envId}
                    onChange={(e) => {
                      setEnvId(e.target.value);
                      setTargetId("");
                    }}
                    disabled={projectId === ""}
                    className={SELECT}
                  >
                    <option value="">{projectId === "" ? "Pick a project first" : "Pick one…"}</option>
                    {(envs.data ?? []).map((e) => (
                      <option key={e.id} value={e.id}>
                        {e.name}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
              <Field label="Application">
                {(id) => (
                  <select
                    id={id}
                    value={targetId}
                    onChange={(e) => setTargetId(e.target.value)}
                    disabled={envId === ""}
                    className={SELECT}
                  >
                    <option value="">{envId === "" ? "Pick an environment first" : "Pick one…"}</option>
                    {(apps.data ?? []).map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Signal">
              {(id) => (
                <select
                  id={id}
                  value={signal}
                  onChange={(e) => setSignal(e.target.value)}
                  className={SELECT}
                >
                  {eligibleSignals.map((s) => (
                    <option key={s.value} value={s.value}>
                      {s.label}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="Above">
              {(id) => (
                <div className="flex items-center gap-1.5">
                  <Input
                    id={id}
                    type="number"
                    min={1}
                    value={threshold}
                    onChange={(e) => setThreshold(e.target.value)}
                    className="mono"
                  />
                  <span className="mono shrink-0 text-[12px] text-text-faint">{chosen.suffix}</span>
                </div>
              )}
            </Field>
            <Field label="For">
              {(id) => (
                <select
                  id={id}
                  value={windowSeconds}
                  onChange={(e) => setWindowSeconds(Number(e.target.value))}
                  className={SELECT}
                >
                  {WINDOWS.map((w) => (
                    <option key={w.value} value={w.value}>
                      {w.label}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>

          {/* The denominator, named inline: same signal, two meanings. */}
          {signal === "cpu" && (
            <p className="text-[12px] leading-[1.5] text-text-faint">
              {kind === "server"
                ? "Percent of all the host's cores — the number a person means by “the box is at 90%”."
                : "Percent of one core, uncapped: a container pinned across two cores reads 190%."}
            </p>
          )}
          {signal === "memory" && kind === "application" && (
            <p className="text-[12px] leading-[1.5] text-text-faint">
              Percent of the application's memory limit. An application with no limit has no honest denominator, so
              this rule will read “no data” for it until one is set.
            </p>
          )}

          <Field label="Send it to" hint="The rule delivers here. Two rules over one signal is how you page on-call and post to a channel.">
            {(id) => (
              <select
                id={id}
                value={notifierId}
                onChange={(e) => setNotifierId(e.target.value)}
                disabled={projectId === ""}
                className={SELECT}
              >
                <option value="">{projectId === "" ? "Pick a project first" : "Pick a notifier…"}</option>
                {(notifiers.data ?? []).map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name} ({n.channel})
                  </option>
                ))}
              </select>
            )}
          </Field>

          {/* The backtest sits directly above the save button, because that is
              where the question it answers is being asked. */}
          <div className="rounded-md border border-border bg-pane px-3.5 py-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-[12.5px] text-pane-text">
                {verdict ?? "See what this rule would have done over the last 7 days, before you save it."}
              </p>
              <ActionButton
                type="button"
                size="sm"
                variant="secondary"
                state={checking ? "busy" : "idle"}
                busyLabel="Checking…"
                onClick={check}
              >
                {verdict ? "Check again" : "Check"}
              </ActionButton>
            </div>
          </div>

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
              Create alert
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const SELECT =
  "w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none";
