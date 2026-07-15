"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Plus, Trash2 } from "lucide-react";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  ApiError,
  dnsDelete,
  dnsList,
  dnsRecordTypes,
  dnsUpsert,
  type AccountInfo,
} from "@/lib/api";

export function DNSDialog({ account }: { account: AccountInfo }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState({ name: "", type: "A", ttl: "3600", value: "" });

  const key = ["dns", account.id];
  const { data: records } = useQuery({
    queryKey: key,
    queryFn: () => dnsList(account.id!),
    enabled: open,
  });
  const { data: types } = useQuery({
    queryKey: ["dns-types"],
    queryFn: dnsRecordTypes,
    enabled: open,
  });

  const suffix = `.${account.primary_domain}`;
  const fqdn = (n: string) => {
    const t = n.trim().replace(/\.$/, "");
    if (!t || t === "@") return account.primary_domain!;
    if (t === account.primary_domain || t.endsWith(suffix)) return t;
    return `${t}${suffix}`;
  };

  const save = useMutation({
    mutationFn: () =>
      dnsUpsert(account.id!, {
        name: fqdn(form.name),
        type: form.type,
        ttl: Number(form.ttl) || 3600,
        contents: form.value.split("\n").map((v) => v.trim()).filter(Boolean),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      setForm({ name: "", type: "A", ttl: "3600", value: "" });
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to save record"),
  });
  const remove = useMutation({
    mutationFn: ({ name, type }: { name: string; type: string }) => dnsDelete(account.id!, name, type),
    onSuccess: () => qc.invalidateQueries({ queryKey: key }),
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button variant="ghost" size="icon" aria-label="DNS zone editor" onClick={() => setOpen(true)}>
        <Globe className="h-4 w-4" />
      </Button>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>DNS — {account.primary_domain}</DialogTitle>
          <DialogDescription>
            Records for this account&apos;s zone. A name of <code>@</code> is the
            apex; short names are completed with the domain.
          </DialogDescription>
        </DialogHeader>

        <div className="grid grid-cols-[1fr_7rem_5rem_auto] gap-2">
          <Input
            placeholder="name (@ or www)"
            value={form.name}
            onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          />
          <Select value={form.type} onValueChange={(v) => setForm((f) => ({ ...f, type: v ?? "A" }))}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              {(types ?? ["A"]).map((t) => (
                <SelectItem key={t} value={t}>{t}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            placeholder="TTL"
            value={form.ttl}
            onChange={(e) => setForm((f) => ({ ...f, ttl: e.target.value }))}
          />
          <Button onClick={() => save.mutate()} disabled={!form.name || !form.value || save.isPending}>
            <Plus className="h-4 w-4" />
          </Button>
          <Input
            className="col-span-4"
            placeholder="value (e.g. 1.2.3.4 · 10 mail.example.com · one per line)"
            value={form.value}
            onChange={(e) => setForm((f) => ({ ...f, value: e.target.value }))}
          />
        </div>
        {error && <p className="text-sm text-destructive">{error}</p>}

        <div className="max-h-[45vh] overflow-y-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="py-2 pr-2 font-medium">Name</th>
                <th className="py-2 pr-2 font-medium">Type</th>
                <th className="py-2 pr-2 font-medium">TTL</th>
                <th className="py-2 pr-2 font-medium">Value</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(records ?? []).map((r) => (
                <tr key={`${r.name}-${r.type}`} className="border-b last:border-0">
                  <td className="py-1.5 pr-2 font-mono text-xs">{r.name}</td>
                  <td className="py-1.5 pr-2">{r.type}</td>
                  <td className="py-1.5 pr-2 tabular-nums">{r.ttl}</td>
                  <td className="py-1.5 pr-2 font-mono text-xs break-all">{r.contents.join(", ")}</td>
                  <td className="py-1.5">
                    {r.type !== "SOA" && r.type !== "NS" && (
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="Delete record"
                        onClick={() => remove.mutate({ name: r.name, type: r.type })}
                      >
                        <Trash2 className="h-3.5 w-3.5 text-destructive" />
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </DialogContent>
    </Dialog>
  );
}
