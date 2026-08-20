// Database · Overview: engine/version, start/stop, and reset password — the
// new password is shown once (ui-principles §6).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Play, RotateCcw, Square } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import {
  getGetDatabaseQueryKey,
  useGetDatabase,
  useResetDatabasePassword,
  useStartDatabase,
  useStopDatabase,
} from "@/api/gen/databases/databases";
import { CopyField } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { relativeTime, absoluteTime } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/")({
  component: DatabaseOverview,
});

function DatabaseOverview() {
  const { dbId } = Route.useParams();
  const qc = useQueryClient();
  const db = useGetDatabase(dbId);
  const [newPassword, setNewPassword] = useState<string | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: getGetDatabaseQueryKey(dbId) });
  const start = useStartDatabase({ mutation: { onSuccess: invalidate, onError: mutErr } });
  const stop = useStopDatabase({ mutation: { onSuccess: invalidate, onError: mutErr } });
  const reset = useResetDatabasePassword({
    mutation: {
      onSuccess: (res) => {
        invalidate();
        // The field is root_password. This used to read `res.password` through
        // an `as` cast, which typed the mistake away: the value was always
        // undefined, so the one-time reveal silently showed nothing and the new
        // password was unrecoverable.
        setNewPassword(res.root_password);
      },
      onError: mutErr,
    },
  });

  return (
    <PageState query={db} isEmpty={() => false}>
      {(d) => {
        // Gate on INTENT, not observation. Start/stop are guarded server-side
        // on desired_state, so offering Start whenever the observed status
        // isn't "running" hands the operator a button that 400s every time the
        // agent is still converging (ADR-005: the two are allowed to differ).
        const wantsRunning = d.desired_state === "running";
        const converging = wantsRunning !== (d.status === "running");
        return (
          <div className="max-w-xl space-y-6">
            <dl className="space-y-2 rounded-lg border border-border bg-surface p-4 text-[13px]">
              <Row label="Status">
                <span className="flex items-center gap-2">
                  <StatusBadge status={d.status} />
                  {d.status_detail && <span className="text-xs text-text-mid">{d.status_detail}</span>}
                </span>
              </Row>
              <Row label="Engine">
                <span className="mono">
                  {d.engine} {d.version}
                </span>
              </Row>
              <Row label="Root user">
                <span className="mono">{d.root_user}</span>
              </Row>
              <Row label="Password required">
                <span className="mono">{d.require_password ? "yes" : "no"}</span>
              </Row>
              {/* Only shown when one was requested: an empty value means "the
                  engine's own default", which is a sentence, not a field. */}
              {d.initial_database && (
                <Row label="Application database">
                  <span className="mono">{d.initial_database}</span>
                </Row>
              )}
              <Row label="Created">
                <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
              </Row>
            </dl>

            <section className="space-y-2">
              <Eyebrow>Lifecycle</Eyebrow>
              <div className="flex flex-wrap items-center gap-2">
                {wantsRunning ? (
                  <Button size="sm" disabled={stop.isPending} onClick={() => stop.mutate({ id: dbId })}>
                    <Square className="h-3.5 w-3.5" /> Stop
                  </Button>
                ) : (
                  <Button size="sm" variant="primary" disabled={start.isPending} onClick={() => start.mutate({ id: dbId })}>
                    <Play className="h-3.5 w-3.5" /> Start
                  </Button>
                )}
                {/* Reality lags intent — say so rather than leaving the
                    operator to wonder why Stop is showing on a stopped box. */}
                {converging && (
                  <span className="font-mono text-[11px] text-text-faint">
                    {wantsRunning ? "starting…" : "stopping…"} agent is reconciling
                  </span>
                )}
                <ConfirmDestructive
                  trigger={
                    <Button size="sm" variant="secondary">
                      <RotateCcw className="h-3.5 w-3.5" /> Reset password
                    </Button>
                  }
                  title="Reset the root password?"
                  // The default lead announces a permanent removal, and a
                  // password reset removes nothing — so it states its own
                  // consequence instead, over the three things that follow
                  // from it.
                  lead="Resetting the password:"
                  blastRadius={[
                    "the current password stops working immediately",
                    "apps, backups and external clients using it must be updated by hand",
                    "the new password is shown once and then sealed",
                  ]}
                  actionLabel="Reset password"
                  pending={reset.isPending}
                  onConfirm={() => reset.mutate({ id: dbId })}
                />
              </div>
            </section>

            <Dialog open={newPassword !== null} onOpenChange={(o) => !o && setNewPassword(null)}>
              <DialogContent title="Copy the new password now" description="This is the only time it will be shown.">
                {newPassword && <CopyField value={newPassword} />}
                <div className="mt-4 flex justify-end">
                  <DialogClose asChild>
                    <Button variant="primary">Done</Button>
                  </DialogClose>
                </div>
              </DialogContent>
            </Dialog>
          </div>
        );
      }}
    </PageState>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <dt className="shrink-0 text-text-faint">{label}</dt>
      <dd className="min-w-0 text-right text-text">{children}</dd>
    </div>
  );
}

function mutErr(e: unknown) {
  toast.error(e instanceof Error ? e.message : "Something went wrong");
}
