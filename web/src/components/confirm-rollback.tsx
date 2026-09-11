// Rollback confirm — design canvas `9g` and its dark twin `13ae`: "confirm with
// the two revisions face to face".
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
//
// The confirm is deliberately NOT dressed as destructive: no red rule, no
// typed-name gate, an ink pill rather than the danger pill. Rolling back is
// what an operator does to *stop* an incident, and a UI that flinches at it
// costs minutes exactly when minutes are expensive.
import { Undo2 } from "lucide-react";
import { type ReactNode, useState } from "react";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

export interface RevisionSummary {
  /** Short sha, as displayed. */
  rev: string;
  /** Commit subject, or whatever the deployment recorded as its detail. */
  detail?: string;
  /**
   * How long this revision was (or has been) the one serving — "6 days"
   * (13ae: "served 6 days"). The canvas adds ", healthy"; nothing in the
   * deployment record can vouch for that, so the app does not say it.
   */
  served?: string;
}

export function ConfirmRollback({
  trigger,
  open: controlledOpen,
  onOpenChange,
  appName,
  now,
  target,
  onConfirm,
  pending,
}: {
  /**
   * The element that opens the confirm. Leave it out to drive the dialog from
   * your own control through `open`/`onOpenChange` — the row's ActionButton
   * does this, because a 6-state pill is a fragment of two elements and a
   * radix trigger can only wear one.
   */
  trigger?: ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  appName: string;
  /** What is serving right now. */
  now: RevisionSummary;
  /** What the rollback returns to. */
  target: RevisionSummary;
  onConfirm: () => void;
  pending?: boolean;
}) {
  const [ownOpen, setOwnOpen] = useState(false);
  const open = controlledOpen ?? ownOpen;
  const setOpen = (o: boolean) => {
    setOwnOpen(o);
    onOpenChange?.(o);
  };
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {trigger !== undefined && <DialogTrigger asChild>{trigger}</DialogTrigger>}
      {/* No ✕ on confirms (canvas 9g/9h) — Cancel is the way out. */}
      <DialogContent size="alert" hideClose title={`Roll back ${appName}?`}>
        {/* The comparison table is the one white surface inside the paper card:
            9g makes the modal the frame and the evidence the thing framed. */}
        <div className="mt-0.5 overflow-hidden rounded-lg border border-border bg-surface text-[12.5px]">
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
          <ActionButton
            variant="primary"
            state={pending ? "busy" : "idle"}
            busyLabel="Rolling back…"
            onClick={() => {
              onConfirm();
              // The modal steps aside immediately: the working toast (10c,
              // "Rolling back to 9be04d1…" → "Rolled back · no rebuild · env
              // vars unchanged") carries the outcome, and the deployment list
              // underneath is where the rollback then plays out — holding a
              // card over it hides that.
              setOpen(false);
            }}
          >
            <Undo2 className="h-3.5 w-3.5" aria-hidden /> Roll back to {target.rev}
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Row({ label, rev, detail, served, target }: RevisionSummary & { label: string; target?: boolean }) {
  return (
    <div
      className={cn(
        "flex items-center gap-2.5 px-3.5 py-2.5",
        // The rule between the two rows is the quiet in-card weight, not the
        // hairline that separates table rows: inside a bordered card the row
        // rule would compete with the card's own edge.
        target ? "bg-status-running/[0.05]" : "border-b border-border-subtle",
      )}
    >
      <span
        className={cn(
          "w-[52px] flex-none font-mono text-[10px] tracking-[0.08em]",
          target ? "text-status-running" : "text-text-faint",
        )}
      >
        {label}
      </span>
      <span className="flex-none font-mono text-[12.5px] font-medium text-text">{rev}</span>
      {(detail || served) && (
        <span className="min-w-0 truncate text-text-dim">
          {detail}
          {detail && served && " · "}
          {served && `served ${served}`}
        </span>
      )}
    </div>
  );
}
