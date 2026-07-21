// Settings · Backup targets: where backup files are stored (any S3-compatible
// service). Secrets are write-only (ui-principles §6).
import { createFileRoute } from "@tanstack/react-router";
import { Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  useCreateBackupTarget,
  useDeleteBackupTarget,
  useListBackupTargets,
} from "@/api/gen/backups/backups";
import type { BackupTarget } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/_app/settings/backup-targets")({ component: BackupTargetsTab });

function BackupTargetsTab() {
  const targets = useListBackupTargets();
  return (
    <div className="max-w-xl space-y-3">
      <div className="flex items-center justify-between">
        <Eyebrow>Backup targets</Eyebrow>
        <CreateTargetDialog />
      </div>
      <p className="text-[13px] text-text-mid">
        A backup target is where database backups are stored — any S3-compatible service (AWS S3, Backblaze B2, MinIO,
        Hetzner …). Credentials are sealed and write-only.
      </p>
      <PageState
        query={targets}
        empty={<EmptyState title="No backup targets" hint="Add one, then schedule backups on any database." action={<CreateTargetDialog primary />} />}
      >
        {(list) => (
          <ul className="divide-y divide-border rounded-md border border-border bg-surface">
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
  const del = useDeleteBackupTarget({
    mutation: {
      onSuccess: () => toast.success(`Deleted ${t.name}`),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the target"),
    },
  });
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2.5">
      <span className="flex min-w-0 flex-col">
        <span className="truncate text-[13px] font-medium text-text">{t.name}</span>
        <span className="mono truncate text-xs text-text-faint">
          {t.endpoint} / {t.bucket}
          {t.path_prefix ? ` / ${t.path_prefix}` : ""}
        </span>
      </span>
      <Button size="sm" variant="ghost" aria-label={`Delete ${t.name}`} disabled={del.isPending} onClick={() => del.mutate({ id: t.id })}>
        <Trash2 className="h-3.5 w-3.5 text-danger" />
      </Button>
    </li>
  );
}

function CreateTargetDialog({ primary }: { primary?: boolean }) {
  const [form, setForm] = useState({ name: "", endpoint: "", bucket: "", region: "", access_key: "", secret_key: "" });
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const create = useCreateBackupTarget({
    mutation: {
      onSuccess: () => {
        toast.success("Backup target added");
        setForm({ name: "", endpoint: "", bucket: "", region: "", access_key: "", secret_key: "" });
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
      },
    });
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          <Plus className="h-3.5 w-3.5" /> New target
        </Button>
      </DialogTrigger>
      <DialogContent title="Add a backup target" description="From your storage provider: the S3 endpoint, bucket, and an access key that may write to it.">
        <form onSubmit={submit} className="space-y-4">
          <Field label="Name" error={error ?? undefined}>
            {(id) => <Input id={id} required autoFocus value={form.name} onChange={set("name")} placeholder="b2-backups" />}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Endpoint">
              {(id) => <Input id={id} required value={form.endpoint} onChange={set("endpoint")} placeholder="s3.us-west-002.backblazeb2.com" className="mono" />}
            </Field>
            <Field label="Bucket">
              {(id) => <Input id={id} required value={form.bucket} onChange={set("bucket")} className="mono" />}
            </Field>
          </div>
          <Field label="Region" hint="Leave empty if your provider doesn't use regions.">
            {(id) => <Input id={id} value={form.region} onChange={set("region")} className="mono" />}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Access key">
              {(id) => <Input id={id} required value={form.access_key} onChange={set("access_key")} className="mono" autoComplete="off" />}
            </Field>
            <Field label="Secret key">
              {(id) => <Input id={id} required type="password" value={form.secret_key} onChange={set("secret_key")} className="mono" autoComplete="off" />}
            </Field>
          </div>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {create.isPending ? "Adding…" : "Add target"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
