// Project settings · Deploy protection (canvas 7c, dark 13k). Two rules and a
// queue, per environment: who must approve a deploy here, when deploys are
// refused outright, and what is waiting right now.
//
// The canvas datelines this ATLAS-CRM / PRODUCTION / PROTECTION — the policy
// belongs to an environment, not to the project — so the environments are a
// filter across the top, exactly as they are on the project board. A project
// with one environment still shows it, because "which environment am I
// changing?" is the first thing this page has to answer.
import { createFileRoute } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import {
  getGetEnvironmentProtectionQueryKey,
  getListDeployApprovalsQueryKey,
  useApproveDeployment,
  useGetEnvironmentProtection,
  useListDeployApprovals,
  useRejectDeployment,
  useSetEnvironmentProtection,
} from "@/api/gen/protection/protection";
import { useListEnvironments } from "@/api/gen/projects/projects";
import type { DeployApproval, Environment, EnvironmentProtection } from "@/api/gen/model";
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

export const Route = createFileRoute("/_app/projects/$projectId/settings/protection")({
  component: ProtectionTab,
});

const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

/** "Fri 18:00" — the canvas writes a window's edges as day plus wall clock. */
function edge(dow: number, minutes: number) {
  const h = String(Math.floor(minutes / 60)).padStart(2, "0");
  const m = String(minutes % 60).padStart(2, "0");
  return `${DOW[dow] ?? "?"} ${h}:${m}`;
}

function ProtectionTab() {
  const { projectId } = Route.useParams();
  useCrumbs([{ label: "settings" }, { label: "protection" }]);
  const envs = useListEnvironments(projectId);
  const [envId, setEnvId] = useState<string | null>(null);
  const active = useMemo(() => {
    const list = envs.data ?? [];
    return list.find((e) => e.id === envId) ?? list[0];
  }, [envs.data, envId]);

  return (
    <div className="space-y-4">
      <PageState query={envs}>
        {(list) => (
          <>
            {/* Environments are a filter, not routes — the same decision the
                project board makes, so switching here reads the same way. */}
            <div className="-mb-px flex gap-1 overflow-x-auto" role="tablist" aria-label="Environments">
              {list.map((e: Environment) => (
                <button
                  key={e.id}
                  type="button"
                  role="tab"
                  aria-selected={active?.id === e.id}
                  onClick={() => setEnvId(e.id)}
                  className={cn(
                    "mono rounded-t-md border border-b-0 px-3.5 py-2 text-[11.5px] uppercase tracking-[.08em]",
                    active?.id === e.id
                      ? "-mb-px border-border bg-surface text-text"
                      : "border-transparent text-text-faint hover:text-text-mid",
                  )}
                >
                  {e.name}
                </button>
              ))}
            </div>
            {active ? <EnvironmentProtectionPanel env={active} /> : null}
          </>
        )}
      </PageState>
    </div>
  );
}

function EnvironmentProtectionPanel({ env }: { env: Environment }) {
  const policy = useGetEnvironmentProtection(env.id);
  const pending = useListDeployApprovals(env.id, { state: "pending" });
  return (
    <div className="space-y-5 rounded-lg border border-border bg-surface p-5">
      <PageState query={policy}>{(p) => <PolicyRules env={env} p={p} />}</PageState>
      <PageState query={pending}>
        {(list) =>
          list.length === 0 ? (
            <p className="mono text-[11.5px] text-text-faint">Nothing waiting for approval.</p>
          ) : (
            <div className="space-y-2">
              <Eyebrow>Pending approval</Eyebrow>
              <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border">
                {list.map((a) => (
                  <ApprovalRow key={a.deployment_id} a={a} envId={env.id} />
                ))}
              </ul>
            </div>
          )
        }
      </PageState>
    </div>
  );
}

