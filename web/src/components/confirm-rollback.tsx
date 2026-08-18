// Rollback confirm — design canvas `13ae` (dark twin of 9g): "confirm with the
// two revisions face to face".
//
// Rollback used to fire straight from the row's click with nothing in between,
// which is the one production action in the panel that changes what is serving
// without asking. The canvas puts the two revisions side by side because the
// question an operator actually has is "what am I going back *to*", and a
// short sha in a button does not answer it.
//
// The second paragraph is the one that earns the modal: **env vars are
// today's**. A rollback re-deploys the old image through the same
// zero-downtime pipeline, but configuration does not rewind — so "roll back to
// Tuesday" does not reproduce Tuesday, and that surprise belongs before the
// click rather than in an incident review.
import { type ReactNode, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";

export interface RevisionSummary {
  /** Short sha, as displayed. */
  rev: string;
  /** Commit subject, or whatever the deployment recorded as its detail. */
  detail?: string;
}

export function ConfirmRollback({
  trigger,
  appName,
  now,
  target,
  onConfirm,
  pending,
}: {
  trigger: ReactNode;
  appName: string;
  /** What is serving right now. */
  now: RevisionSummary;
  /** What the rollback returns to. */
  target: RevisionSummary;
  onConfirm: () => void;
  pending?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      {/* No ✕ on confirms (canvas 9g/9h) — Cancel is the way out. */}
      <DialogContent size="alert" hideClose title={`Roll back ${appName}?`}>
        <div className="mt-1 overflow-hidden rounded-lg border border-border bg-surface text-[12.5px]">
          <Row label="NOW" {...now} />
          <Row label="TARGET" {...target} target />
        </div>

        <p className="mt-3 text-[12.5px] leading-[1.55] text-text-mid">
          The old image redeploys through the normal zero-downtime pipeline — no rebuild.{" "}
          <b className="text-text">Env vars are today's, not last week's</b> — a rollback doesn't rewind
          configuration.
        </p>

        <div className="mt-4 flex justify-end gap-2.5">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            variant="primary"
            disabled={pending}
            onClick={() => {
              onConfirm();
              setOpen(false);
            }}
          >
            ↺ Roll back to {target.rev}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Row({ label, rev, detail, target }: RevisionSummary & { label: string; target?: boolean }) {
  return (
    <div
      className={
        "flex items-center gap-2.5 px-3.5 py-2.5 " +
        (target ? "bg-status-running/[0.05]" : "border-b border-border")
      }
    >
      <span
        className={
          "mono w-[52px] flex-none text-[10px] tracking-[0.08em] " +
          (target ? "text-status-running" : "text-text-faint")
        }
      >
        {label}
      </span>
      <span className="mono flex-none text-[12.5px] font-medium text-text">{rev}</span>
      {detail && <span className="min-w-0 truncate text-text-mid">{detail}</span>}
    </div>
  );
}
