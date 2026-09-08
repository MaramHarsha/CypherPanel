// Approving or rejecting a parked deploy.
//
// WHY THIS IS SHARED. The controls lived only on Project → Settings →
// Protection. The Deployments tab — the screen that SHOWS the deploy parked,
// the screen somebody is looking at when they notice it — offered Cancel and
// nothing else. So the person who saw the problem had to leave, find a
// settings page two levels away, and identify the deploy again by id.
//
// A gate whose release lives somewhere other than where the gate is visible is
// a gate people work around. The actions are the same in both places on
// purpose: one implementation, one set of confirmations, one behaviour.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useApproveDeployment, useRejectDeployment } from "@/api/gen/protection/protection";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

export function ApprovalActions({
  deploymentId,
  onSettled,
  size = "sm",
}: {
  deploymentId: string;
  /** Invalidate whatever list the caller is rendering. */
  onSettled: () => void;
  size?: "sm" | "md";
}) {
  const qc = useQueryClient();
  const done = (message: string) => {
    toastSuccess(message);
    onSettled();
    // The deployment's own status changed, so anything showing it is stale.
    void qc.invalidateQueries();
  };
  const approve = useApproveDeployment({
    mutation: {
      onSuccess: () => done("Approved — the deploy is on its way"),
      onError: (e: unknown) => toastFailed("Could not approve the deploy", e),
    },
  });
  const reject = useRejectDeployment({
    mutation: {
      onSuccess: () => done("Rejected"),
      onError: (e: unknown) => toastFailed("Could not reject the deploy", e),
    },
  });

  return (
    <span className="flex flex-wrap items-center gap-2">
      <ActionButton
        variant="primary"
        size={size}
        state={approve.isPending ? "busy" : "idle"}
        busyLabel="Approving…"
        onClick={() => approve.mutate({ id: deploymentId })}
      >
        Approve &amp; deploy
      </ActionButton>
      <RejectDialog
        busy={reject.isPending}
        size={size}
        onReject={(reason) => reject.mutate({ id: deploymentId, data: { reason } })}
      />
    </span>
  );
}

/**
 * A rejection carries a sentence.
 *
 * The requester is told why, and that is the whole content of the dialog: a
 * rejection with no reason is a deploy that stopped for reasons the person who
 * pushed it has to come and ask about.
 */
function RejectDialog({
  onReject,
  busy,
  size,
}: {
  onReject: (reason: string) => void;
  busy: boolean;
  size: "sm" | "md";
}) {
  const [reason, setReason] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onReject(reason.trim());
  };
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="ghost" size={size}>
          Reject
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Reject this deploy?"
        description="The person who pushed it sees your reason. Nothing is deployed and nothing is lost — the same commit can be deployed again once whatever this is about is settled."
      >
        <form onSubmit={submit} className="space-y-3">
          <Field label="Reason" qualifier="· shown to whoever pushed it">
            {(id) => (
              <Input
                id={id}
                autoFocus
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                placeholder="Waiting on the migration window"
              />
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton type="submit" variant="danger" size="lg" state={busy ? "busy" : "idle"} busyLabel="Rejecting…">
              Reject
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
