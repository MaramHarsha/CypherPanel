// Settings · Deploy keys. The one-line explanation lives inline, per the
// beginner-first rule (ui-principles §11).
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  useCreateDeployKey,
  useDeleteDeployKey,
  useListDeployKeys,
} from "@/api/gen/deploy-keys/deploy-keys";
import type { DeployKey } from "@/api/gen/model";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/_app/settings/deploy-keys")({ component: DeployKeysTab });

function DeployKeysTab() {
  const keys = useListDeployKeys();
  return (
    <div className="max-w-xl space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Deploy keys</Eyebrow>
        <CreateKeyDialog />
      </div>
      <p className="text-[13px] text-text-mid">
        A deploy key lets CypherPanel read a private repository. Create one, add its public key to the repo on GitHub
        (Settings → Deploy keys), then pick it when creating the app.
      </p>
      <PageState
        query={keys}
        empty={<EmptyState title="No deploy keys" hint="Only needed for private repositories — public repos deploy without one." action={<CreateKeyDialog primary />} />}
      >
        {(list) => (
          <ul className="space-y-2">
            {list.map((k) => (
              <KeyRow key={k.id} k={k} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function KeyRow({ k }: { k: DeployKey }) {
  const del = useDeleteDeployKey({
    mutation: {
      onSuccess: () => toast.success(`Deleted ${k.name}`),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the key"),
    },
  });
  return (
    <li className="space-y-2 rounded-md border border-border bg-surface p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-[13px] font-medium text-text">{k.name}</span>
        <span className="flex items-center gap-2">
          <span className="mono hidden text-xs text-text-faint sm:inline">{k.fingerprint}</span>
          <Button size="sm" variant="ghost" aria-label={`Delete ${k.name}`} disabled={del.isPending} onClick={() => del.mutate({ id: k.id })}>
            <Trash2 className="h-3.5 w-3.5 text-danger" />
          </Button>
        </span>
      </div>
      <CopyField value={k.public_key} />
    </li>
  );
}

function CreateKeyDialog({ primary }: { primary?: boolean }) {
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useCreateDeployKey({
    mutation: {
      onSuccess: () => {
        toast.success("Deploy key created — copy the public key into GitHub");
        setName("");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the key"),
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
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          <Plus className="h-3.5 w-3.5" /> New deploy key
        </Button>
      </DialogTrigger>
      <DialogContent title="Create a deploy key" description="CypherPanel generates the keypair; the private half never leaves the server.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="acme-monorepo" />}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === ""}>
              {create.isPending ? "Creating…" : "Create key"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
