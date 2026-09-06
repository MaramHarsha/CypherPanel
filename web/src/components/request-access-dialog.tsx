// The ask half of invitations-and-access-requests.md — the mirror image of an
// invitation, and the one thing the 403 screen has always promised but could
// not do: a member asks this team's owners for a higher rank, in their own
// words, without leaving the panel.
//
// It replaces a mailto. The mailto was honest while there was no API; there is
// one now, and an in-panel request is better for the reason the spec gives —
// granting applies through the existing member-role path, so the last-owner
// guard holds, and the decision is audit-logged with the requester notified
// either way. A mail to an owner's inbox is none of those things.
//
// The message is the whole substance of the decision, so the field is the body
// of the dialog rather than an optional afterthought: "someone wants developer"
// is not something an owner can act on. It is still optional, because the API
// makes it so and a refusal to send without one would be the panel inventing a
// rule.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import {
  getListAccessRequestsQueryKey,
  useCreateAccessRequest,
} from "@/api/gen/access-requests/access-requests";
import { CreateAccessRequestRequestRequestedRole } from "@/api/gen/model";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Textarea } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

type Requestable = (typeof CreateAccessRequestRequestRequestedRole)[keyof typeof CreateAccessRequestRequestRequestedRole];

export function RequestAccessDialog({
  open,
  onOpenChange,
  teamId,
  teamName,
  /** The rank the refused route wanted — what the request asks for. */
  role,
  /** What the caller holds today, so the dialog states the delta rather than
   *  only the destination. */
  held,
  /** Who decides. Named because a request that disappears into "the owners" is
   *  a request nobody can chase. */
  owners,
  /** What was refused, as a gerund — it seeds the message so the ask is already
   *  written when the dialog opens. */
  action,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  teamId: string;
  teamName: string | undefined;
  role: Requestable;
  held: string | undefined;
  owners: string[];
  action: string;
}) {
  const qc = useQueryClient();
  const [message, setMessage] = useState(`${action} needs the ${role} role, and I need to do it.`);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateAccessRequest({
    mutation: {
      onSuccess: () => {
        // The owners' own copy of this list is a different session, but a
        // second tab of ours is not — and a duplicate request answers 409.
        void qc.invalidateQueries({ queryKey: getListAccessRequestsQueryKey(teamId) });
        toastSuccess({
          title: "Request sent",
          detail: owners[0]
            ? `${owners.length === 1 ? owners[0] : `${owners[0]} and ${owners.length - 1} other${owners.length === 2 ? "" : "s"}`} can grant it. You'll be notified either way.`
            : "The team's owners can grant it. You'll be notified either way.",
        });
        onOpenChange(false);
      },
      onError: (e: unknown) => {
        setError(e instanceof Error ? e.message : "Could not send the request");
        toastFailed("Could not send the request", e);
      },
    },
  });
  const state = useMutationActionState(create);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ id: teamId, data: { requested_role: role, message: message.trim() || undefined } });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        title={`Ask for ${role}${teamName ? ` on ${teamName}` : ""}`}
        description={
          held
            ? `You're a ${held} on this team. An owner can raise you to ${role} — the decision, either way, is audit-logged and you're notified.`
            : `An owner can grant you ${role} on this team. The decision, either way, is audit-logged and you're notified.`
        }
      >
        <form onSubmit={submit} className="space-y-4">
          <Field
            label="Why"
            qualifier="· the owners read this"
            hint="Optional, and the one thing that makes the request decidable rather than a name and a rank."
            error={error ?? undefined}
          >
            {(id, describedBy) => (
              <Textarea
                id={id}
                autoFocus
                maxLength={500}
                aria-describedby={describedBy}
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                className="min-h-24"
              />
            )}
          </Field>
          {owners.length > 0 && (
            <p className="mono text-[11.5px] leading-[1.6] text-text-faint">
              goes to {owners.join(", ")}
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
              state={state}
              busyLabel="Sending…"
              successLabel="Sent"
            >
              Send request
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
