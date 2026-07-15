"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, ExternalLink, Eye, Plus, Trash2 } from "lucide-react";
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
  adminerHandoff,
  ApiError,
  createDatabase,
  deleteDatabase,
  listDatabases,
  revealDBPassword,
  type AccountInfo,
  type DBCredentials,
} from "@/lib/api";

// openAdminer builds a one-shot auto-submitting POST form and opens Adminer in
// a new tab, logged in with the account's (least-privilege) DB credentials.
function openAdminer(h: {
  url: string;
  driver: string;
  server: string;
  username: string;
  password: string;
  db: string;
}) {
  const win = window.open("about:blank", "_blank");
  if (!win) return;
  const fields: Record<string, string> = {
    "auth[driver]": h.driver,
    "auth[server]": h.server,
    "auth[username]": h.username,
    "auth[password]": h.password,
    "auth[db]": h.db,
  };
  const inputs = Object.entries(fields)
    .map(
      ([k, v]) =>
        `<input type="hidden" name="${k}" value="${v.replace(/"/g, "&quot;")}">`,
    )
    .join("");
  win.document.write(
    `<form id="a" method="post" action="${h.url}">${inputs}</form><script>document.getElementById('a').submit()</script>`,
  );
}

export function DatabasesDialog({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creds, setCreds] = useState<(DBCredentials & { db: string }) | null>(null);

  const key = ["databases", account.id];
  const { data: dbs } = useQuery({
    queryKey: key,
    queryFn: () => listDatabases(account.id!),
    enabled: open,
    refetchInterval: open ? 3000 : false, // reflect creating → active
  });

  const create = useMutation({
    mutationFn: () => createDatabase(account.id!, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setName("");
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create database"),
  });

  const remove = useMutation({
    mutationFn: (id: string) => deleteDatabase(account.id!, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  const reveal = useMutation({
    mutationFn: ({ id, db }: { id: string; db: string }) =>
      revealDBPassword(account.id!, id).then((c) => ({ ...c, db })),
    onSuccess: (c) => setCreds(c),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not reveal password"),
  });

  const adminer = useMutation({
    mutationFn: (id: string) => adminerHandoff(account.id!, id),
    onSuccess: (h) => openAdminer(h),
    onError: (e) =>
      setError(e instanceof ApiError ? e.message : "Adminer is not configured"),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => { setOpen(o); if (!o) setCreds(null); }}>
      <Button variant="ghost" size="icon" aria-label="Databases" onClick={() => setOpen(true)}>
        <Database className="h-4 w-4" />
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Databases — {account.username}</DialogTitle>
          <DialogDescription>
            MariaDB databases for this account. Names are prefixed with the
            account&apos;s system user.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-2">
          <Input
            placeholder="name (e.g. blog)"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && name) create.mutate();
            }}
          />
          <Button onClick={() => create.mutate()} disabled={!name || create.isPending}>
            <Plus className="h-4 w-4" /> Create
          </Button>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}

        {creds && (
          <div className="rounded-lg border border-primary/30 bg-primary/5 p-3 text-sm">
            <p className="mb-1 font-medium">{creds.db}</p>
            <p className="font-mono text-xs">
              user: {creds.username}@{creds.host}
            </p>
            <p className="font-mono text-xs break-all">password: {creds.password}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              Copy it now — shown here on request only.
            </p>
          </div>
        )}

        <div className="divide-y divide-border">
          {(dbs ?? []).length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">No databases yet.</p>
          ) : (
            (dbs ?? []).map((d) => (
              <div key={d.id} className="flex items-center justify-between py-2">
                <div className="min-w-0">
                  <p className="truncate font-mono text-xs">{d.name}</p>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant={d.status === "active" ? "success" : "secondary"}>
                    {d.status}
                  </Badge>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Open in Adminer"
                    title="Open in Adminer"
                    disabled={d.status !== "active" || adminer.isPending}
                    onClick={() => adminer.mutate(d.id!)}
                  >
                    <ExternalLink className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Reveal password"
                    disabled={d.status !== "active"}
                    onClick={() => reveal.mutate({ id: d.id!, db: d.name! })}
                  >
                    <Eye className="h-4 w-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="Delete database"
                    onClick={() => remove.mutate(d.id!)}
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
