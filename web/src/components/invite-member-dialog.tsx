// "+ Invite member" (canvas 9d/13ab): the role explained at the moment of
// choice, and the invitation the canvas always drew.
//
// This dialog used to add an account that already existed, because
// teams-and-roles.md §7 had put invitations by email out of scope — the note in
// the footer said so, and promised that "when invitations land, the title, the
// note and the verb change here and nowhere else". They have landed
// (invitations-and-access-requests.md), so this is that change: the canvas's
// "Send invite →" over "invite link expires in 7 days" is now literally true.
//
// The accept URL is readable exactly once, in the create response, whether or
// not mail went out — so it is shown after sending rather than assumed
// delivered. A panel with no SMTP still gets a link to hand over, which is the
// whole reason the API returns it.
//
// Roles are the API's own enum: member / admin / owner. The canvas's Developer
// and Viewer do not exist server-side, and a card for a role the server would
// refuse is a promise the panel cannot keep — so the member card carries the
// Developer line, which is what a member actually is.
import { useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { CreateInviteRequestRole, type CreatedInvite, type Team } from "@/api/gen/model";
import { getListTeamInvitesQueryKey, useCreateTeamInvite } from "@/api/gen/invites/invites";
import { CopyButton } from "@/components/copy-field";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

// A role is a choice you make once and live with, so the three options are laid
// out with their consequences beside them rather than folded into a closed
// dropdown under one sentence that describes all three. The lines follow the
// rank table in teams-and-roles.md §1: what each rank adds, and where it stops.
const TEAM_ROLES: ReadonlyArray<{ value: CreateInviteRequestRole; label: string; help: string }> = [
  { value: CreateInviteRequestRole.member, label: "Member", help: "deploy, rollback, env vars, logs — no member or team changes" },
  { value: CreateInviteRequestRole.admin, label: "Admin", help: "member, plus creating and deleting projects and managing members below owner" },
  { value: CreateInviteRequestRole.owner, label: "Owner", help: "everything, including renaming or deleting the team and promoting other owners" },
];

function RoleCards({
  name,
  value,
  onChange,
}: {
  name: string;
  value: CreateInviteRequestRole;
  onChange: (value: CreateInviteRequestRole) => void;
}) {
  return (
    <div className="flex flex-col gap-[7px]">
      {TEAM_ROLES.map((o) => {
        const on = o.value === value;
        return (
          <label
            key={o.value}
            className={cn(
              // The painted card has to carry the focus ring itself: the radio
              // that actually holds focus is clipped to a 1px box, so the ring
              // the browser draws on it lands nowhere the eye can find.
              "flex cursor-pointer items-baseline gap-2.5 rounded-lg bg-surface px-[13px] py-2.5",
              "has-[input:focus-visible]:outline-2 has-[input:focus-visible]:outline-offset-[3px] has-[input:focus-visible]:outline-focus",
              on ? "border-[1.5px] border-border-strong" : "border border-border hover:border-border-strong",
            )}
          >
            {/* The real radio stays in the DOM, unpainted: it carries the
                keyboard behaviour and the accessible name that a div would
                have to re-invent badly. */}
            <input
              type="radio"
              name={name}
              value={o.value}
              checked={on}
              onChange={() => onChange(o.value)}
              className="sr-only"
            />
            <span
              className={cn(
                "h-2 w-2 shrink-0 self-center rounded-full",
                on ? "bg-primary" : "border-[1.5px] border-border-input",
              )}
              aria-hidden
            />
            <span className={cn("w-[84px] shrink-0 text-[13px] font-semibold", on ? "text-text" : "text-text-mid")}>
              {o.label}
            </span>
            <span className={cn("text-[12px] leading-[1.45]", on ? "text-text-mid" : "text-text-faint")}>{o.help}</span>
          </label>
        );
      })}
    </div>
  );
}

// After sending. The link is the whole point of this state: it is readable
// exactly once, and a panel with no SMTP has no other way to pass it on — so it
// is shown as a copyable machine value rather than described.
function InviteSent({ created, onDone }: { created: CreatedInvite; onDone: () => void }) {
  return (
    <div className="space-y-3.5">
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        {created.mail_sent ? (
          <>
            Emailed to <span className="mono">{created.invite.email}</span>. The link below is the same one — copy it if
            you would rather send it yourself.
          </>
        ) : (
          <>
            This panel has no mail configured, so nothing was sent. The invitation is valid — hand this link over
            yourself.
          </>
        )}
      </p>
      <div className="flex items-center gap-2 rounded-md border border-pane-border bg-pane px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-pane-text" title={created.accept_url}>
          {created.accept_url}
        </span>
        <span className="-mr-1 shrink-0">
          <CopyButton value={created.accept_url} label="Copy the invitation link" />
        </span>
      </div>
      <p className="mono text-[11px] text-text-faint">
        readable once · expires in 7 days · role {created.invite.role}
      </p>
      <div className="flex justify-end">
        <Button type="button" variant="primary" size="lg" onClick={onDone}>
          Done
        </Button>
      </div>
    </div>
  );
}

export function InviteMemberDialog({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<CreateInviteRequestRole>(CreateInviteRequestRole.member);
  const [error, setError] = useState<string | null>(null);
  // The link the invitee needs. Held here because the response is the only
  // place it is ever readable — nothing stores it, so navigating away loses it.
  const [sent, setSent] = useState<CreatedInvite | null>(null);
  const add = useCreateTeamInvite({
    mutation: {
      onSuccess: (created) => {
        void qc.invalidateQueries({ queryKey: getListTeamInvitesQueryKey(teamId) });
        toastSuccess(created.mail_sent ? `Invitation sent to ${created.invite.email}` : "Invitation created");
        setSent(created);
      },
      // The pill turns to "✕ Retry" and the toast carries the why (10b/10c);
      // the inline line keeps the server's sentence beside the field it is
      // about — "no account with that email" belongs under Email.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not send the invitation");
        toastFailed("Could not send the invitation", e, { retry: () => add.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(add);

  // Opening resets the mutation as well as the fields, so a reopened modal
  // never inherits the last attempt's "✓ Added" or "✕ Retry" pill.
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      add.reset();
      return;
    }
    setEmail("");
    setRole(CreateInviteRequestRole.member);
    setError(null);
    setSent(null);
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    add.mutate({ id: teamId, data: { email: email.trim(), role } });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm">
          <Plus className="h-3.5 w-3.5" aria-hidden /> Invite member
        </Button>
      </DialogTrigger>
      {/* Naming the team in the title is the whole answer to "invite them
          where?" — this dialog opens from a row in a list of teams. */}
      <DialogContent title={`Invite to ${team.name}`}>
        {sent ? (
          <InviteSent created={sent} onDone={() => onOpenChange(false)} />
        ) : (
        <form onSubmit={submit} className="space-y-3.5">
          <fieldset disabled={add.isPending} className="min-w-0 space-y-3.5">
            <Field label="Email" error={error ?? undefined}>
              {(id, describedBy) => (
                <Input
                  id={id}
                  type="email"
                  required
                  autoFocus
                  autoComplete="off"
                  aria-describedby={describedBy}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="priya@example.com"
                />
              )}
            </Field>
            <fieldset>
              <legend className="mb-[7px] text-[12px] font-semibold text-text">Role</legend>
              <RoleCards name={`team-role-${teamId}`} value={role} onChange={setRole} />
            </fieldset>
          </fieldset>
          <div className="flex items-center justify-end gap-2.5 pt-0.5">
            {/* The canvas's "invite link expires in 7 days" slot, holding the
                one thing that is true instead: no email goes out, and the
                account is made in Settings → Users. */}
            <span className="mr-auto text-[11.5px] leading-snug text-text-faint">invite link expires in 7 days</span>
            <ActionButton
              type="submit"
              variant="accent"
              state={state}
              busyLabel="Sending…"
              successLabel="Sent"
              disabledReason={email.trim() === "" ? "Enter their email first" : undefined}
            >
              Send invite →
            </ActionButton>
          </div>
        </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
