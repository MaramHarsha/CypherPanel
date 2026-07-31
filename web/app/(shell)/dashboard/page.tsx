"use client";

import { useQuery } from "@tanstack/react-query";
import {
  Activity,
  CheckCircle2,
  HardDrive,
  MemoryStick,
  Server,
  Users,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ServiceChips } from "@/components/service-chips";
import { PageHeader } from "@/components/page-header";
import { cn } from "@/lib/utils";
import {
  getMe,
  listAccounts,
  listPackages,
  listServers,
  type ServerInfo,
} from "@/lib/api";

function formatBytes(n?: number): string {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function StatCard({
  icon: Icon,
  label,
  value,
  hint,
}: {
  icon: typeof Server;
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Card>
      <CardContent className="flex items-center gap-4">
        <span className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/12 text-primary">
          <Icon className="h-5 w-5" />
        </span>
        <div className="min-w-0">
          <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {label}
          </p>
          <p className="mt-0.5 text-2xl font-semibold tabular-nums tracking-tight">
            {value}
          </p>
          {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
        </div>
      </CardContent>
    </Card>
  );
}

function UsageRow({
  icon: Icon,
  label,
  used,
  total,
}: {
  icon: typeof MemoryStick;
  label: string;
  used?: number;
  total?: number;
}) {
  const pct = used && total ? Math.min(100, (used / total) * 100) : 0;
  const bar =
    pct >= 90 ? "bg-destructive" : pct >= 75 ? "bg-warning" : "bg-primary";
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-1.5 text-muted-foreground">
          <Icon className="h-3.5 w-3.5" />
          {label}
        </span>
        <span className="tabular-nums">
          {formatBytes(used)} / {formatBytes(total)}
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className={cn("h-full rounded-full transition-all", bar)}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function ServerCard({ server }: { server: ServerInfo }) {
  const online = server.agent_status === "online";
  return (
    <Card className="hover:shadow-md">
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2 text-base">
            <Server className="h-4 w-4 text-muted-foreground" />
            <span className="truncate">{server.name}</span>
          </CardTitle>
          <CardDescription className="font-mono text-xs">
            {server.ip_address}
          </CardDescription>
        </div>
        <Badge variant={online ? "success" : "destructive"} className="gap-1.5">
          <span
            className={cn(
              "h-1.5 w-1.5 rounded-full",
              online ? "bg-success animate-pulse" : "bg-destructive",
            )}
          />
          {server.agent_status}
        </Badge>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between text-sm">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <Activity className="h-3.5 w-3.5" />
            Load (1m)
          </span>
          <span className="tabular-nums">{server.load_1m?.toFixed(2) ?? "—"}</span>
        </div>
        <UsageRow
          icon={MemoryStick}
          label="Memory"
          used={server.memory_used_bytes}
          total={server.memory_total_bytes}
        />
        <UsageRow
          icon={HardDrive}
          label="Disk"
          used={server.disk_used_bytes}
          total={server.disk_total_bytes}
        />
        <div className="pt-1">
          <ServiceChips services={server.services} />
        </div>
      </CardContent>
    </Card>
  );
}

function ResellerDashboard() {
  const { data: accounts } = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  const { data: packages } = useQuery({ queryKey: ["packages"], queryFn: listPackages });
  const list = accounts ?? [];
  const active = list.filter((a) => a.status === "active").length;
  const suspended = list.filter((a) => a.status === "suspended").length;

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Your hosting accounts and packages at a glance."
      />
      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard icon={Users} label="Accounts" value={String(list.length)} hint="in your pool" />
        <StatCard icon={CheckCircle2} label="Active" value={String(active)} hint="serving" />
        <StatCard icon={Server} label="Suspended" value={String(suspended)} hint="paused" />
        <StatCard
          icon={Activity}
          label="Packages"
          value={packages ? String(packages.length) : "—"}
          hint="plans you offer"
        />
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent accounts</CardTitle>
          <CardDescription>Newest first. Manage them on the Accounts page.</CardDescription>
        </CardHeader>
        <CardContent>
          {list.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted-foreground">
              No accounts yet — create your first from the Accounts page.
            </p>
          ) : (
            <ul className="divide-y divide-border text-sm">
              {list.slice(0, 8).map((a) => (
                <li key={a.id} className="flex items-center justify-between py-2">
                  <span className="font-medium">{a.username}</span>
                  <span className="font-mono text-xs text-muted-foreground">
                    {a.primary_domain}
                  </span>
                  <Badge variant={a.status === "active" ? "success" : "secondary"}>
                    {a.status}
                  </Badge>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export default function DashboardPage() {
  const { data: me } = useQuery({ queryKey: ["me"], queryFn: getMe });
  const { data, isLoading, error } = useQuery({
    queryKey: ["servers"],
    // Wrapped, not passed bare: listServers takes an optional region, and
    // TanStack would hand it the query context as that argument.
    queryFn: () => listServers(),
    refetchInterval: 15_000, // live-ish: heartbeats land every 30s
    enabled: me?.role === "root_admin",
  });
  const { data: accounts } = useQuery({
    queryKey: ["accounts"],
    queryFn: listAccounts,
  });

  // Resellers get an account-focused view, not fleet infrastructure.
  if (me && me.role !== "root_admin") return <ResellerDashboard />;

  const servers = data ?? [];
  const online = servers.filter((s) => s.agent_status === "online").length;
  const memUsed = servers.reduce((a, s) => a + (s.memory_used_bytes ?? 0), 0);
  const memTotal = servers.reduce((a, s) => a + (s.memory_total_bytes ?? 0), 0);

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Fleet overview — live stats from agent heartbeats."
      />

      {/* Summary stats */}
      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={Server}
          label="Servers"
          value={String(servers.length)}
          hint="registered nodes"
        />
        <StatCard
          icon={CheckCircle2}
          label="Online"
          value={`${online} / ${servers.length}`}
          hint="agents reporting"
        />
        <StatCard
          icon={MemoryStick}
          label="Fleet memory"
          value={formatBytes(memUsed)}
          hint={memTotal ? `of ${formatBytes(memTotal)}` : "—"}
        />
        <StatCard
          icon={Users}
          label="Accounts"
          value={accounts ? String(accounts.length) : "—"}
          hint="hosting accounts"
        />
      </div>

      <h2 className="mb-3 text-sm font-semibold text-muted-foreground">Servers</h2>

      {isLoading && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-52 rounded-xl" />
          ))}
        </div>
      )}

      {error && (
        <Card className="ring-destructive/40">
          <CardHeader>
            <CardTitle className="text-base">Couldn&apos;t load servers</CardTitle>
            <CardDescription>
              {error instanceof Error ? error.message : "Unknown error"}
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {data && data.length === 0 && (
        <Card>
          <CardHeader className="items-center py-12 text-center">
            <div className="mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-muted text-muted-foreground">
              <Server className="h-6 w-6" />
            </div>
            <CardTitle className="text-base">No servers yet</CardTitle>
            <CardDescription>
              Install CypherAgent on a server and it will register here
              automatically.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {data && data.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.map((s) => (
            <ServerCard key={s.id} server={s} />
          ))}
        </div>
      )}
    </div>
  );
}
