// Settings · Users: the panel's accounts. Panel-role gated — hidden below
// admin. A user's panel role (member/admin/owner) is distinct from their
// per-team role (web-ui-design.md §4).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import {
  getListUsersQueryKey,
  useCreateUser,
  useDeleteUser,
  useListUsers,
  useUpdateUserRole,
} from "@/api/gen/teams/teams";
import type { User } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { PanelRoleRefusal } from "@/components/role-refusal";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/users")({ component: UsersTab });

function UsersTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "users" }]);
  const me = useGetMe();
  const canManage = atLeast(me.data?.role as Role | undefined, "admin");
  const users = useListUsers({ query: { enabled: canManage } });

  if (me.isSuccess && !canManage) {
    return <PanelRoleRefusal action="Managing panel accounts" needs="admin" />;
  }

  return (
    <div className="max-w-xl space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Users</Eyebrow>
        <CreateUserDialog />
      </div>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        Accounts that can sign in to this panel. The panel role is the account's baseline rank; team roles scope access per team.
      </p>
      <PageState
        query={users}
        empty={<EmptyState title="No other users" hint="Create an account for each teammate here, then add them to a team under Settings → Teams." action={<CreateUserDialog primary />} />}
      >
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((u) => (
              <UserRow key={u.id} user={u} isSelf={u.id === me.data?.id} canDelete={me.data?.role === "owner"} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function UserRow({ user, isSelf, canDelete }: { user: User; isSelf: boolean; canDelete: boolean }) {
  const qc = useQueryClient();
  const update = useUpdateUserRole({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListUsersQueryKey() });
        toastSuccess("Role updated");
      },
      onError: (e: unknown, vars) => toastFailed("Could not update the role", e, { retry: () => update.mutate(vars) }),
    },
  });
  const del = useDeleteUser({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListUsersQueryKey() });
        toastSuccess(`Deleted ${user.email}`);
      },
      onError: (e: unknown) => toastFailed("Could not delete the account", e),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-[13px] text-text">{user.email}</span>
        {isSelf && <span className="mono text-[11px] text-text-faint">you</span>}
      </span>
      {/* A role is a word, not a machine value, so it drops the mono the
          shared control defaults to (canvas 12c/13av set this class of
          dropdown in sans). */}
      <Select
        value={user.role}
        disabled={isSelf || update.isPending}
        onChange={(e) => update.mutate({ id: user.id, data: { role: e.target.value as "member" | "admin" | "owner" } })}
        aria-label={`Role for ${user.email}`}
        className="w-auto shrink-0 font-sans"
      >
        <option value="member">member</option>
        <option value="admin">admin</option>
        <option value="owner">owner</option>
      </Select>
      {/* Panel owner only, and never your own account — deleting the account
          you are signed in as is the one mistake nobody can undo from here.
          The API refuses it; the button says so rather than letting a 400 do
          the explaining. */}
      <ConfirmDestructive
        trigger={
          <Button
            size="sm"
            variant="ghost"
            className="shrink-0 text-danger"
            disabledReason={
              isSelf
                ? "You cannot delete your own account"
                : canDelete
                  ? undefined
                  : "Deleting an account needs panel owner"
            }
          >
            Delete
          </Button>
        }
        title={`Delete ${user.email}?`}
        lead="Deleting this account, immediately:"
        blastRadius={[
          "their sign-in — every live session and API token they hold stops working",
          "their membership of every team, and the invitations they issued",
          "not what they made: their projects, servers and deploys stay, and the audit log keeps their name",
        ]}
        confirmName={user.email}
        actionLabel="Delete account"
        pending={del.isPending}
        pendingLabel="Deleting…"
        onConfirm={() => del.mutate({ id: user.id })}
      />
    </li>
  );
}

function CreateUserDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("member");
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState(false);

  const create = useCreateUser({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListUsersQueryKey() });
        setCreated(true);
        toastSuccess("User created");
      },
      // The pill turns to "✕ Retry" and the toast carries the why (10b/10c);
      // the inline line keeps the server's sentence beside the field.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the user");
        toastFailed("Could not create the user", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { email, password, role: role as "member" | "admin" | "owner" } });
  };

  return (
    <Dialog
      onOpenChange={(open) => {
        if (open) {
          // A reopened modal never inherits the last attempt's "✕ Retry" pill.
          create.reset();
          return;
        }
        setEmail("");
        setPassword("");
        setCreated(false);
        setError(null);
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New user
        </Button>
      </DialogTrigger>
      {created ? (
        <DialogContent title="User created" description="Share these credentials over a secure channel; ask them to change the password after signing in.">
          <div className="space-y-2">
            <CopyField value={email} />
            <CopyField value={password} />
          </div>
          <div className="mt-4 flex justify-end">
            <DialogClose asChild>
              <Button variant="primary">Done</Button>
            </DialogClose>
          </div>
        </DialogContent>
      ) : (
        <DialogContent title="Create a user" description="Sets up a sign-in account. Add them to a team afterwards to grant access.">
          <form onSubmit={submit} className="space-y-4">
            <Field label="Email" error={error ?? undefined}>
              {(id) => <Input id={id} type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />}
            </Field>
            <Field label="Temporary password" hint="They can change it after their first sign-in.">
              {(id) => <Input id={id} type="text" required value={password} onChange={(e) => setPassword(e.target.value)} className="mono" autoComplete="off" />}
            </Field>
            <Field
              label="Panel role"
              hint="Members work in the teams they belong to; admins also manage servers, deploy keys, backup targets and accounts; owners can change panel roles and delete users."
            >
              {(id) => (
                <Select id={id} className="font-sans" value={role} onChange={(e) => setRole(e.target.value)}>
                  <option value="member">member</option>
                  <option value="admin">admin</option>
                  <option value="owner">owner</option>
                </Select>
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
                disabledReason={
                  email.trim() === "" ? "Enter their email first" : password === "" ? "Set a temporary password first" : undefined
                }
              >
                Create user
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      )}
    </Dialog>
  );
}
