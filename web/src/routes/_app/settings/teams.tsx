// Settings · Teams: the tenancy boundary. Create teams, manage members. The
// last-owner guard's 409 is surfaced verbatim (web-ui-design.md §4).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2, Users } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getListTeamMembersQueryKey,
  getListTeamsQueryKey,
  useAddTeamMember,
  useCreateTeam,
  useListTeamMembers,
  useListTeams,
  useRemoveTeamMember,
} from "@/api/gen/teams/teams";
import type { Team, TeamMember } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { ActionButton } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

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
        empty={<EmptyState title="No teams" hint="Create a team to group projects and invite people." action={<CreateTeamDialog primary />} />}
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
      <button type="button" onClick={onToggle} className="flex w-full items-center justify-between gap-2 px-4 py-3 text-left">
        <span className="flex items-center gap-2">
          <Users className="h-4 w-4 text-text-faint" aria-hidden />
          <span className="text-[13px] font-medium text-text">{team.name}</span>
          {team.role && <span className="mono text-[11px] text-text-faint">{team.role}</span>}
        </span>
        <span className="mono text-xs text-text-faint">created {relativeTime(team.created_at)}</span>
      </button>
      {open && <MembersList team={team} />}
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
        toast.success("Member removed");
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not remove the member"),
    },
  });

  return (
    <div className="space-y-2 border-t border-border p-3">
      <div className="flex items-center justify-between">
        <span className="eyebrow">Members</span>
        <AddMemberDialog team={team} />
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

function CreateTeamDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useCreateTeam({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamsQueryKey() });
        toast.success("Team created");
        setName("");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the team"),
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name } });
  };
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New team
        </Button>
      </DialogTrigger>
      <DialogContent title="Create a team">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="platform" />}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === ""}>
              Create team
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// A role is a choice you make once and live with, so the three options are laid
// out with their consequences beside them rather than folded into a closed
// dropdown under one sentence that describes all three (canvas 9d/13ab).
const TEAM_ROLES = [
  { value: "member", label: "Member", help: "operate everything the team owns — deploy, roll back, env vars, logs" },
  { value: "admin", label: "Admin", help: "member, plus creating projects and managing everyone below owner" },
  { value: "owner", label: "Owner", help: "everything, including renaming the team and promoting other owners" },
] as const;

function RoleCards({
  name,
  value,
  onChange,
  options,
}: {
  name: string;
  value: string;
  onChange: (value: string) => void;
  options: ReadonlyArray<{ value: string; label: string; help: string }>;
}) {
  return (
    <div className="flex flex-col gap-[7px]">
      {options.map((o) => {
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

function AddMemberDialog({ team }: { team: Team }) {
  const teamId = team.id;
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("member");
  const [error, setError] = useState<string | null>(null);
  const add = useAddTeamMember({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListTeamMembersQueryKey(teamId) });
        toast.success("Member added");
        setEmail("");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not add the member"),
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    add.mutate({ id: teamId, data: { email, role: role as "member" | "admin" | "owner" } });
  };
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button size="sm" variant="ghost">
          <Plus className="h-3.5 w-3.5" /> Add
        </Button>
      </DialogTrigger>
      {/* Naming the team in the title is the whole answer to "add them where?" —
          this dialog opens from a row in a list of teams. */}
      <DialogContent title={`Add to ${team.name}`}>
        <form onSubmit={submit} className="space-y-4">
          <Field label="Email" error={error ?? undefined}>
            {(id) => <Input id={id} type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />}
          </Field>
          <fieldset>
            <legend className="mb-[7px] text-[12px] font-semibold text-text">Role</legend>
            <RoleCards name="team-role" value={role} onChange={setRole} options={TEAM_ROLES} />
          </fieldset>
          <div className="flex items-center justify-end gap-2.5">
            <span className="mr-auto text-[11.5px] text-text-faint">they must already have an account (Settings → Users)</span>
            <ActionButton
              type="submit"
              variant="accent"
              state={add.isPending ? "busy" : "idle"}
              busyLabel="Adding…"
              disabled={email.trim() === ""}
            >
              Add member →
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
