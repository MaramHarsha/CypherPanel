"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, HardDrive, Plus, Server, Trash2 } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  createDestination,
  deleteDestination,
  listDestinations,
  updateDestination,
  type BackupDestination,
} from "@/lib/api";

const kindIcon = {
  local: HardDrive,
  s3: Database,
  sftp: Server,
  rest: Server,
} as const;

// Repository hints per backend so the operator knows the shape restic expects
// without leaving the page.
const repoHints: Record<BackupDestination["kind"], string> = {
  local: "/var/backups/cypherpanel",
  s3: "s3:s3.amazonaws.com/bucket/prefix",
  sftp: "sftp:user@host:/srv/backups",
  rest: "rest:https://backup.example.com/repo",
};

function DeleteDestinationButton({ dest }: { dest: BackupDestination }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteDestination(dest.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backup-destinations"] });
      setOpen(false);
    },
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Remove destination ${dest.name}`}
        onClick={() => setOpen(true)}
      >
        <Trash2 className="h-4 w-4 text-destructive" />
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {dest.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            This removes the destination and its stored credentials from CypherPanel, and
            stops any scheduled backups writing to it. The remote repository and every
            snapshot in it are left untouched — delete those with your storage provider if
            you actually want the data gone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={remove.isPending}
            onClick={(e) => {
              e.preventDefault();
              remove.mutate();
            }}
          >
            {remove.isPending ? "Removing…" : "Remove"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ScheduleControl({ dest }: { dest: BackupDestination }) {
  const qc = useQueryClient();
  const update = useMutation({
    mutationFn: (schedule: BackupDestination["schedule"]) =>
      updateDestination(dest.id, {
        schedule,
        retention_daily: dest.retention_daily,
        retention_weekly: dest.retention_weekly,
        retention_monthly: dest.retention_monthly,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["backup-destinations"] }),
  });

  return (
    <Select
      value={dest.schedule}
      onValueChange={(v) => update.mutate((v ?? "off") as BackupDestination["schedule"])}
      disabled={update.isPending}
    >
      <SelectTrigger size="sm" aria-label={`Schedule for ${dest.name}`}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="off">Manual only</SelectItem>
        <SelectItem value="daily">Daily</SelectItem>
        <SelectItem value="weekly">Weekly</SelectItem>
      </SelectContent>
    </Select>
  );
}

const emptyForm = {
  name: "",
  kind: "local" as BackupDestination["kind"],
  repository: "",
  password: "",
  schedule: "daily" as BackupDestination["schedule"],
  retention_daily: "7",
  retention_weekly: "4",
  retention_monthly: "6",
};

export default function BackupsPage() {
  const qc = useQueryClient();
  const [form, setForm] = useState(emptyForm);
  const [error, setError] = useState<string | null>(null);

  const { data: destinations, isLoading } = useQuery({
    queryKey: ["backup-destinations"],
    queryFn: listDestinations,
  });

  const create = useMutation({
    mutationFn: () =>
      createDestination({
        name: form.name,
        kind: form.kind,
        repository: form.repository,
        password: form.password,
        schedule: form.schedule,
        retention_daily: Number(form.retention_daily) || 0,
        retention_weekly: Number(form.retention_weekly) || 0,
        retention_monthly: Number(form.retention_monthly) || 0,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backup-destinations"] });
      setForm(emptyForm);
      setError(null);
    },
    onError: (e) =>
      setError(e instanceof ApiError ? e.message : "Could not create destination"),
  });

  const retentionKeepsSomething =
    Number(form.retention_daily) > 0 ||
    Number(form.retention_weekly) > 0 ||
    Number(form.retention_monthly) > 0;
  const canCreate =
    form.name.trim() !== "" &&
    form.repository.trim() !== "" &&
    form.password.length >= 8 &&
    retentionKeepsSomething &&
    !create.isPending;

  return (
    <div>
      <PageHeader
        title="Backups"
        description="Backup destinations for the fleet. Snapshots are incremental and deduplicated; per-account history lives on each account's Backups page."
      />

      <Card>
        <CardContent className="grid gap-3 p-4 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="dest-name">Name</Label>
            <Input
              id="dest-name"
              placeholder="primary-offsite"
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dest-kind">Backend</Label>
            <Select
              value={form.kind}
              onValueChange={(v) =>
                setForm((f) => ({ ...f, kind: (v ?? "local") as BackupDestination["kind"] }))
              }
            >
              <SelectTrigger id="dest-kind" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="local">Local path</SelectItem>
                <SelectItem value="s3">S3-compatible</SelectItem>
                <SelectItem value="sftp">SFTP</SelectItem>
                <SelectItem value="rest">restic REST server</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5 sm:col-span-2">
            <Label htmlFor="dest-repo">Repository</Label>
            <Input
              id="dest-repo"
              placeholder={repoHints[form.kind]}
              value={form.repository}
              onChange={(e) => setForm((f) => ({ ...f, repository: e.target.value }))}
            />
          </div>
          <div className="grid gap-1.5 sm:col-span-2">
            <Label htmlFor="dest-password">Repository password (8+ chars)</Label>
            <Input
              id="dest-password"
              type="password"
              value={form.password}
              onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
            />
            <p className="text-xs text-muted-foreground">
              Encrypts the repository. Stored encrypted and never shown again — if you lose
              it, the snapshots are unrecoverable.
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dest-schedule">Schedule</Label>
            <Select
              value={form.schedule}
              onValueChange={(v) =>
                setForm((f) => ({
                  ...f,
                  schedule: (v ?? "off") as BackupDestination["schedule"],
                }))
              }
            >
              <SelectTrigger id="dest-schedule" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="off">Manual only</SelectItem>
                <SelectItem value="daily">Daily</SelectItem>
                <SelectItem value="weekly">Weekly</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid grid-cols-3 gap-2">
            <div className="grid gap-1.5">
              <Label htmlFor="keep-daily">Keep daily</Label>
              <Input
                id="keep-daily"
                inputMode="numeric"
                value={form.retention_daily}
                onChange={(e) => setForm((f) => ({ ...f, retention_daily: e.target.value }))}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="keep-weekly">Weekly</Label>
              <Input
                id="keep-weekly"
                inputMode="numeric"
                value={form.retention_weekly}
                onChange={(e) => setForm((f) => ({ ...f, retention_weekly: e.target.value }))}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="keep-monthly">Monthly</Label>
              <Input
                id="keep-monthly"
                inputMode="numeric"
                value={form.retention_monthly}
                onChange={(e) => setForm((f) => ({ ...f, retention_monthly: e.target.value }))}
              />
            </div>
          </div>
          {!retentionKeepsSomething && (
            <p className="text-sm text-destructive sm:col-span-2">
              Retention must keep at least one snapshot — an all-zero policy would prune
              the repository empty.
            </p>
          )}
          <Button className="sm:col-span-2" onClick={() => create.mutate()} disabled={!canCreate}>
            <Plus className="h-4 w-4" /> Add destination
          </Button>
          {error && <p className="text-sm text-destructive sm:col-span-2">{error}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-12 w-full" />
              ))}
            </div>
          ) : (destinations ?? []).length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              <HardDrive className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
              No destinations yet. Add one above to enable backups.
            </p>
          ) : (
            <div className="divide-y divide-border">
              {(destinations ?? []).map((d) => {
                const Icon = kindIcon[d.kind] ?? HardDrive;
                return (
                  <div
                    key={d.id}
                    className="flex flex-wrap items-center justify-between gap-3 px-4 py-3"
                  >
                    <div className="flex min-w-0 items-center gap-3">
                      <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/12 text-primary">
                        <Icon className="h-4 w-4" />
                      </span>
                      <div className="min-w-0">
                        <p className="truncate font-medium">{d.name}</p>
                        <p className="truncate font-mono text-xs text-muted-foreground">
                          {d.repository}
                        </p>
                      </div>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="hidden text-xs text-muted-foreground sm:inline">
                        keep {d.retention_daily}d/{d.retention_weekly}w/{d.retention_monthly}m
                      </span>
                      <Badge variant="secondary">
                        {d.last_run_at
                          ? `ran ${new Date(d.last_run_at).toLocaleDateString()}`
                          : "never run"}
                      </Badge>
                      <ScheduleControl dest={d} />
                      <DeleteDestinationButton dest={d} />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
