"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, FolderUp, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  ApiError,
  createFTP,
  deleteFTP,
  listFTP,
  revealFTPPassword,
  type AccountInfo,
  type FTPCredentials,
} from "@/lib/api";

export function FTPDialog({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creds, setCreds] = useState<FTPCredentials | null>(null);

  const key = ["ftp", account.id];
  const { data: items } = useQuery({
    queryKey: key,
    queryFn: () => listFTP(account.id!),
    enabled: open,
    refetchInterval: open ? 3000 : false,
  });

  const create = useMutation({
    mutationFn: () => createFTP(account.id!, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setName("");
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create FTP user"),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteFTP(account.id!, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const reveal = useMutation({
    mutationFn: (id: string) => revealFTPPassword(account.id!, id),
    onSuccess: (c) => setCreds(c),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not reveal password"),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setCreds(null); }}>
      <Button variant="ghost" size="icon" aria-label="FTP accounts" onClick={() => setOpen(true)}>
        <FolderUp className="h-4 w-4" />
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>FTP accounts — {account.username}</DialogTitle>
          <DialogDescription>
            Pure-FTPd virtual users mapped to this account&apos;s files. Names are
            prefixed with the account&apos;s system user.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-2">
          <Input
            placeholder="name (e.g. deploy)"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter" && name) create.mutate(); }}
          />
          <Button onClick={() => create.mutate()} disabled={!name || create.isPending}>
            <Plus className="h-4 w-4" /> Create
          </Button>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}

        {creds && (
          <div className="rounded-lg border border-primary/30 bg-primary/5 p-3 text-sm">
            <p className="font-mono text-xs">user: {creds.username}</p>
            <p className="font-mono text-xs break-all">home: {creds.home_dir}</p>
            <p className="font-mono text-xs break-all">password: {creds.password}</p>
            <p className="mt-1 text-xs text-muted-foreground">Copy it now — shown on request only.</p>
          </div>
        )}

        <div className="divide-y divide-border">
          {(items ?? []).length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">No FTP users yet.</p>
          ) : (
            (items ?? []).map((f) => (
              <div key={f.id} className="flex items-center justify-between py-2">
                <p className="truncate font-mono text-xs">{f.username}</p>
                <div className="flex items-center gap-2">
                  <Badge variant={f.status === "active" ? "success" : "secondary"}>{f.status}</Badge>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Reveal password"
                    disabled={f.status !== "active"}
                    onClick={() => reveal.mutate(f.id)}
                  >
                    <Eye className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Delete FTP user"
                    onClick={() => remove.mutate(f.id)}
                    disabled={remove.isPending}
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
