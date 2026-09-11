// Settings · Deploy keys (canvas 3i). A deploy key is the credential that lets
// CypherPanel read one private repository, so it shares the shape every other
// credential surface uses (canvas 13a): a bordered list whose rows carry a bold
// label, a faint mono sub-line, and a plain word action on the right.
//
// The public half is the whole point of the page — it is what the operator
// carries to GitHub — so it is on screen and copyable in the row rather than a
// click deeper. The private half never leaves the control plane, which is
// stated where the key is made rather than in documentation nobody opens
// (ui-principles §11).
import { createFileRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  getListDeployKeysQueryKey,
  useCreateDeployKey,
  useDeleteDeployKey,
  useListDeployKeys,
} from "@/api/gen/deploy-keys/deploy-keys";
import type { DeployKey } from "@/api/gen/model";
import { CopyButton } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";
import { useQueryClient } from "@tanstack/react-query";

export const Route = createFileRoute("/_app/settings/deploy-keys")({ component: DeployKeysTab });

/** The plain word-action that ends a security row (canvas 13a/13i). */
const rowAction = "shrink-0 text-[12px] font-medium hover:underline disabled:no-underline disabled:opacity-50";

function DeployKeysTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "deploy keys" }]);
  const keys = useListDeployKeys();
  return (
    <div className="max-w-xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Deploy keys</Eyebrow>
        <CreateKeyDialog />
      </div>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        A deploy key lets CypherPanel read one private repository. Create one, add its public key to the repo on GitHub
        (Settings → Deploy keys), then pick it when you create the app. Public repositories need none of this.
      </p>
      <PageState
        query={keys}
        empty={
          <EmptyState
            title="No deploy keys"
            hint="Only private repositories need one — a public repo deploys without any credential at all."
            action={<CreateKeyDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
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
  return (
    <li className="px-4 py-3">
      <div className="flex items-center gap-3">
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13px] font-semibold text-text">{k.name}</span>
          <span className="mono mt-0.5 block truncate text-[11.5px] text-text-faint" title={k.fingerprint}>
            added {relativeTime(k.created_at)} · {k.fingerprint}
          </span>
        </span>
        <DeleteKeyDialog k={k} />
      </div>
      {/* The public key is a machine value the operator has to move somewhere
          else, so it gets the treatment every such value gets in the canvas: an
          ink strip with a copy control, legible wherever it is read. */}
      <div className="mt-2.5 flex items-center gap-2 rounded-md border border-pane-border bg-pane px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-pane-text" title={k.public_key}>
          {k.public_key}
        </span>
        <span className="-mr-1 shrink-0">
          <CopyButton value={k.public_key} label={`Copy the public key for ${k.name}`} />
        </span>
      </div>
    </li>
  );
}

// Removing a key is not undoable and not free: the repositories it opens close
// again. §2 wants the blast radius stated, and it is the one thing the operator
// cannot see from this page — nothing here lists which apps read through it.
function DeleteKeyDialog({ k }: { k: DeployKey }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const del = useDeleteDeployKey({
    mutation: {
      // The list is only ever refreshed by whoever changed it — deploy keys
      // carry no live stream — and a row still showing a copyable public key
      // the panel has thrown away is exactly the state §10 forbids.
      onSuccess: () => {
        toastSuccess(`Deleted ${k.name}`);
        void qc.invalidateQueries({ queryKey: getListDeployKeysQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the key", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button type="button" className={cn(rowAction, "text-danger")}>
          Delete
        </button>
      </DialogTrigger>
      <DialogContent
        title={`Delete ${k.name}?`}
        description="Any application that reads its repository through this key stops building until it is given another one. Remove the key from GitHub too — this only deletes CypherPanel's half."
      >
        <div className="flex items-center justify-end gap-2.5">
          <DialogClose asChild>
            <Button variant="ghost" size="lg">
              Cancel
            </Button>
          </DialogClose>
          <ActionButton
            variant="danger"
            size="lg"
            state={del.isPending ? "busy" : "idle"}
            busyLabel="Deleting…"
            onClick={() => del.mutate({ id: k.id })}
          >
            Delete key
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function CreateKeyDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [made, setMade] = useState<DeployKey | null>(null);

  const create = useCreateDeployKey({
    mutation: {
      // The dialog moves on to the public half, but the list underneath is what
      // the operator returns to for it — it has to know the key exists.
      onSuccess: (res) => {
        setMade(res.deploy_key);
        void qc.invalidateQueries({ queryKey: getListDeployKeysQueryKey() });
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
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setMade(null);
          setName("");
          setError(null);
          create.reset();
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New deploy key
        </Button>
      </DialogTrigger>
      {made === null ? (
        <DialogContent
          title="Create a deploy key"
          description="CypherPanel generates the keypair. The private half is sealed here and never leaves the control plane; only the public half is yours to carry."
        >
          <form onSubmit={submit} className="space-y-4">
            <Field label="Name" qualifier="· which repository it opens" error={error ?? undefined}>
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="acme-monorepo"
                />
              )}
            </Field>
            <div className="flex items-center justify-end gap-2.5 pt-1">
              <DialogClose asChild>
                <Button variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton
                type="submit"
                variant="accent"
                size="lg"
                state={create.isPending ? "busy" : "idle"}
                busyLabel="Generating…"
                disabledReason={name.trim() === "" ? "Name the key first" : undefined}
              >
                Create key →
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      ) : (
        // Canvas 10a's third panel: the click ends on the value it produced,
        // with the one next action spelled out. The key is readable again from
        // the list afterwards — this step exists so the golden path does not
        // end at "now go and find it".
        <DialogContent
          title={`${made.name} is ready`}
          description="Add this public key to the repository on GitHub — Settings → Deploy keys → Add deploy key. Read access is enough."
        >
          <div className="rounded-md border border-pane-border bg-pane px-3.5 py-3">
            <div className="flex items-start gap-2.5">
              <code className="min-w-0 flex-1 break-all font-mono text-[11.5px] leading-[1.7] text-pane-text">
                {made.public_key}
              </code>
              <span className="-mr-1 -mt-1 shrink-0">
                <CopyButton value={made.public_key} label="Copy the public key" />
              </span>
            </div>
          </div>
          <p className="mono mt-2.5 break-all text-[11px] leading-5 text-text-faint">{made.fingerprint}</p>
          <div className="mt-4 flex justify-end">
            <DialogClose asChild>
              <Button variant="primary" size="lg">
                Done
              </Button>
            </DialogClose>
          </div>
        </DialogContent>
      )}
    </Dialog>
  );
}
