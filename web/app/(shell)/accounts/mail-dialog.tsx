"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Plus, Trash2 } from "lucide-react";
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
  createMail,
  deleteMail,
  listMail,
  type AccountInfo,
} from "@/lib/api";

export function MailDialog({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({ local: "", password: "", quota: "0" });
  const [error, setError] = useState<string | null>(null);

  const key = ["mail", account.id];
  const { data: boxes } = useQuery({
    queryKey: key,
    queryFn: () => listMail(account.id!),
    enabled: open,
    refetchInterval: open ? 3000 : false,
  });

  const create = useMutation({
    mutationFn: () =>
      createMail(account.id!, {
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
    mutationFn: (id: string) => deleteMail(account.id!, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button variant="ghost" size="icon" aria-label="Email accounts" onClick={() => setOpen(true)}>
        <Mail className="h-4 w-4" />
      </Button>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Email — {account.username}</DialogTitle>
          <DialogDescription>
            Mailboxes at <span className="font-mono">@{account.primary_domain}</span>.
            MX/SPF/DMARC records are published automatically.
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-[1fr_auto] items-end gap-2">
          <div className="flex items-center gap-1">
            <Input
              placeholder="name"
              value={form.local}
              onChange={(e) => setForm((f) => ({ ...f, local: e.target.value }))}
            />
            <span className="whitespace-nowrap text-sm text-muted-foreground">
              @{account.primary_domain}
            </span>
          </div>
          <Input
            className="w-24"
            placeholder="quota MB"
            value={form.quota}
            onChange={(e) => setForm((f) => ({ ...f, quota: e.target.value }))}
          />
          <Input
            className="col-span-2"
            type="password"
            placeholder="mailbox password (8+ chars)"
            value={form.password}
            onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
          />
          <Button
            className="col-span-2"
            onClick={() => create.mutate()}
            disabled={!form.local || form.password.length < 8 || create.isPending}
          >
            <Plus className="h-4 w-4" /> Create mailbox
          </Button>
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}

        <div className="divide-y divide-border">
          {(boxes ?? []).length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">No mailboxes yet.</p>
          ) : (
            (boxes ?? []).map((m) => (
              <div key={m.id} className="flex items-center justify-between py-2">
                <span className="truncate font-mono text-xs">{m.address}</span>
                <div className="flex items-center gap-2">
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
      </DialogContent>
    </Dialog>
  );
}
