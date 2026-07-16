"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Eye, FolderUp, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  createFTP,
  deleteFTP,
  listFTP,
  revealFTPPassword,
  type FTPCredentials,
} from "@/lib/api";
import { useAccount } from "../../use-account";

export default function FTPPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [creds, setCreds] = useState<FTPCredentials | null>(null);

  const key = ["ftp", id];
  const { data: items } = useQuery({
    queryKey: key,
    queryFn: () => listFTP(id),
    refetchInterval: 3000,
  });

  const create = useMutation({
    mutationFn: () => createFTP(id, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setName("");
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create FTP user"),
  });
  const remove = useMutation({
    mutationFn: (ftpId: string) => deleteFTP(id, ftpId),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });
  const reveal = useMutation({
    mutationFn: (ftpId: string) => revealFTPPassword(id, ftpId),
    onSuccess: (c) => setCreds(c),
    onError: (e) => setError(e instanceof ApiError ? e.message : "Could not reveal password"),
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
        title="FTP accounts"
        description="Pure-FTPd virtual users mapped to this account's files. Names are prefixed with the account's system user."
      />

      <Card>
        <CardContent className="p-4">
          <div className="flex gap-2">
            <Input
              placeholder="name (e.g. deploy)"
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
              <p className="font-mono text-xs">user: {creds.username}</p>
              <p className="font-mono text-xs break-all">home: {creds.home_dir}</p>
              <p className="font-mono text-xs break-all">password: {creds.password}</p>
              <p className="mt-1 text-xs text-muted-foreground">
                Copy it now — shown on request only.
              </p>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            {(items ?? []).length === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">
                <FolderUp className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
                No FTP users yet.
              </p>
            ) : (
              (items ?? []).map((f) => (
                <div key={f.id} className="flex items-center justify-between px-4 py-3">
                  <p className="truncate font-mono text-sm">{f.username}</p>
                  <div className="flex items-center gap-2">
                    <Badge variant={f.status === "active" ? "success" : "secondary"}>
                      {f.status}
                    </Badge>
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
        </CardContent>
      </Card>
    </div>
  );
}
