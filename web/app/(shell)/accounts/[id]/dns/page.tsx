"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Globe, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/page-header";
import { ApiError, dnsDelete, dnsList, dnsRecordTypes, dnsUpsert } from "@/lib/api";
import { useAccount } from "../../use-account";

// Per-type placeholder so the value field always shows the right shape
// instead of one generic hint that only makes sense for A records.
const valueHints: Record<string, string> = {
  A: "e.g. 192.0.2.10",
  AAAA: "e.g. 2001:db8::1",
  CNAME: "e.g. target.example.com",
  MX: "priority host, e.g. 10 mail.example.com",
  TXT: 'e.g. "v=spf1 include:_spf.example.com ~all"',
  SRV: "priority weight port target, e.g. 10 5 5060 sip.example.com",
  CAA: '0 issue "letsencrypt.org"',
  NS: "e.g. ns1.example.com",
};

// Common presets in seconds, human-labeled — DNS TTL is a classic "hard to
// pick" raw number that every other field gets a plain-language label for.
const ttlPresets = [
  { seconds: "300", label: "5 minutes" },
  { seconds: "1800", label: "30 minutes" },
  { seconds: "3600", label: "1 hour (default)" },
  { seconds: "14400", label: "4 hours" },
  { seconds: "86400", label: "1 day" },
];

export default function DNSPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const { account } = useAccount(id);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState({ name: "", type: "A", ttl: "3600", value: "" });

  const key = ["dns", id];
  const { data: records } = useQuery({
    queryKey: key,
    queryFn: () => dnsList(id),
  });
  const { data: types } = useQuery({
    queryKey: ["dns-types"],
    queryFn: dnsRecordTypes,
  });

  const suffix = `.${account?.primary_domain}`;
  const fqdn = (n: string) => {
    const t = n.trim().replace(/\.$/, "");
    if (!t || t === "@") return account?.primary_domain ?? "";
    if (t === account?.primary_domain || t.endsWith(suffix)) return t;
    return `${t}${suffix}`;
  };

  const save = useMutation({
    mutationFn: () =>
      dnsUpsert(id, {
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
    mutationFn: ({ name, type }: { name: string; type: string }) => dnsDelete(id, name, type),
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
        title="DNS zone editor"
        description={
          account
            ? `Records for ${account.primary_domain}'s zone. A name of @ is the apex; short names are completed with the domain.`
            : "Loading…"
        }
      />

      <Card>
        <CardContent className="p-4">
          <div className="grid grid-cols-[1fr_7rem_9rem] gap-x-2 gap-y-1">
            <Label htmlFor="dns-name" className="text-xs text-muted-foreground">Name</Label>
            <Label htmlFor="dns-type" className="text-xs text-muted-foreground">Type</Label>
            <Label htmlFor="dns-ttl" className="text-xs text-muted-foreground">TTL</Label>

            <Input
              id="dns-name"
              placeholder="@ or www"
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
            <Select value={form.type} onValueChange={(v) => setForm((f) => ({ ...f, type: v ?? "A" }))}>
              <SelectTrigger id="dns-type"><SelectValue /></SelectTrigger>
              <SelectContent>
                {(types ?? ["A"]).map((t) => (
                  <SelectItem key={t} value={t}>{t}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={form.ttl} onValueChange={(v) => setForm((f) => ({ ...f, ttl: v ?? "3600" }))}>
              <SelectTrigger id="dns-ttl">
                <SelectValue>
                  {(v: string) => ttlPresets.find((t) => t.seconds === v)?.label ?? v}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {ttlPresets.map((t) => (
                  <SelectItem key={t.seconds} value={t.seconds}>{t.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>

            <div className="col-span-3 grid gap-1">
              <Label htmlFor="dns-value" className="text-xs text-muted-foreground">Value</Label>
              <div className="flex gap-2">
                <Input
                  id="dns-value"
                  className="flex-1"
                  placeholder={valueHints[form.type] ?? "value"}
                  value={form.value}
                  onChange={(e) => setForm((f) => ({ ...f, value: e.target.value }))}
                />
                <Button onClick={() => save.mutate()} disabled={!form.name || !form.value || save.isPending}>
                  <Plus className="h-4 w-4" /> Add
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                Multiple values (e.g. several MX hosts) go one per line.
              </p>
            </div>
          </div>
          {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardContent className="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="py-2 pl-4 pr-2 font-medium">Name</th>
                <th className="py-2 pr-2 font-medium">Type</th>
                <th className="py-2 pr-2 font-medium">TTL</th>
                <th className="py-2 pr-2 font-medium">Value</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(records ?? []).length === 0 ? (
                <tr>
                  <td colSpan={5} className="py-8 text-center text-muted-foreground">
                    <Globe className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
                    No records yet.
                  </td>
                </tr>
              ) : (
                (records ?? []).map((r) => (
                  <tr key={`${r.name}-${r.type}`} className="border-b last:border-0">
                    <td className="py-1.5 pl-4 pr-2 font-mono text-xs">{r.name}</td>
                    <td className="py-1.5 pr-2">{r.type}</td>
                    <td className="py-1.5 pr-2 tabular-nums">{r.ttl}</td>
                    <td className="py-1.5 pr-2 font-mono text-xs break-all">{r.contents.join(", ")}</td>
                    <td className="py-1.5 pr-2">
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
                ))
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </div>
  );
}
