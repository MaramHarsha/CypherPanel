// Settings · Users: the panel's accounts. Panel-role gated — hidden below
// admin. A user's panel role (member/admin/owner) is distinct from their
// per-team role (web-ui-design.md §4).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { useGetMe } from "@/api/gen/auth/auth";
import {
  getListUsersQueryKey,
  useCreateUser,
  useListUsers,
  useUpdateUserRole,
} from "@/api/gen/teams/teams";
import type { User } from "@/api/gen/model";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { atLeast, type Role } from "@/lib/roles";

export const Route = createFileRoute("/_app/settings/users")({ component: UsersTab });

function UsersTab() {
  const me = useGetMe();
  const canManage = atLeast(me.data?.role as Role | undefined, "admin");
  const users = useListUsers({ query: { enabled: canManage } });

  if (me.isSuccess && !canManage) {
    return (
      <EmptyState
        title="Admins only"
        hint="Managing panel accounts needs an admin or owner role. Ask an admin if you need access changed."
      />
    );
  }

  return (
    <div className="max-w-xl space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Users</Eyebrow>
        <CreateUserDialog />
      </div>
      <p className="text-[13px] text-text-mid">
        Accounts that can sign in to this panel. The panel role is the account's baseline rank; team roles scope access per team.
      </p>
      <PageState
        query={users}
        empty={<EmptyState title="No other users" hint="Invite teammates by creating their account here." action={<CreateUserDialog primary />} />}
      >
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((u) => (
              <UserRow key={u.id} user={u} isSelf={u.id === me.data?.id} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function UserRow({ user, isSelf }: { user: User; isSelf: boolean }) {
  const qc = useQueryClient();
  const update = useUpdateUserRole({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListUsersQueryKey() });
        toast.success("Role updated");
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not update the role"),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-[13px] text-text">{user.email}</span>
        {isSelf && <span className="mono text-[11px] text-text-faint">you</span>}
      </span>
      <select
        value={user.role}
        disabled={isSelf || update.isPending}
        onChange={(e) => update.mutate({ id: user.id, data: { role: e.target.value as "member" | "admin" | "owner" } })}
        aria-label={`Role for ${user.email}`}
        className="h-7 rounded-lg border border-border bg-surface px-2 text-xs text-text disabled:opacity-50"
      >
        <option value="member">member</option>
        <option value="admin">admin</option>
        <option value="owner">owner</option>
      </select>
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
        toast.success("User created");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the user"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { email, password, role: role as "member" | "admin" | "owner" } });
  };

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setEmail("");
          setPassword("");
          setCreated(false);
          setError(null);
        }
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
            <Field label="Panel role">
              {(id) => (
                <select id={id} value={role} onChange={(e) => setRole(e.target.value)} className="h-8 w-full rounded-lg border border-border bg-surface px-2 text-sm text-text">
                  <option value="member">member</option>
                  <option value="admin">admin</option>
                  <option value="owner">owner</option>
                </select>
              )}
            </Field>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <Button type="submit" variant="primary" disabled={create.isPending}>
                Create user
              </Button>
            </div>
          </form>
        </DialogContent>
      )}
    </Dialog>
  );
}
