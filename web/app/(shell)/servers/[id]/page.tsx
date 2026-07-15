"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ArrowLeft,
  ChevronDown,
  HardDrive,
  MemoryStick,
  Play,
  Power,
  RotateCw,
  Server,
  Trash2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/page-header";
import { cn } from "@/lib/utils";
import {
  ApiError,
  controlService,
  deleteServer,
  getServer,
  managePHPRuntime,
  phpVersions,
  type ServiceAction,
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

const stateTone: Record<string, "success" | "warning" | "destructive" | "secondary"> = {
  active: "success",
  activating: "warning",
  inactive: "secondary",
  failed: "destructive",
  unknown: "secondary",
};

const actions: { action: ServiceAction; label: string; icon: typeof Play }[] = [
  { action: "start", label: "Start", icon: Play },
  { action: "restart", label: "Restart", icon: RotateCw },
  { action: "reload", label: "Reload", icon: RotateCw },
  { action: "stop", label: "Stop", icon: Power },
];

function ServiceRow({
  serverId,
  name,
  state,
}: {
  serverId: string;
  name: string;
  state: string;
}) {
  const qc = useQueryClient();
  const [note, setNote] = useState<string | null>(null);
  const control = useMutation({
    mutationFn: (action: ServiceAction) => controlService(serverId, name, action),
    onSuccess: (_d, action) => {
      setNote(`${action} dispatched`);
      setTimeout(() => qc.invalidateQueries({ queryKey: ["server", serverId] }), 1500);
    },
    onError: (e) => setNote(e instanceof ApiError ? e.message : "failed"),
  });

  return (
    <div className="flex items-center justify-between rounded-lg border border-border px-3 py-2.5">
      <div className="flex items-center gap-2.5">
        <span
          className={cn(
            "h-2 w-2 rounded-full",
            state === "active"
              ? "bg-success"
              : state === "failed"
                ? "bg-destructive"
                : state === "activating"
                  ? "bg-warning"
                  : "bg-muted-foreground",
          )}
        />
        <span className="font-medium">{name}</span>
        <Badge variant={stateTone[state] ?? "secondary"}>{state}</Badge>
      </div>
      <div className="flex items-center gap-2">
        {note && <span className="text-xs text-muted-foreground">{note}</span>}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button variant="outline" size="sm" disabled={control.isPending}>
                Manage <ChevronDown className="h-3.5 w-3.5" />
              </Button>
            }
          />
          <DropdownMenuContent align="end">
            {actions.map(({ action, label, icon: Icon }) => (
              <DropdownMenuItem
                key={action}
                variant={action === "stop" ? "destructive" : "default"}
                onClick={() => control.mutate(action)}
              >
                <Icon className="h-4 w-4" />
                {label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}

function PHPRuntimes({ serverId }: { serverId: string }) {
  const { data: versions } = useQuery({ queryKey: ["php-versions"], queryFn: phpVersions });
  const [note, setNote] = useState<string | null>(null);
  const manage = useMutation({
    mutationFn: ({ version, action }: { version: string; action: "install" | "uninstall" }) =>
      managePHPRuntime(serverId, version, action),
    onSuccess: (_d, v) => setNote(`${v.action} ${v.version} dispatched`),
    onError: (e) => setNote(e instanceof ApiError ? e.message : "failed"),
  });

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle className="text-base">PHP runtimes</CardTitle>
        <CardDescription>
          Install or remove PHP-FPM branches via the distro package manager.
          {note && <span className="ml-2 text-foreground">· {note}</span>}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {(versions ?? []).map((v) => (
          <div
            key={v}
            className="flex items-center justify-between rounded-lg border border-border px-3 py-2.5"
          >
            <span className="font-medium">PHP {v}</span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={manage.isPending}
                onClick={() => manage.mutate({ version: v, action: "install" })}
              >
                Install
              </Button>
              <Button
                variant="ghost"
                size="sm"
                disabled={manage.isPending}
                onClick={() => manage.mutate({ version: v, action: "uninstall" })}
              >
                Uninstall
              </Button>
            </div>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function StatTile({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof Activity;
  label: string;
  value: string;
}) {
  return (
    <Card>
      <CardContent className="flex items-center gap-3">
        <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary/12 text-primary">
          <Icon className="h-5 w-5" />
        </span>
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {label}
          </p>
          <p className="text-lg font-semibold tabular-nums">{value}</p>
        </div>
      </CardContent>
    </Card>
  );
}

export default function ServerDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;
  const router = useRouter();
  const [removeOpen, setRemoveOpen] = useState(false);
  const [removeErr, setRemoveErr] = useState<string | null>(null);

  const { data: s, isLoading, error } = useQuery({
    queryKey: ["server", id],
    queryFn: () => getServer(id),
    refetchInterval: 15_000,
  });

  const remove = useMutation({
    mutationFn: () => deleteServer(id),
    onSuccess: () => router.replace("/servers"),
    onError: (e) =>
      setRemoveErr(e instanceof ApiError ? e.message : "Could not remove server"),
  });

  if (isLoading) return <Skeleton className="h-64 w-full rounded-xl" />;
  if (error || !s)
    return (
      <Card className="ring-destructive/40">
        <CardHeader>
          <CardTitle className="text-base">Server not found</CardTitle>
          <CardDescription>
            {error instanceof Error ? error.message : "It may have been removed."}
          </CardDescription>
        </CardHeader>
      </Card>
    );

  const online = s.agent_status === "online";
  return (
    <div>
      <Link
        href="/servers"
        className="mb-4 inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Servers
      </Link>

      <PageHeader title={s.name ?? "Server"} description={s.ip_address ?? ""}>
        <Badge variant={online ? "success" : "destructive"} className="gap-1.5">
          <span
            className={cn(
              "h-1.5 w-1.5 rounded-full",
              online ? "bg-success animate-pulse" : "bg-destructive",
            )}
          />
          {s.agent_status}
        </Badge>
        <AlertDialog open={removeOpen} onOpenChange={setRemoveOpen}>
          <Button variant="destructive" size="sm" onClick={() => setRemoveOpen(true)}>
            <Trash2 className="h-4 w-4" /> Remove
          </Button>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Remove {s.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                De-registers this node from the panel. Refused if it still hosts
                accounts. A running agent would re-register on its next heartbeat.
              </AlertDialogDescription>
            </AlertDialogHeader>
            {removeErr && <p className="text-sm text-destructive">{removeErr}</p>}
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={remove.isPending}
                onClick={(e) => {
                  e.preventDefault();
                  remove.mutate();
                }}
              >
                {remove.isPending ? "Removing…" : "Remove server"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </PageHeader>

      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile icon={Activity} label="Load (1m)" value={s.load_1m?.toFixed(2) ?? "—"} />
        <StatTile
          icon={MemoryStick}
          label="Memory"
          value={`${formatBytes(s.memory_used_bytes)} / ${formatBytes(s.memory_total_bytes)}`}
        />
        <StatTile
          icon={HardDrive}
          label="Disk"
          value={`${formatBytes(s.disk_used_bytes)} / ${formatBytes(s.disk_total_bytes)}`}
        />
        <StatTile icon={Server} label="Hostname" value={s.hostname ?? "—"} />
      </div>

      <Card className="mb-6">
        <CardHeader>
          <CardTitle className="text-base">Managed services</CardTitle>
          <CardDescription>
            Start, stop, restart or reload services via the agent. Only managed
            units are controllable.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          {(s.services ?? []).length === 0 ? (
            <p className="py-4 text-center text-sm text-muted-foreground">
              No managed services reported (agent may lack systemd, or none installed).
            </p>
          ) : (
            (s.services ?? []).map((svc) => (
              <ServiceRow
                key={svc.name}
                serverId={id}
                name={svc.name!}
                state={svc.state ?? "unknown"}
              />
            ))
          )}
        </CardContent>
      </Card>

      <PHPRuntimes serverId={id} />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Add another server</CardTitle>
          <CardDescription>
            Agents self-register over mTLS — no manual entry here. Install
            CypherAgent on the new host with its issued client certificate and it
            appears automatically.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <pre className="overflow-x-auto rounded-lg bg-muted p-3 text-xs">
            <code>{`# On the control plane, issue an agent certificate:
cypher-core pki issue-agent --name <new-host>

# On the new server, install and start the agent pointing at this core.
# It registers over mTLS and shows up in Servers within ~30s.`}</code>
          </pre>
        </CardContent>
      </Card>
    </div>
  );
}
