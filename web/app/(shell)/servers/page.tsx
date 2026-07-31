"use client";

import { Fragment, useMemo, useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { listServers, type ServerInfo } from "@/lib/api";

function since(iso?: string | null): string {
  if (!iso) return "never";
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 90) return `${Math.round(secs)}s ago`;
  if (secs < 5400) return `${Math.round(secs / 60)}m ago`;
  return `${Math.round(secs / 3600)}h ago`;
}

const ALL_REGIONS = "__all__";
const UNASSIGNED = "Unassigned";

// Group by region so a multi-region fleet reads as regions-of-servers rather
// than one flat list where nothing says where a node actually lives.
function groupByRegion(servers: ServerInfo[]): [string, ServerInfo[]][] {
  const groups = new Map<string, ServerInfo[]>();
  for (const s of servers) {
    const key = s.region?.trim() || UNASSIGNED;
    const bucket = groups.get(key);
    if (bucket) bucket.push(s);
    else groups.set(key, [s]);
  }
  // Named regions first, alphabetically; unassigned last — it is the leftover
  // bucket, not a region.
  return [...groups.entries()].sort(([a], [b]) => {
    if (a === UNASSIGNED) return 1;
    if (b === UNASSIGNED) return -1;
    return a.localeCompare(b);
  });
}

export default function ServersPage() {
  const [region, setRegion] = useState(ALL_REGIONS);
  const { data, isLoading } = useQuery({
    queryKey: ["servers"],
    queryFn: () => listServers(),
    refetchInterval: 15_000,
  });

  // Memoised so the fallback [] is not a fresh array on every render, which
  // would invalidate the deps of the useMemo below each time.
  const servers = useMemo(() => data ?? [], [data]);
  // Filtering is client-side over the already-fetched fleet so switching
  // regions is instant and doesn't refetch. The API's ?region= filter exists
  // for clients (cypherctl, SDKs) that want one region directly.
  const regions = useMemo(
    () =>
      [...new Set(servers.map((s) => s.region?.trim()).filter(Boolean))].sort() as string[],
    [servers],
  );
  const visible =
    region === ALL_REGIONS ? servers : servers.filter((s) => (s.region ?? "") === region);
  const grouped = groupByRegion(visible);

  return (
    <div>
      <PageHeader
        title="Servers"
        description="Every node running CypherAgent, with registration and liveness state."
      >
        {regions.length > 0 && (
          <Select value={region} onValueChange={(v) => setRegion(v ?? ALL_REGIONS)}>
            <SelectTrigger size="sm" aria-label="Filter by region">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_REGIONS}>All regions</SelectItem>
              {regions.map((r) => (
                <SelectItem key={r} value={r}>
                  {r}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </PageHeader>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Registered nodes</CardTitle>
          <CardDescription>
            Agents self-register over mTLS and heartbeat every 30 seconds.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="space-y-2">
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">Name</th>
                    <th className="py-2 pr-4 font-medium">IP address</th>
                    <th className="py-2 pr-4 font-medium">Status</th>
                    <th className="py-2 pr-4 font-medium">Services</th>
                    <th className="py-2 pr-4 font-medium">Last seen</th>
                  </tr>
                </thead>
                <tbody>
                  {grouped.map(([regionName, nodes]) => (
                    <Fragment key={regionName}>
                      {/* Only show region headers when the fleet actually spans
                          more than one — a single-region install shouldn't pay
                          for a grouping it doesn't have. */}
                      {grouped.length > 1 && (
                        <tr className="bg-muted/40">
                          <th
                            colSpan={5}
                            scope="colgroup"
                            className="py-1.5 pr-4 text-left text-xs font-medium text-muted-foreground"
                          >
                            {regionName}
                            <span className="ml-2 font-normal">
                              {nodes.length} node{nodes.length === 1 ? "" : "s"}
                            </span>
                          </th>
                        </tr>
                      )}
                      {nodes.map((s) => (
                        <tr
                          key={s.id}
                          className="border-b transition-colors last:border-0 hover:bg-muted/50"
                        >
                          <td className="py-2.5 pr-4 font-medium">
                            <Link
                              href={`/servers/${s.id}`}
                              className="text-primary hover:underline"
                            >
                              {s.name}
                            </Link>
                          </td>
                          <td className="py-2.5 pr-4 font-mono text-xs">{s.ip_address}</td>
                          <td className="py-2.5 pr-4">
                            <Badge
                              variant={s.agent_status === "online" ? "success" : "destructive"}
                            >
                              {s.agent_status}
                            </Badge>
                          </td>
                          <td className="py-2 pr-4">
                            <ServiceChips services={s.services} />
                          </td>
                          <td className="py-2 pr-4 text-muted-foreground">
                            {since(s.last_seen_at)}
                          </td>
                        </tr>
                      ))}
                    </Fragment>
                  ))}
                  {visible.length === 0 && (
                    <tr>
                      <td colSpan={5} className="py-8 text-center text-muted-foreground">
                        {servers.length === 0
                          ? "No servers registered yet."
                          : "No servers in this region."}
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
