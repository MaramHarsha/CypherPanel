"use client";

import { useQuery } from "@tanstack/react-query";
import { Activity, HardDrive, MemoryStick, Server } from "lucide-react";
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
import { listServers, type ServerInfo } from "@/lib/api";

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
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-sm">
        <span className="flex items-center gap-1.5 text-muted-foreground">
          <Icon className="h-3.5 w-3.5" />
          {label}
        </span>
        <span>
          {formatBytes(used)} / {formatBytes(total)}
        </span>
      </div>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full bg-primary transition-all"
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

function ServerCard({ server }: { server: ServerInfo }) {
  const online = server.agent_status === "online";
  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div>
          <CardTitle className="flex items-center gap-2 text-base">
            <Server className="h-4 w-4" />
            {server.name}
          </CardTitle>
          <CardDescription>{server.ip_address}</CardDescription>
        </div>
        <Badge variant={online ? "default" : "destructive"}>
          {server.agent_status}
        </Badge>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between text-sm">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <Activity className="h-3.5 w-3.5" />
            Load (1m)
          </span>
          <span>{server.load_1m?.toFixed(2) ?? "—"}</span>
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

export default function DashboardPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["servers"],
    queryFn: listServers,
    refetchInterval: 15_000, // live-ish: heartbeats land every 30s
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold">Dashboard</h1>
        <p className="text-sm text-muted-foreground">
          Fleet overview — live stats from agent heartbeats.
        </p>
      </div>

      {isLoading && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-48 rounded-xl" />
          ))}
        </div>
      )}

      {error && (
        <Card className="border-destructive/50">
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
            <Server className="mb-2 h-8 w-8 text-muted-foreground" />
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