// The two rules, each stated as the sentence the canvas writes rather than as a
// pair of unexplained switches: what the setting DOES is the label.
function PolicyRules({ env, p }: { env: Environment; p: EnvironmentProtection }) {
  const qc = useQueryClient();
  const set = useSetEnvironmentProtection({
    mutation: {
      onSuccess: () => {
        toastSuccess(`Updated protection for ${env.name}`);
        void qc.invalidateQueries({ queryKey: getGetEnvironmentProtectionQueryKey(env.id) });
      },
      onError: (e: unknown) => toastFailed("Could not update protection", e),
    },
  });

  function save(next: Partial<EnvironmentProtection>) {
    set.mutate({
      id: env.id,
      data: {
        require_approval: next.require_approval ?? p.require_approval,
        min_approver_role: next.min_approver_role ?? p.min_approver_role,
        freeze_enabled: next.freeze_enabled ?? p.freeze_enabled,
        windows: (next.windows ?? p.windows ?? []).map((w) => ({
          start_dow: w.start_dow,
          start_minute: w.start_minute,
          end_dow: w.end_dow,
          end_minute: w.end_minute,
          timezone: w.timezone,
        })),
      },
    });
  }

  return (
    <div className="space-y-5">
      <section className="space-y-1.5">
        <label className="flex items-start gap-2.5">
          <input
            type="checkbox"
            checked={p.require_approval}
            onChange={(e) => save({ require_approval: e.currentTarget.checked })}
            className="mt-0.5 size-3.5 accent-accent"
          />
          <span className="min-w-0">
            <span className="block text-[13px] font-semibold text-text">Require approval</span>
            <span className="block text-[12.5px] leading-[1.5] text-text-mid">
              Deploys to this environment wait for 1 approval from{" "}
              <select
                value={p.min_approver_role}
                onChange={(e) =>
                  save({ min_approver_role: e.currentTarget.value as EnvironmentProtection["min_approver_role"] })
                }
                className="mono rounded border border-border-input bg-surface px-1.5 py-0.5 text-[11.5px]"
                aria-label="Minimum approver role"
              >
                <option value="member">a member</option>
                <option value="admin">an admin</option>
                <option value="owner">an owner</option>
              </select>
              . Webhook deploys queue as “pending approval”.
            </span>
          </span>
        </label>
      </section>

      <section className="space-y-1.5">
        <label className="flex items-start gap-2.5">
          <input
            type="checkbox"
            checked={p.freeze_enabled}
            onChange={(e) => save({ freeze_enabled: e.currentTarget.checked })}
            className="mt-0.5 size-3.5 accent-accent"
          />
          <span className="min-w-0">
            <span className="block text-[13px] font-semibold text-text">Freeze window</span>
            {/* The windows themselves are the policy's teeth, so they are read
                out in full rather than summarised as a count. */}
            {(p.windows ?? []).length === 0 ? (
              <span className="block text-[12.5px] leading-[1.5] text-text-mid">
                No window declared yet — add one and deploys are refused inside it.
              </span>
            ) : (
              <span className="block text-[12.5px] leading-[1.5] text-text-mid">
                No deploys{" "}
                {(p.windows ?? []).map((w, i) => (
                  <span key={w.id}>
                    {i > 0 ? "; " : ""}
                    <span className="mono">
                      {edge(w.start_dow, w.start_minute)} → {edge(w.end_dow, w.end_minute)}
                    </span>{" "}
                    ({w.timezone})
                  </span>
                ))}
                . Owners can break glass — it’s audit-logged.
              </span>
            )}
          </span>
        </label>
        <div className="pl-6">
          <AddWindowDialog
            onAdd={(w) => save({ windows: [...(p.windows ?? []), w as EnvironmentProtection["windows"][number]] })}
            busy={set.isPending}
          />
        </div>
      </section>
    </div>
  );
}

