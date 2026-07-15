"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import { listAudit } from "@/lib/api";

function since(iso: string): string {
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 90) return `${Math.round(secs)}s ago`;
  if (secs < 5400) return `${Math.round(secs / 60)}m ago`;
  if (secs < 172800) return `${Math.round(secs / 3600)}h ago`;
  return new Date(iso).toLocaleDateString();
}

export default function AuditPage() {
  const [filter, setFilter] = useState("");
  const { data, isLoading } = useQuery({
    queryKey: ["audit", filter],
    queryFn: () => listAudit(filter, 200, 0),
    refetchInterval: 20_000,
  });

  return (
    <div>
      <PageHeader
        title="Audit log"
        description="Every privileged action — who did what, to which resource, from where."
      >
        <Input
          placeholder="Filter by action (e.g. account.)"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="w-64"
        />
      </PageHeader>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="space-y-2 p-4">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-9 w-full" />
              ))}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th className="px-4 py-2 font-medium">When</th>
                    <th className="px-4 py-2 font-medium">Actor</th>
                    <th className="px-4 py-2 font-medium">Action</th>
                    <th className="px-4 py-2 font-medium">Target</th>
                    <th className="px-4 py-2 font-medium">IP</th>
                  </tr>
                </thead>
                <tbody>
                  {(data ?? []).map((r) => (
                    <tr key={r.id} className="border-b transition-colors last:border-0 hover:bg-muted/50">
                      <td className="whitespace-nowrap px-4 py-2 text-muted-foreground" title={r.created_at}>
                        {since(r.created_at)}
                      </td>
                      <td className="px-4 py-2">
                        {r.actor_name || <span className="text-muted-foreground">system</span>}
                        {r.actor_role && (
                          <span className="ml-1 text-xs text-muted-foreground">({r.actor_role})</span>
                        )}
                      </td>
                      <td className="px-4 py-2">
                        <Badge variant="secondary" className="font-mono text-[11px]">{r.action}</Badge>
                      </td>
                      <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                        {r.target_type}
                        {r.target_id ? ` ${r.target_id.slice(0, 8)}` : ""}
                      </td>
                      <td className="px-4 py-2 font-mono text-xs text-muted-foreground">{r.ip_address}</td>
                    </tr>
                  ))}
                  {data?.length === 0 && (
                    <tr>
                      <td colSpan={5} className="px-4 py-10 text-center text-muted-foreground">
                        No audit entries{filter ? ` matching “${filter}”` : ""}.
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
