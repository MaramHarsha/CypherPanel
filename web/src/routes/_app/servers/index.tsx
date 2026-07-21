// Servers: the fleet, plus the join flow — the curl|sh moment, copy button
// front and center, "running within 60 s" progress (web-ui-design.md §4).
import { createFileRoute, Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { JoinInstructions, Server } from "@/api/gen/model";
import { useCreateServer, useListServers } from "@/api/gen/servers/servers";
import { CopyField } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";

export const Route = createFileRoute("/_app/servers/")({ component: ServersPage });

function ServersPage() {
  useCrumbs([{ label: "servers" }]);
  // Server heartbeats are not on the invalidate stream — poll honestly.
  const servers = useListServers({ query: { refetchInterval: 5_000 } });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <Eyebrow>Servers</Eyebrow>
        <JoinServerDialog />
      </div>
      <PageState
        query={servers}
        empty={
          <EmptyState
            title="No servers yet"
            hint="A server is any Linux host that runs your apps. Joining installs the CypherPanel agent with one copy-paste command — it dials home, no SSH keys, no open ports."
            action={<JoinServerDialog primary />}
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border rounded-md border border-border bg-surface">
            {list.map((s) => (
              <ServerRow key={s.id} server={s} />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function ServerRow({ server: s }: { server: Server }) {
  return (
    <li>
      <Link
        to="/servers/$serverId"
        params={{ serverId: s.id }}
        className="flex items-center justify-between gap-3 px-4 py-3 hover:bg-raised"
      >
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-sm font-medium text-text">{s.name}</span>
          <span className="mono truncate text-xs text-text-faint">
            {s.enrolled
              ? `${s.hostname || s.id} · agent ${s.agent_version}${s.role && s.role !== "all" ? ` · ${s.role}` : ""}`
              : "waiting for the agent to join"}
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-3">
          {s.last_seen_at && (
            <span className="mono hidden text-xs text-text-faint sm:inline">seen {relativeTime(s.last_seen_at)}</span>
          )}
          <StatusBadge status={s.enrolled ? s.status : "unknown"} />
        </span>
      </Link>
    </li>
  );
}

function JoinServerDialog({ primary }: { primary?: boolean }) {
  const [name, setName] = useState("");
  const [join, setJoin] = useState<{ serverId: string; instructions: JoinInstructions } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const create = useCreateServer({
    mutation: {
      onSuccess: (res) => setJoin({ serverId: res.server.id, instructions: res.join }),
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the server"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name } });
  };

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) {
          setJoin(null);
          setName("");
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size="sm">
          <Plus className="h-3.5 w-3.5" /> Join a server
        </Button>
      </DialogTrigger>
      {join === null ? (
        <DialogContent
          title="Join a server"
          description="Name the host first — the join command is generated for it, valid once, and expires shortly."
        >
          <form onSubmit={submit} className="space-y-4">
            <Field label="Name" error={error ?? undefined}>
              {(id) => <Input id={id} required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="hetzner-1" />}
            </Field>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button variant="ghost">Cancel</Button>
              </DialogClose>
              <Button type="submit" variant="primary" disabled={create.isPending || name.trim() === ""}>
                {create.isPending ? "Creating…" : "Get join command"}
              </Button>
            </div>
          </form>
        </DialogContent>
      ) : (
        <DialogContent title={`Join ${name}`} description="Run this on the server as root. The agent installs, dials home over mTLS, and appears here — usually within a minute.">
          <JoinProgress serverId={join.serverId} command={join.instructions.install_command} fingerprint={join.instructions.ca_fingerprint} />
        </DialogContent>
      )}
    </Dialog>
  );
}

function JoinProgress({ serverId, command, fingerprint }: { serverId: string; command: string; fingerprint: string }) {
  // Watch for the agent's first heartbeat while the dialog is open.
  const servers = useListServers({ query: { refetchInterval: 3_000 } });
  const joined = useMemo(() => {
    const s = (servers.data ?? []).find((x) => x.id === serverId);
    return s?.enrolled === true;
  }, [servers.data, serverId]);

  return (
    <div className="space-y-3">
      <CopyField value={command} />
      <p className="mono break-all text-[11px] text-text-faint">CA fingerprint {fingerprint}</p>
      {joined ? (
        <p className="flex items-center gap-2 text-[13px] text-status-running">
          <span className="h-2 w-2 rounded-full bg-status-running" aria-hidden /> Agent joined — this server is ready.
        </p>
      ) : (
        <p className="flex items-center gap-2 text-[13px] text-text-mid" role="status">
          <span className="h-2 w-2 rounded-full bg-status-deploying animate-status-pulse" aria-hidden />
          Waiting for the agent to dial home…
        </p>
      )}
    </div>
  );
}
