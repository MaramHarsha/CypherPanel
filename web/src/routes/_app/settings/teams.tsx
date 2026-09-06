// Settings · Teams: the tenancy boundary. Create teams, manage members. The
// last-owner guard's 409 is surfaced verbatim (web-ui-design.md §4).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2, Users } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  getListTeamMembersQueryKey,
  getListTeamsQueryKey,
  useCreateTeam,
  useListTeamMembers,
  useListTeams,
  useRemoveTeamMember,
} from "@/api/gen/teams/teams";
import type { AccessRequest, Team, TeamInvite, TeamMember } from "@/api/gen/model";
import { getListTeamInvitesQueryKey, useListTeamInvites, useRevokeTeamInvite } from "@/api/gen/invites/invites";
import {
  getListAccessRequestsQueryKey,
  useDenyAccessRequest,
  useGrantAccessRequest,
  useListAccessRequests,
} from "@/api/gen/access-requests/access-requests";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InviteMemberDialog } from "@/components/invite-member-dialog";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/teams")({ component: TeamsTab });

function TeamsTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "teams" }]);
  const teams = useListTeams();
  const [openId, setOpenId] = useState<string | null>(null);

  return (
    <div className="max-w-xl space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Teams</Eyebrow>
        <CreateTeamDialog />
      </div>
      <p className="text-[13px] text-text-mid">
        A team owns servers and projects. Members share access to everything the team owns, with a role that sets what they can change.
      </p>
      <PageState
        query={teams}
        empty={<EmptyState title="No teams" hint="Create a team to group projects and add people to it." action={<CreateTeamDialog primary />} />}
      >
        {(list) => (
          <ul className="space-y-2">
            {list.map((t) => (
              <TeamCard key={t.id} team={t} open={openId === t.id} onToggle={() => setOpenId((id) => (id === t.id ? null : t.id))} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function TeamCard({ team, open, onToggle }: { team: Team; open: boolean; onToggle: () => void }) {
  return (
    <li className="rounded-lg border border-border bg-surface">
      <button type="button" aria-expanded={open} onClick={onToggle} className="flex w-full items-center justify-between gap-2 px-4 py-3 text-left">
        <span className="flex items-center gap-2">
          <Users className="h-4 w-4 text-text-faint" aria-hidden />
          <span className="text-[13px] font-medium text-text">{team.name}</span>
          {team.role && <span className="mono text-[11px] text-text-faint">{team.role}</span>}
        </span>
        <span className="mono text-xs text-text-faint">created {relativeTime(team.created_at)}</span>
      </button>
      {open && (
        <>
          <MembersList team={team} />
          {/* An invitation is a member who has not arrived yet, and an access
              request is one asking to be more — both belong beside the member
              list rather than on pages of their own
              (invitations-and-access-requests.md §6). */}
          <PendingInvites team={team} />
          <AccessRequests team={team} />
        </>
      )}
    </li>
  );
}

function MembersList({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const members = useListTeamMembers(teamId);
  const remove = useRemoveTeamMember({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamMembersQueryKey(teamId) });
        toastSuccess("Member removed");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the member", e, { retry: () => remove.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-2 border-t border-border p-3">
      <div className="flex items-center justify-between">
        <h3 className="eyebrow">Members</h3>
        {/* 9d/13ab's "+ Invite member" pill, at the team it acts on. */}
        <InviteMemberDialog team={team} />
      </div>
      <PageState query={members} loading={<div className="text-xs text-text-faint">Loading…</div>}>
        {(list) => (
          <ul className="divide-y divide-border rounded-md border border-border">
            {list.map((m: TeamMember) => (
              <li key={m.user_id} className="flex items-center justify-between gap-2 px-3 py-2">
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate text-[13px] text-text">{m.email}</span>
                  <span className="mono text-[11px] text-text-faint">{m.role}</span>
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove ${m.email}`}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate({ id: teamId, uid: m.user_id })}
                >
                  <Trash2 className="h-3.5 w-3.5 text-danger" />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

// Invitations that have not been accepted yet. Only pending ones are listed:
// a spent or expired invitation is history, and history belongs to the audit
// log rather than to the surface where people are managed.
function PendingInvites({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const invites = useListTeamInvites(teamId, { state: "pending" });
  const revoke = useRevokeTeamInvite({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamInvitesQueryKey(teamId) });
        toastSuccess("Invitation revoked");
      },
      onError: (e: unknown, vars) => toastFailed("Could not revoke the invitation", e, { retry: () => revoke.mutate(vars) }),
    },
  });

  if (invites.isPending || invites.isError || (invites.data ?? []).length === 0) return null;
  return (
    <div className="space-y-2 border-t border-border p-3">
      <h3 className="eyebrow">Invited</h3>
      <ul className="divide-y divide-border rounded-md border border-border">
        {(invites.data ?? []).map((inv: TeamInvite) => (
          <li key={inv.id} className="flex items-center justify-between gap-2 px-3 py-2">
            <span className="flex min-w-0 flex-col">
              <span className="truncate text-[13px] text-text">{inv.email}</span>
              <span className="mono text-[11px] text-text-faint">
                {inv.role} · invited by {inv.invited_by_label} · expires {relativeTime(inv.expires_at)}
              </span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              aria-label={`Revoke the invitation for ${inv.email}`}
              disabled={revoke.isPending}
              onClick={() => revoke.mutate({ id: teamId, inv: inv.id })}
            >
              <Trash2 className="h-3.5 w-3.5 text-danger" />
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}

// The mirror image of an invitation (canvas 13ah): a member asking this team's
// owners for a higher rank, in their own words. The message is the whole
// substance of the decision, so it is rendered rather than truncated to a
// count — "reports@client-x.com wants developer" is not something anyone can
// act on.
function AccessRequests({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const requests = useListAccessRequests(teamId);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: getListAccessRequestsQueryKey(teamId) });
  };
  const grant = useGrantAccessRequest({
    mutation: {
      onSuccess: () => {
        toastSuccess("Access granted");
        refresh();
        void qc.invalidateQueries({ queryKey: getListTeamMembersQueryKey(teamId) });
      },
      onError: (e: unknown) => toastFailed("Could not grant access", e),
    },
  });
  const deny = useDenyAccessRequest({
    mutation: {
      onSuccess: () => {
        toastSuccess("Request denied");
        refresh();
      },
      onError: (e: unknown) => toastFailed("Could not deny the request", e),
    },
  });

  const pending = (requests.data ?? []).filter((r: AccessRequest) => r.state === "pending");
  if (requests.isPending || requests.isError || pending.length === 0) return null;
  return (
    <div className="space-y-2 border-t border-border p-3">
      <h3 className="eyebrow">Access requests</h3>
      <ul className="space-y-2">
        {pending.map((r: AccessRequest) => (
          <li key={r.id} className="rounded-md border border-border p-3">
            <p className="text-[13px] text-text">
              <span className="mono">{r.user_email}</span> requests{" "}
              <span className="mono">
                {r.current_role || "none"} → {r.requested_role}
              </span>
            </p>
            {r.message && <p className="mt-1.5 text-[12.5px] leading-[1.5] text-text-mid">“{r.message}”</p>}
            <div className="mt-2.5 flex items-center gap-2.5">
              <ActionButton
                variant="primary"
                size="sm"
                state={grant.isPending ? "busy" : "idle"}
                busyLabel="Granting…"
                onClick={() => grant.mutate({ id: r.id })}
              >
                Grant {r.requested_role}
              </ActionButton>
              <ActionButton
                variant="ghost"
                size="sm"
                state={deny.isPending ? "busy" : "idle"}
                busyLabel="Denying…"
                onClick={() => deny.mutate({ id: r.id, data: { reason: "" } })}
              >
                Deny
              </ActionButton>
              <span className="mono ml-auto text-[11px] text-text-faint">
                either way, audit-logged · requester is notified
              </span>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function CreateTeamDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useCreateTeam({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamsQueryKey() });
        toastSuccess("Team created");
        setOpen(false);
      },
      // The pill turns to "✕ Retry" and the toast carries the why (10b/10c);
      // the inline line keeps the server's sentence beside the field.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the team");
        toastFailed("Could not create the team", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      create.reset();
      return;
    }
    setName("");
    setError(null);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name: name.trim() } });
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> New team
        </Button>
      </DialogTrigger>
      <DialogContent title="Create a team">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id, describedBy) => (
              <Input
                id={id}
                required
                autoFocus
                aria-describedby={describedBy}
                disabled={create.isPending}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="platform"
              />
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="primary"
              state={state}
              busyLabel="Creating…"
              successLabel="Created"
              disabledReason={name.trim() === "" ? "Name the team first" : undefined}
            >
              Create team
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
