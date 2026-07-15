"use client";

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
import { listServers } from "@/lib/api";

function since(iso?: string | null): string {
  if (!iso) return "never";
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 90) return `${Math.round(secs)}s ago`;
  if (secs < 5400) return `${Math.round(secs / 60)}m ago`;
  return `${Math.round(secs / 3600)}h ago`;
}

export default function ServersPage() {
  const { data, isLoading } = useQuery({
    queryKey: ["servers"],
    queryFn: listServers,
    refetchInterval: 15_000,
  });

  return (
    <div>
      <PageHeader
        title="Servers"
        description="Every node running CypherAgent, with registration and liveness state."
      />

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
                  {(data ?? []).map((s) => (
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
                  {data?.length === 0 && (
                    <tr>
                      <td colSpan={5} className="py-8 text-center text-muted-foreground">
                        No servers registered yet.
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
