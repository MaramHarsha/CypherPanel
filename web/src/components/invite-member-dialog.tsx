// "+ Add member" (canvas 9d/13ab): the role explained at the moment of choice.
//
// The canvas draws an email invitation — "Send invite →" over a note that the
// link expires in seven days. The API has no such thing: teams-and-roles.md §7
// puts invitations by email out of scope, and POST /teams/{id}/members adds an
// account that already exists, by its email. So this keeps the canvas's shape —
// email, three role cards with their consequences beside them, a quiet footer
// note where the expiry would go — and tells the truth in its words: nothing
// is sent, and the account has to exist first. When invitations land, the
// title, the note and the verb change here and nowhere else.
//
// Roles are the API's own enum (AddMemberRequestRole): member / admin / owner.
// The canvas's Developer and Viewer do not exist server-side, and a card for a
// role the server would refuse is a promise the panel cannot keep — so the
// member card carries the Developer line, which is what a member actually is.
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { AddMemberRequestRole, type Team } from "@/api/gen/model";
import { getListTeamMembersQueryKey, useAddTeamMember } from "@/api/gen/teams/teams";
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
const TEAM_ROLES: ReadonlyArray<{ value: AddMemberRequestRole; label: string; help: string }> = [
  { value: AddMemberRequestRole.member, label: "Member", help: "deploy, rollback, env vars, logs — no member or team changes" },
  { value: AddMemberRequestRole.admin, label: "Admin", help: "member, plus creating and deleting projects and managing members below owner" },
  { value: AddMemberRequestRole.owner, label: "Owner", help: "everything, including renaming or deleting the team and promoting other owners" },
];

function RoleCards({
  name,
  value,
  onChange,
}: {
  name: string;
  value: AddMemberRequestRole;
  onChange: (value: AddMemberRequestRole) => void;
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

export function InviteMemberDialog({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<AddMemberRequestRole>(AddMemberRequestRole.member);
  const [error, setError] = useState<string | null>(null);
  const add = useAddTeamMember({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamMembersQueryKey(teamId) });
        toastSuccess("Member added");
        setOpen(false);
      },
      // The pill turns to "✕ Retry" and the toast carries the why (10b/10c);
      // the inline line keeps the server's sentence beside the field it is
      // about — "no account with that email" belongs under Email.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not add the member");
        toastFailed("Could not add the member", e, { retry: () => add.mutate(vars) });
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
    setRole(AddMemberRequestRole.member);
    setError(null);
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
          <Plus className="h-3.5 w-3.5" aria-hidden /> Add member
        </Button>
      </DialogTrigger>
      {/* Naming the team in the title is the whole answer to "add them where?" —
          this dialog opens from a row in a list of teams. */}
      <DialogContent title={`Add to ${team.name}`}>
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
            <span className="mr-auto text-[11.5px] leading-snug text-text-faint">
              no email is sent — they need an account first (
              <Link to="/settings/users" className="underline-offset-2 hover:text-text hover:underline">
                Settings → Users
              </Link>
              )
            </span>
            <ActionButton
              type="submit"
              variant="accent"
              state={state}
              busyLabel="Adding…"
              successLabel="Added"
              disabledReason={email.trim() === "" ? "Enter their email first" : undefined}
            >
              Add member →
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
