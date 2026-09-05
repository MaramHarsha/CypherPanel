// Settings · Backup targets: where database backups are written — any
// S3-compatible service. Same shape as the other credential surfaces (canvas
// 13a): a bordered list, a bold label with the region as a mono chip, the
// bucket path as a faint sub-line, and a plain word action on the right.
//
// The access key is an identifier and stays readable; the secret key is
// write-only and is never returned by the API, so the form says so on the label
// rather than pretending the value can be reviewed later (ui-principles §6).
import { createFileRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import {
  getListBackupTargetsQueryKey,
  useCreateBackupTarget,
  useDeleteBackupTarget,
  useListBackupTargets,
} from "@/api/gen/backups/backups";
import type { BackupTarget } from "@/api/gen/model";
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

export const Route = createFileRoute("/_app/settings/backup-targets")({ component: BackupTargetsTab });

/** The plain word-action that ends a security row (canvas 13a/13i). */
const rowAction = "shrink-0 text-[12px] font-medium hover:underline disabled:no-underline disabled:opacity-50";

function BackupTargetsTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "backup targets" }]);
  const targets = useListBackupTargets();
  return (
    <div className="max-w-xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Backup targets</Eyebrow>
        <CreateTargetDialog />
      </div>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        A backup target is where database backups are stored — any S3-compatible service (AWS S3, Backblaze B2, MinIO,
        Hetzner). Add one here, then schedule backups on any database. Credentials are sealed on save and never shown
        again.
      </p>
      <PageState
        query={targets}
        empty={
          <EmptyState
            title="No backup targets"
            hint="Databases can run without one, but nothing is being backed up until a target exists."
            action={<CreateTargetDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((t) => (
              <TargetRow key={t.id} t={t} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function TargetRow({ t }: { t: BackupTarget }) {
  // Where the files actually land, in the order the provider writes them.
  const path = [t.endpoint, t.bucket, t.path_prefix].filter(Boolean).join(" / ");
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="truncate text-[13px] font-semibold text-text">{t.name}</span>
          {t.region && (
            <span className="mono shrink-0 rounded bg-raised px-1.5 py-0.5 text-[10.5px] uppercase text-text-faint">
              {t.region}
            </span>
          )}
        </span>
        <span className="mono mt-0.5 block truncate text-[11.5px] text-text-faint" title={path}>
          {path} · added {relativeTime(t.created_at)}
        </span>
      </span>
      <DeleteTargetDialog t={t} />
    </li>
  );
}

// The blast radius is the part that is easy to get wrong here: removing the
// target does not remove the files, but it does remove the only credentials
// CypherPanel has for them — which is what makes the existing backups
// unreachable from this panel (ui-principles §2).
function DeleteTargetDialog({ t }: { t: BackupTarget }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const del = useDeleteBackupTarget({
    mutation: {
      // Backup targets are not on the live stream, so the deleted row would
      // stay on screen — offering credentials the panel no longer holds —
      // until something else reloaded the page.
      onSuccess: () => {
        toastSuccess(`Deleted ${t.name}`);
        void qc.invalidateQueries({ queryKey: getListBackupTargetsQueryKey() });
        setOpen(false);
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the target", e, { retry: () => del.mutate(vars) }),
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
        title={`Delete ${t.name}?`}
        description="Backup schedules writing here stop, and CypherPanel can no longer restore from what is already in the bucket. The files themselves are left untouched — only the credentials go."
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
            onClick={() => del.mutate({ id: t.id })}
          >
            Delete target
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}

const BLANK = { name: "", endpoint: "", bucket: "", region: "", path_prefix: "", access_key: "", secret_key: "" };

function CreateTargetDialog({ primary }: { primary?: boolean }) {
  const qc = useQueryClient();
  const [form, setForm] = useState(BLANK);
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const create = useCreateBackupTarget({
    mutation: {
      onSuccess: () => {
        toastSuccess(`${form.name} is ready — schedule a backup on any database`);
        void qc.invalidateQueries({ queryKey: getListBackupTargetsQueryKey() });
        setForm(BLANK);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not add the target"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      data: {
        name: form.name,
        endpoint: form.endpoint,
        bucket: form.bucket,
        access_key: form.access_key,
        secret_key: form.secret_key,
        ...(form.region ? { region: form.region } : {}),
        ...(form.path_prefix ? { path_prefix: form.path_prefix } : {}),
      },
    });
  };

  const incomplete =
    form.name.trim() === "" ||
    form.endpoint.trim() === "" ||
    form.bucket.trim() === "" ||
    form.access_key.trim() === "" ||
    form.secret_key.trim() === "";

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setForm(BLANK);
          setError(null);
          create.reset();
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="primary" size={primary ? "lg" : "md"}>
          <Plus className="h-3.5 w-3.5" /> New target
        </Button>
      </DialogTrigger>
      <DialogContent
        size="form"
        title="Add a backup target"
        description="From your storage provider: the S3 endpoint, the bucket, and an access key that may write to it."
      >
        <form onSubmit={submit} className="space-y-4">
          {error && (
            <p
              role="alert"
              className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger"
            >
              {error}
            </p>
          )}
          <Field label="Name" qualifier="· how you'll pick it later">
            {(id) => <Input id={id} required autoFocus value={form.name} onChange={set("name")} placeholder="b2-backups" />}
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Endpoint">
              {(id) => (
                <Input
                  id={id}
                  required
                  value={form.endpoint}
                  onChange={set("endpoint")}
                  placeholder="s3.us-west-002.backblazeb2.com"
                />
              )}
            </Field>
            <Field label="Bucket">
              {(id) => <Input id={id} required value={form.bucket} onChange={set("bucket")} placeholder="atlas-backups" />}
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Region" hint="Leave empty if your provider doesn't use regions.">
              {(id) => <Input id={id} value={form.region} onChange={set("region")} placeholder="us-west-002" />}
            </Field>
            <Field label="Path prefix" hint="Optional folder inside the bucket.">
              {(id) => <Input id={id} value={form.path_prefix} onChange={set("path_prefix")} placeholder="cypherpanel/" />}
            </Field>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Access key">
              {(id) => <Input id={id} required value={form.access_key} onChange={set("access_key")} autoComplete="off" />}
            </Field>
            {/* 12a's own label for a credential that is sealed on save: the
                qualifier tells you before you type that you will not read it
                back here. */}
            <Field label="Secret key" qualifier="· write-only">
              {(id) => (
                <Input
                  id={id}
                  required
                  type="password"
                  value={form.secret_key}
                  onChange={set("secret_key")}
                  autoComplete="off"
                />
              )}
            </Field>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-2.5 pt-1">
            <span className="mr-auto text-[11.5px] text-text-faint">credentials are sealed on save</span>
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
              busyLabel="Adding…"
              disabledReason={incomplete ? "Fill in the endpoint, bucket and access key first" : undefined}
            >
              Add target
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
