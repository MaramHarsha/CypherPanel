// Settings · Account: who you are, plus API tokens (shown once at creation).
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { useCreateToken, useDeleteToken, useGetMe, useListTokens } from "@/api/gen/auth/auth";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { relativeTime } from "@/lib/time";

export const Route = createFileRoute("/_app/settings/")({ component: AccountTab });

function AccountTab() {
  const me = useGetMe();
  const tokens = useListTokens();

  return (
    <div className="max-w-xl space-y-8">
      <section className="space-y-2">
        <Eyebrow>Account</Eyebrow>
        <div className="rounded-lg border border-border bg-surface p-4 text-[13px]">
          <p className="text-text">{me.data?.email ?? "…"}</p>
          <p className="mono mt-1 text-xs text-text-faint">panel role: {me.data?.role ?? "…"}</p>
        </div>
      </section>

      <section className="space-y-2">
        <div className="flex items-center justify-between">
          <Eyebrow>API tokens</Eyebrow>
          <CreateTokenDialog />
        </div>
        <p className="text-[13px] text-text-mid">
          A token lets scripts and CI call the CypherPanel API as you. The value is shown once at creation.
        </p>
        <PageState
          query={tokens}
          empty={<EmptyState title="No API tokens" hint="Create one to drive deploys from CI or the command line." action={<CreateTokenDialog primary />} />}
        >
          {(list) => (
            <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((t) => (
                <TokenRow key={t.id} id={t.id} name={t.name} lastUsed={t.last_used_at} created={t.created_at} />
              ))}
            </ul>
          )}
        </PageState>
      </section>
    </div>
  );
}

function TokenRow({ id, name, lastUsed, created }: { id: string; name: string; lastUsed?: string; created: string }) {
  const del = useDeleteToken({
    mutation: {
      onSuccess: () => toast.success(`Revoked ${name}`),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not revoke the token"),
    },
  });
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 flex-col">
        <span className="truncate text-[13px] text-text">{name}</span>
        <span className="mono text-xs text-text-faint">
          created {relativeTime(created)}
          {lastUsed ? ` · last used ${relativeTime(lastUsed)}` : " · never used"}
        </span>
      </span>
      <Button size="sm" variant="ghost" aria-label={`Revoke ${name}`} disabled={del.isPending} onClick={() => del.mutate({ id })}>
        <Trash2 className="h-3.5 w-3.5 text-danger" />
      </Button>
    </li>
  );
}

function CreateTokenDialog({ primary }: { primary?: boolean }) {
  const [name, setName] = useState("");
  const [minted, setMinted] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateToken({
    mutation: {
      onSuccess: (res) => setMinted((res as { token?: string }).token ?? null),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the token"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name } });
  };

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setMinted(null);
          setName("");
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New token
        </Button>
      </DialogTrigger>
      {minted === null ? (
        <DialogContent title="Create an API token" description="Name it after what will use it — 'ci', 'deploy script'.">
          <form onSubmit={submit} className="space-y-4">
            <Field label="Name" error={error ?? undefined}>
              {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} />}
            </Field>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === ""}>
                {create.isPending ? "Creating…" : "Create token"}
              </Button>
            </div>
          </form>
        </DialogContent>
      ) : (
        <DialogContent title="Copy your token now" description="This is the only time it will be shown. Store it like a password.">
          <CopyField value={minted} />
          <div className="mt-4 flex justify-end">
            <DialogClose asChild>
              <Button variant="primary">Done</Button>
            </DialogClose>
          </div>
        </DialogContent>
      )}
    </Dialog>
  );
}
