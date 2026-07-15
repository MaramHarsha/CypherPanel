"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Store } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import { ApiError, createReseller, listResellers } from "@/lib/api";

function limit(n?: number) {
  return !n ? "∞" : n.toLocaleString();
}

function CreateResellerDialog() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({
    username: "",
    email: "",
    password: "",
    max_accounts: 0,
    max_disk_mb: 0,
  });
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => createReseller(form),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["resellers"] });
      setOpen(false);
      setForm({ username: "", email: "", password: "", max_accounts: 0, max_disk_mb: 0 });
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create reseller"),
  });

  const ready = form.username && form.email && form.password.length >= 12;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button />}>
        <Plus className="h-4 w-4" /> New reseller
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New reseller</DialogTitle>
          <DialogDescription>
            Resellers create their own packages and accounts within the resource
            pool you allocate. 0 = unlimited.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="r-username">Username</Label>
            <Input
              id="r-username"
              placeholder="acme-hosting"
              value={form.username}
              onChange={(e) => setForm((f) => ({ ...f, username: e.target.value }))}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="r-email">Email</Label>
            <Input
              id="r-email"
              type="email"
              value={form.email}
              onChange={(e) => setForm((f) => ({ ...f, email: e.target.value }))}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="r-password">Password (12+ chars)</Label>
            <Input
              id="r-password"
              type="password"
              value={form.password}
              onChange={(e) => setForm((f) => ({ ...f, password: e.target.value }))}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="r-max-accounts">Max accounts</Label>
              <Input
                id="r-max-accounts"
                type="number"
                min={0}
                value={form.max_accounts}
                onChange={(e) => setForm((f) => ({ ...f, max_accounts: Number(e.target.value) }))}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="r-max-disk">Max disk (MB)</Label>
              <Input
                id="r-max-disk"
                type="number"
                min={0}
                value={form.max_disk_mb}
                onChange={(e) => setForm((f) => ({ ...f, max_disk_mb: Number(e.target.value) }))}
              />
            </div>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button onClick={() => create.mutate()} disabled={!ready || create.isPending}>
            {create.isPending ? "Creating…" : "Create reseller"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function ResellersPage() {
  const { data, isLoading } = useQuery({ queryKey: ["resellers"], queryFn: listResellers });

  return (
    <div>
      <PageHeader
        title="Resellers"
        description="Allocate resource pools to resellers who manage their own accounts."
      >
        <CreateResellerDialog />
      </PageHeader>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : data && data.length > 0 ? (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">Reseller</th>
                    <th className="py-2 pr-4 font-medium">Email</th>
                    <th className="py-2 pr-4 font-medium">Accounts</th>
                    <th className="py-2 pr-4 font-medium">Disk cap</th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((r) => (
                    <tr
                      key={r.id}
                      className="border-b transition-colors last:border-0 hover:bg-muted/50"
                    >
                      <td className="py-2.5 pr-4 font-medium">{r.username}</td>
                      <td className="py-2.5 pr-4 text-muted-foreground">{r.email}</td>
                      <td className="py-2 pr-4">
                        {r.account_count ?? 0} / {limit(r.max_accounts)}
                      </td>
                      <td className="py-2 pr-4">
                        {r.max_disk_mb ? `${limit(r.max_disk_mb)} MB` : "∞"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="flex flex-col items-center py-12 text-center">
              <Store className="mb-2 h-8 w-8 text-muted-foreground" />
              <CardTitle className="text-base">No resellers yet</CardTitle>
              <CardDescription>
                Create a reseller to delegate account management within a quota.
              </CardDescription>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