// A window is four numbers and a zone; typing it as four numbers is how the
// canvas's "Fri 18:00 → Mon 08:00 (Europe/Berlin)" gets entered.
function AddWindowDialog({
  onAdd,
  busy,
}: {
  onAdd: (w: { start_dow: number; start_minute: number; end_dow: number; end_minute: number; timezone: string }) => void;
  busy: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [startDow, setStartDow] = useState(5);
  const [endDow, setEndDow] = useState(1);

  function toMinutes(v: string) {
    const [h = 0, m = 0] = v.split(":").map((n) => Number(n) || 0);
    return h * 60 + m;
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button type="button" className="text-[12px] font-medium text-text-mid hover:underline">
          Add a window
        </button>
      </DialogTrigger>
      <DialogContent title="Add freeze window" description="Deploys and rollbacks are refused inside it. A window may wrap the week.">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            onAdd({
              start_dow: startDow,
              start_minute: toMinutes(String(f.get("start") ?? "18:00")),
              end_dow: endDow,
              end_minute: toMinutes(String(f.get("end") ?? "08:00")),
              timezone: String(f.get("tz") ?? "UTC").trim() || "UTC",
            });
            setOpen(false);
          }}
          className="space-y-3"
        >
          <div className="grid grid-cols-2 gap-3">
            <Field label="From">
              {(id) => (
                <div className="flex gap-2">
                  <select
                    value={startDow}
                    onChange={(e) => setStartDow(Number(e.currentTarget.value))}
                    className="mono rounded-md border border-border-input bg-surface px-2 py-2 text-[12.5px]"
                    aria-label="Start day"
                  >
                    {DOW.map((d, i) => (
                      <option key={d} value={i}>
                        {d}
                      </option>
                    ))}
                  </select>
                  <Input id={id} name="start" type="time" defaultValue="18:00" className="mono" />
                </div>
              )}
            </Field>
            <Field label="Until">
              {(id) => (
                <div className="flex gap-2">
                  <select
                    value={endDow}
                    onChange={(e) => setEndDow(Number(e.currentTarget.value))}
                    className="mono rounded-md border border-border-input bg-surface px-2 py-2 text-[12.5px]"
                    aria-label="End day"
                  >
                    {DOW.map((d, i) => (
                      <option key={d} value={i}>
                        {d}
                      </option>
                    ))}
                  </select>
                  <Input id={id} name="end" type="time" defaultValue="08:00" className="mono" />
                </div>
              )}
            </Field>
          </div>
          <Field label="Time zone" qualifier="· the window is read in this zone, so it survives DST">
            {(id) => <Input id={id} name="tz" required className="mono" defaultValue="Europe/Berlin" />}
          </Field>
          <div className="flex justify-end gap-2.5 pt-1">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton type="submit" variant="primary" size="lg" state={busy ? "busy" : "idle"} busyLabel="Adding…">
              Add window
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// One parked deploy, with both decisions beside it. The canvas puts the commit,
// its message and who pushed it on the row, because that is what the decision
// is actually about — an approval with only a deployment id asks someone to
// vouch for a number.
function ApprovalRow({ a, envId }: { a: DeployApproval; envId: string }) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: getListDeployApprovalsQueryKey(envId) });
  };
  const approve = useApproveDeployment({
    mutation: {
      onSuccess: () => {
        toastSuccess("Approved — the deploy is on its way");
        refresh();
      },
      onError: (e: unknown) => toastFailed("Could not approve the deploy", e),
    },
  });
  const reject = useRejectDeployment({
    mutation: {
      onSuccess: () => {
        toastSuccess("Rejected");
        refresh();
      },
      onError: (e: unknown) => toastFailed("Could not reject the deploy", e),
    },
  });

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] text-text">
          <span className="mono font-medium">{a.deployment_id}</span>
          {a.requested_by_email ? <span className="text-text-mid"> · pushed by {a.requested_by_email}</span> : null}
        </span>
        <span className="mono mt-0.5 block text-[11.5px] text-text-faint">
          queued {relativeTime(a.created_at)} · needs {a.required_role}
        </span>
      </span>
      <ActionButton
        variant="primary"
        size="sm"
        state={approve.isPending ? "busy" : "idle"}
        busyLabel="Approving…"
        onClick={() => approve.mutate({ id: a.deployment_id })}
      >
        Approve &amp; deploy
      </ActionButton>
      <RejectDialog a={a} onReject={(reason) => reject.mutate({ id: a.deployment_id, data: { reason } })} busy={reject.isPending} />
    </li>
  );
}

// A rejection carries a sentence: the requester is told why, and the canvas
// makes that the whole content of the dialog.
function RejectDialog({ a, onReject, busy }: { a: DeployApproval; onReject: (reason: string) => void; busy: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button type="button" className="shrink-0 text-[12px] font-medium text-danger hover:underline">
          Reject
        </button>
      </DialogTrigger>
      <DialogContent title="Reject this deploy?" description="Your reason reaches whoever asked for it, and the decision is audit-logged.">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            onReject(String(f.get("reason") ?? "").trim());
            setOpen(false);
          }}
          className="space-y-3"
        >
          <Field label="Reason" qualifier="· what the requester will read">
            {(id) => <Input id={id} name="reason" required maxLength={500} placeholder="Frozen until the incident is closed." autoFocus />}
          </Field>
          <p className="mono text-[11px] text-text-faint">deployment {a.deployment_id}</p>
          <div className="flex justify-end gap-2.5">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton type="submit" variant="danger" size="lg" state={busy ? "busy" : "idle"} busyLabel="Rejecting…">
              Reject deploy
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
