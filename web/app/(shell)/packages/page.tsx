"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
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
import { Switch } from "@/components/ui/switch";
import { PageHeader } from "@/components/page-header";
import {
  ApiError,
  createPackage,
  deletePackage,
  listPackages,
  type PackageLimits,
} from "@/lib/api";

const limitFields: { key: keyof PackageLimits; label: string; unit?: string; hint: string }[] = [
  { key: "disk_mb", label: "Disk space", unit: "MB", hint: "Total storage this account may use." },
  { key: "bandwidth_mb", label: "Bandwidth", unit: "MB/mo", hint: "Monthly data transfer allowance." },
  { key: "domains", label: "Domains", hint: "Addon domains, subdomains, and aliases combined." },
  { key: "databases", label: "Databases", hint: "MariaDB databases the account may create." },
  { key: "email_accounts", label: "Email accounts", hint: "Mailboxes the account may create." },
  { key: "cpu_quota_pct", label: "CPU quota", unit: "%", hint: "Max CPU share under the account's cgroup slice." },
  { key: "memory_max_mb", label: "Memory limit", unit: "MB", hint: "Hard memory cap enforced by the account's cgroup slice." },
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

// Number input + "Unlimited" switch, matching the number+unlimited-checkbox
// pattern found in Froxlor's `input_ul` and HestiaCP's infinity-icon toggle —
// both exist because a bare "0 = unlimited" convention (which this API uses)
// is easy to miss unless the UI makes "unlimited" its own explicit choice.
function LimitField({
  field,
  value,
  onChange,
}: {
  field: (typeof limitFields)[number];
  value: number;
  onChange: (v: number) => void;
}) {
  const unlimited = value === 0;
  return (
    <div className="grid gap-1.5 rounded-lg border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <Label htmlFor={field.key} className="text-sm font-medium">
          {field.label}
        </Label>
        <label className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          Unlimited
          <Switch
            size="sm"
            checked={unlimited}
            onCheckedChange={(checked) => onChange(checked ? 0 : 1)}
          />
        </label>
      </div>
      <div className="flex items-center gap-2">
        <Input
          id={field.key}
          type="number"
          min={1}
          disabled={unlimited}
          value={unlimited ? "" : value}
          placeholder={unlimited ? "Unlimited" : "0"}
          onChange={(e) => onChange(Number(e.target.value))}
        />
        {field.unit && (
          <span className="shrink-0 text-xs text-muted-foreground">{field.unit}</span>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{field.hint}</p>
    </div>
  );
}

function PackagesPageInner() {
  const qc = useQueryClient();
  const router = useRouter();
  const searchParams = useSearchParams();
  const { data, isLoading } = useQuery({ queryKey: ["packages"], queryFn: listPackages });

  // Deep-linked from the command palette's "New package" quick action.
  const [open, setOpen] = useState(() => searchParams.get("new") === "1");
  useEffect(() => {
    if (searchParams.get("new") === "1") {
      router.replace("/packages", { scroll: false });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
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
    <div>
      <PageHeader
        title="Packages"
        description="Hosting templates that define an account's resource limits."
      >
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger render={<Button />}>
            <Plus className="h-4 w-4" /> New package
          </DialogTrigger>
          <DialogContent className="sm:max-w-xl">
            <DialogHeader>
              <DialogTitle>New package</DialogTitle>
              <DialogDescription>
                Set the resource limits for accounts on this plan.
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
                  <LimitField
                    key={f.key}
                    field={f}
                    value={limits[f.key] ?? 0}
                    onChange={(v) => setLimits((l) => ({ ...l, [f.key]: v }))}
                  />
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
      </PageHeader>

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

export default function PackagesPage() {
  return (
    <Suspense fallback={null}>
      <PackagesPageInner />
    </Suspense>
  );
}
