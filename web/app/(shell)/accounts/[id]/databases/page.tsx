"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Database, ExternalLink, Eye, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/page-header";
import {
  adminerHandoff,
  ApiError,
  createDatabase,
  deleteDatabase,
  listDatabases,
  revealDBPassword,
  type DBCredentials,
} from "@/lib/api";
import { useAccount } from "../../use-account";

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

export default function DatabasesPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creds, setCreds] = useState<(DBCredentials & { db: string }) | null>(null);

  const key = ["databases", id];
  const { data: dbs } = useQuery({
    queryKey: key,
    queryFn: () => listDatabases(id),
    refetchInterval: 3000, // reflect creating → active
  });

  const create = useMutation({
    mutationFn: () => createDatabase(id, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setName("");
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create database"),
  });

  const remove = useMutation({
    mutationFn: (dbId: string) => deleteDatabase(id, dbId),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  const reveal = useMutation({
    mutationFn: ({ dbId, db }: { dbId: string; db: string }) =>
      revealDBPassword(id, dbId).then((c) => ({ ...c, db })),
    onSuccess: (c) => setCreds(c),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not reveal password"),
  });

  const adminer = useMutation({
    mutationFn: (dbId: string) => adminerHandoff(id, dbId),
    onSuccess: (h) => openAdminer(h),
    onError: (e) =>
      setError(e instanceof ApiError ? e.message : "Adminer is not configured"),
  });

  return (
    <div>
      <Link
        href={`/accounts/${id}`}
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> {account?.username ?? "Account"}
      </Link>
      <PageHeader
        title="Databases"
        description="MariaDB databases for this account. Names are prefixed with the account's system user."
      />

      <Card>
        <CardContent className="p-4">
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
          {error && <p className="mt-2 text-sm text-destructive">{error}</p>}

          {creds && (
            <div className="mt-3 rounded-lg border border-primary/30 bg-primary/5 p-3 text-sm">
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
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            {(dbs ?? []).length === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">
                <Database className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
                No databases yet.
              </p>
            ) : (
              (dbs ?? []).map((d) => (
                <div key={d.id} className="flex items-center justify-between px-4 py-3">
                  <div className="min-w-0">
                    <p className="truncate font-mono text-sm">{d.name}</p>
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
                      onClick={() => reveal.mutate({ dbId: d.id!, db: d.name! })}
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
        </CardContent>
      </Card>
    </div>
  );
}
