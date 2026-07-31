"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArchiveRestore, ArrowLeft, HardDriveDownload, Play } from "lucide-react";
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
  listBackups,
  listDestinations,
  restoreBackup,
  runBackup,
  type AccountBackup,
} from "@/lib/api";
import { useAccount } from "../../use-account";

function formatBytes(n: number): string {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

function statusVariant(status: AccountBackup["status"]) {
  if (status === "completed") return "success" as const;
  if (status === "failed") return "destructive" as const;
  return "secondary" as const;
}

// Restore is destructive when it targets the live home directory, so it is
// gated behind an explicit confirmation naming what will be overwritten —
// never a bare icon button.
function RestoreDialog({
  accountId,
  backup,
  username,
}: {
  accountId: string;
  backup: AccountBackup;
  username: string;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [target, setTarget] = useState<"" | "home">("");
  const [error, setError] = useState<string | null>(null);

  const restore = useMutation({
    mutationFn: () => restoreBackup(accountId, backup.id, backup.snapshot_id ?? "", target),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["backups", accountId] });
      setOpen(false);
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Restore failed"),
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="ghost"
        size="sm"
        aria-label={`Restore snapshot ${backup.snapshot_id}`}
        onClick={() => setOpen(true)}
      >
        <HardDriveDownload className="h-4 w-4" /> Restore
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Restore snapshot {backup.snapshot_id}?</AlertDialogTitle>
          <AlertDialogDescription>
            Choose where this snapshot lands. Restoring to a staging directory is safe
            and lets you inspect the contents first. Restoring in place overwrites{" "}
            <span className="font-mono font-semibold">{username}</span>&apos;s live files
            and cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="restore-target">Restore target</Label>
          <Select
            value={target}
            onValueChange={(v) => setTarget((v ?? "") as "" | "home")}
          >
            <SelectTrigger id="restore-target" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Staging directory (safe — inspect first)</SelectItem>
              <SelectItem value="home">In place — overwrite live files</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={restore.isPending}
            onClick={(e) => {
              e.preventDefault();
              restore.mutate();
            }}
          >
            {restore.isPending ? "Starting…" : target === "home" ? "Overwrite files" : "Restore"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function AccountBackupsPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  const [destination, setDestination] = useState("");
  const [error, setError] = useState<string | null>(null);

  const key = ["backups", id];
  // Backups are long-running agent tasks, so poll while the page is open to
  // move rows from running → completed/failed without a manual reload.
  const { data: backups, isLoading } = useQuery({
    queryKey: key,
    queryFn: () => listBackups(id),
    refetchInterval: 5000,
  });

  // Destinations are a root-admin surface; a reseller gets 403 here, which is
  // expected — they can still see history, just not start a new backup.
  const { data: destinations } = useQuery({
    queryKey: ["backup-destinations"],
    queryFn: listDestinations,
    retry: false,
  });

  const run = useMutation({
    mutationFn: () => runBackup(id, destination),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not start backup"),
  });

  const canRun = Boolean(destination) && !run.isPending;

  return (
    <div>
      <Link
        href={`/accounts/${id}`}
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> {account?.username ?? "Account"}
      </Link>
      <PageHeader
        title="Backups"
        description="Incremental, deduplicated snapshots of this account's files and databases."
      />

      <Card>
        <CardContent className="p-4">
          {destinations && destinations.length > 0 ? (
            <div className="flex flex-wrap items-end gap-2">
              <div className="grid flex-1 gap-1.5">
                <Label htmlFor="backup-destination">Destination</Label>
                <Select
                  value={destination}
                  onValueChange={(v) => setDestination(v ?? "")}
                >
                  <SelectTrigger id="backup-destination" className="w-full">
                    <SelectValue placeholder="Choose a repository…" />
                  </SelectTrigger>
                  <SelectContent>
                    {destinations.map((d) => (
                      <SelectItem key={d.id} value={d.id}>
                        {d.name} ({d.kind})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <Button onClick={() => run.mutate()} disabled={!canRun}>
                <Play className="h-4 w-4" /> {run.isPending ? "Starting…" : "Back up now"}
              </Button>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              No backup destinations are configured yet.{" "}
              <Link href="/backups" className="text-primary hover:underline">
                Add one
              </Link>{" "}
              to enable backups for this account.
            </p>
          )}
          {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : (backups ?? []).length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              <ArchiveRestore className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
              No backups yet.
            </p>
          ) : (
            <div className="divide-y divide-border">
              {(backups ?? []).map((b) => (
                <div key={b.id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-3">
                  <div className="min-w-0">
                    <p className="truncate text-sm">
                      <span className="font-mono">{b.snapshot_id || "—"}</span>
                      <span className="ml-2 text-muted-foreground">{b.kind}</span>
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {new Date(b.started_at).toLocaleString()}
                      {b.size_bytes > 0 && ` · ${formatBytes(b.size_bytes)}`}
                    </p>
                    {b.error && <p className="mt-0.5 text-xs text-destructive">{b.error}</p>}
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge variant={statusVariant(b.status)}>{b.status}</Badge>
                    {b.status === "completed" && b.snapshot_id && b.kind !== "restore" && (
                      <RestoreDialog
                        accountId={id}
                        backup={b}
                        username={account?.username ?? "this account"}
                      />
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
