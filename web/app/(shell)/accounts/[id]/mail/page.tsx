"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Mail as MailIcon, Plus, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/page-header";
import { ApiError, createMail, deleteMail, listMail } from "@/lib/api";
import { useAccount } from "../../use-account";

export default function MailPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  const [form, setForm] = useState({ local: "", password: "", quota: "0" });
  const [error, setError] = useState<string | null>(null);

  const key = ["mail", id];
  const { data: boxes } = useQuery({
    queryKey: key,
    queryFn: () => listMail(id),
    refetchInterval: 3000,
  });

  const create = useMutation({
    mutationFn: () =>
      createMail(id, {
        local_part: form.local,
        password: form.password,
        quota_mb: Number(form.quota) || 0,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setForm({ local: "", password: "", quota: "0" });
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create mailbox"),
  });
  const remove = useMutation({
    mutationFn: (mailId: string) => deleteMail(id, mailId),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
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
        title="Email accounts"
        description={
          account
            ? `Mailboxes at @${account.primary_domain}. MX/SPF/DMARC records are published automatically.`
            : "Loading…"
        }
      />

      <Card>
        <CardContent className="p-4">
          <div className="grid grid-cols-[1fr_auto] items-end gap-2">
            <div className="grid gap-1.5">
              <Label htmlFor="mail-local">Mailbox name</Label>
              <div className="flex items-center gap-1">
                <Input
                  id="mail-local"
                  placeholder="name"
                  value={form.local}
                  onChange={(e) => setForm((f) => ({ ...f, local: e.target.value }))}
                />
                <span className="whitespace-nowrap text-sm text-muted-foreground">
                  @{account?.primary_domain}
                </span>
              </div>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="mail-quota">Quota</Label>
              <Input
                id="mail-quota"
                className="w-24"
                placeholder="MB"
                value={form.quota}
                onChange={(e) => setForm((f) => ({ ...f, quota: e.target.value }))}
              />
            </div>
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="mail-password">Password (8+ chars)</Label>
              <Input
                id="mail-password"
                type="password"
                placeholder="mailbox password"
                value={form.password}
                onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
              />
            </div>
            <Button
              className="col-span-2"
              onClick={() => create.mutate()}
              disabled={!form.local || form.password.length < 8 || create.isPending}
            >
              <Plus className="h-4 w-4" /> Create mailbox
            </Button>
          </div>
          {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            {(boxes ?? []).length === 0 ? (
              <p className="py-8 text-center text-sm text-muted-foreground">
                <MailIcon className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
                No mailboxes yet.
              </p>
            ) : (
              (boxes ?? []).map((m) => (
                <div key={m.id} className="flex items-center justify-between px-4 py-3">
                  <span className="truncate font-mono text-sm">{m.address}</span>
                  <div className="flex items-center gap-3">
                    <span className="text-xs text-muted-foreground">
                      {m.quota_mb ? `${m.quota_mb}MB` : "∞"}
                    </span>
                    <Badge variant={m.status === "active" ? "success" : "secondary"}>{m.status}</Badge>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label="Delete mailbox"
                      onClick={() => remove.mutate(m.id)}
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
