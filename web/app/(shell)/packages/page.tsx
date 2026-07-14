"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Package, Plus, Trash2 } from "lucide-react";
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
import {
  ApiError,
  createPackage,
  deletePackage,
  listPackages,
  type PackageLimits,
} from "@/lib/api";

const limitFields: { key: keyof PackageLimits; label: string; hint: string }[] = [
  { key: "disk_mb", label: "Disk (MB)", hint: "0 = unlimited" },
  { key: "bandwidth_mb", label: "Bandwidth (MB/mo)", hint: "0 = unlimited" },
  { key: "domains", label: "Domains", hint: "0 = unlimited" },
  { key: "databases", label: "Databases", hint: "0 = unlimited" },
  { key: "email_accounts", label: "Email accounts", hint: "0 = unlimited" },
  { key: "cpu_quota_pct", label: "CPU quota (%)", hint: "0 = no cap" },
  { key: "memory_max_mb", label: "Memory max (MB)", hint: "0 = no cap" },
];

const emptyLimits: PackageLimits = {
  disk_mb: 0,
  bandwidth_mb: 0,
  domains: 0,
  databases: 0,
  email_accounts: 0,
  cpu_quota_pct: 0,
  memory_max_mb: 0,
};

export default function PackagesPage() {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({ queryKey: ["packages"], queryFn: listPackages });

  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [limits, setLimits] = useState<PackageLimits>(emptyLimits);
  const [error, setError] = useState<string | null>(null);

  const create = useMutation({
    mutationFn: () => createPackage(name, limits),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["packages"] });
      setOpen(false);
      setName("");
      setLimits(emptyLimits);
      setError(null);
    },
    onError: (e) => setError(e instanceof ApiError ? e.message : "Failed to create package"),
  });

  const remove = useMutation({
    mutationFn: (id: string) => deletePackage(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["packages"] }),
  });

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold">Packages</h1>
          <p className="text-sm text-muted-foreground">
            Hosting templates that define an account&apos;s resource limits.
          </p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger render={<Button />}>
            <Plus className="h-4 w-4" /> New package
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>New package</DialogTitle>
              <DialogDescription>
                Set the resource limits for accounts on this plan. 0 means unlimited.
              </DialogDescription>
            </DialogHeader>
            <div className="grid gap-4">
              <div className="grid gap-2">
                <Label htmlFor="pkg-name">Name</Label>
                <Input
                  id="pkg-name"
                  placeholder="Starter"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                {limitFields.map((f) => (
                  <div key={f.key} className="grid gap-1.5">
                    <Label htmlFor={f.key} className="text-xs">
                      {f.label}
                    </Label>
                    <Input
                      id={f.key}
                      type="number"
                      min={0}
                      value={limits[f.key]}
                      onChange={(e) =>
                        setLimits((l) => ({ ...l, [f.key]: Number(e.target.value) }))
                      }
                    />
                  </div>
                ))}
              </div>
              {error && <p className="text-sm text-destructive">{error}</p>}
            </div>
            <DialogFooter>
              <Button
                onClick={() => create.mutate()}
                disabled={!name || create.isPending}
              >
                {create.isPending ? "Creating…" : "Create package"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-40 rounded-xl" />
          ))}
        </div>
      ) : data && data.length > 0 ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((p) => (
            <Card key={p.id}>
              <CardHeader className="flex flex-row items-start justify-between space-y-0">
                <CardTitle className="flex items-center gap-2 text-base">
                  <Package className="h-4 w-4" />
                  {p.name}
                </CardTitle>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Delete ${p.name}`}
                  onClick={() => remove.mutate(p.id!)}
                  disabled={remove.isPending}
                >
                  <Trash2 className="h-4 w-4 text-destructive" />
                </Button>
              </CardHeader>
              <CardContent className="text-sm text-muted-foreground">
                <dl className="grid grid-cols-2 gap-x-4 gap-y-1">
                  <dt>Disk</dt>
                  <dd className="text-right text-foreground">
                    {p.limits?.disk_mb ? `${p.limits.disk_mb} MB` : "∞"}
                  </dd>
                  <dt>Domains</dt>
                  <dd className="text-right text-foreground">
                    {p.limits?.domains || "∞"}
                  </dd>
                  <dt>Databases</dt>
                  <dd className="text-right text-foreground">
                    {p.limits?.databases || "∞"}
                  </dd>
                  <dt>Memory</dt>
                  <dd className="text-right text-foreground">
                    {p.limits?.memory_max_mb ? `${p.limits.memory_max_mb} MB` : "no cap"}
                  </dd>
                </dl>
              </CardContent>
            </Card>
          ))}
        </div>
      ) : (
        <Card>
          <CardHeader className="items-center py-12 text-center">
            <Package className="mb-2 h-8 w-8 text-muted-foreground" />
            <CardTitle className="text-base">No packages yet</CardTitle>
            <CardDescription>
              Create a package before provisioning hosting accounts.
            </CardDescription>
          </CardHeader>
        </Card>
      )}
    </div>
  );
}
