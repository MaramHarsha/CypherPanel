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
        setNewPassword((res as { password?: string }).password ?? null);
      },
      onError: mutErr,
    },
  });

  return (
    <PageState query={db} isEmpty={() => false}>
      {(d) => {
        const running = d.status === "running";
        return (
          <div className="max-w-xl space-y-6">
            <dl className="space-y-2 rounded-md border border-border bg-surface p-4 text-[13px]">
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
              <Row label="Created">
                <span title={absoluteTime(d.created_at)}>{relativeTime(d.created_at)}</span>
              </Row>
            </dl>

            <section className="space-y-2">
              <Eyebrow>Lifecycle</Eyebrow>
              <div className="flex flex-wrap items-center gap-2">
                {running ? (
                  <Button size="sm" disabled={stop.isPending} onClick={() => stop.mutate({ id: dbId })}>
                    <Square className="h-3.5 w-3.5" /> Stop
                  </Button>
                ) : (
                  <Button size="sm" variant="primary" disabled={start.isPending} onClick={() => start.mutate({ id: dbId })}>
                    <Play className="h-3.5 w-3.5" /> Start
                  </Button>
                )}
                <ConfirmDestructive
                  trigger={
                    <Button size="sm" variant="secondary">
                      <RotateCcw className="h-3.5 w-3.5" /> Reset password
                    </Button>
                  }
                  title="Reset the root password?"
                  blastRadius="Generates a new root password and restarts the database. Anything using the current password — apps, backups, external clients — must be updated. The new password is shown once."
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
