// Servers: the fleet as a board, plus the join flow — the curl|sh moment with
// the copy button front and centre (web-ui-design.md §4). The join card is
// always the last cell, so "add a server" is a place on the page rather than a
// button you have to find.
import { createFileRoute, Link } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { JoinInstructions, Server } from "@/api/gen/model";
import { useCreateServer, useListServers } from "@/api/gen/servers/servers";
import { CopyButton } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { HeaderStat, PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime, absoluteTime } from "@/lib/time";

export const Route = createFileRoute("/_app/servers/")({ component: ServersPage });

function ServersPage() {
  useCrumbs([{ label: "servers" }]);
  // Server heartbeats are not on the invalidate stream — poll honestly.
  const servers = useListServers({ query: { refetchInterval: 5_000 } });
  const list = servers.data ?? [];
  const online = list.filter((s) => s.enrolled && s.status === "running").length;

  return (
    <>
      <PageHeader
        title="Servers"
        actions={
          <>
            {list.length > 0 && <HeaderStat value={`${online}/${list.length}`} label="online" />}
            <JoinServerDialog />
          </>
        }
      />
      <PageBody>
        <PageState
          query={servers}
          empty={
            <EmptyState
              emphasis
              title="Join your first server"
              hint="A server is any Linux host that runs your apps. Joining installs the CypherPanel agent with one copy-paste command — it dials home over mTLS, so there are no SSH keys to store and no ports to open."
              action={<JoinServerDialog primary />}
            />
          }
        >
          {(rows) => (
            <ul className="grid gap-3.5 lg:grid-cols-2">
              {rows.map((s) => (
                <ServerCard key={s.id} server={s} />
              ))}
              <li>
                <JoinCard />
              </li>
            </ul>
          )}
        </PageState>
      </PageBody>
    </>
  );
}

function ServerCard({ server: s }: { server: Server }) {
  const facts = [s.hostname || s.id, s.driver, `agent ${s.agent_version}`, s.role && s.role !== "all" ? s.role : null]
    .filter(Boolean)
    .join(" · ");

  return (
    <li>
      <Link
        to="/servers/$serverId"
        params={{ serverId: s.id }}
        className="flex h-full flex-col rounded-lg border border-border bg-surface p-5 transition-colors hover:border-border-strong"
      >
        <span className="flex items-center gap-2.5">
          <StatusDot status={s.enrolled ? s.status : "unknown"} />
          <span className="min-w-0 truncate text-[17px] font-semibold">{s.name}</span>
          <span className="ml-auto shrink-0 font-mono text-[11px] text-text-faint">
            {s.last_seen_at ? (
              <span title={absoluteTime(s.last_seen_at)}>heartbeat {relativeTime(s.last_seen_at)}</span>
            ) : (
              "never seen"
            )}
          </span>
        </span>
        <span className="mt-1.5 block truncate font-mono text-[11.5px] text-text-faint">
          {s.enrolled ? facts : "waiting for the agent to join"}
        </span>
      </Link>
    </li>
  );
}

/** The dashed cell that explains the join model and starts the flow. */
function JoinCard() {
  return (
    <div className="flex h-full flex-col rounded-lg border-[1.5px] border-dashed border-border-strong/50 p-5">
      <p className="text-[15px] font-semibold">Join a new server</p>
      <p className="mt-1 text-[12.5px] leading-relaxed text-text-mid">
        Run one command on any Linux box. The agent dials home over mTLS — CypherPanel never stores SSH keys and
        never opens a port on your server.
      </p>
      <div className="mt-auto pt-4">
        <JoinServerDialog primary />
      </div>
    </div>
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
        <Button variant="primary" size={primary ? "lg" : "md"}>
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
              {(id) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="hetzner-1"
                />
              )}
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
        <DialogContent
          title={`Join ${name}`}
          description="Run this on the server as root. The agent installs, dials home over mTLS, and appears here — usually within a minute."
        >
          <JoinProgress
            serverId={join.serverId}
            command={join.instructions.install_command}
            fingerprint={join.instructions.ca_fingerprint}
          />
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
      {/* The command sits on ink: it is a terminal line, and it should look
          like one wherever it is read. */}
      <div className="flex items-center gap-2.5 rounded-md border border-pane-border bg-pane px-3.5 py-3">
        <span className="font-mono text-[12px] text-status-running">$</span>
        <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-pane-text" title={command}>
          {command}
        </span>
        <CopyButton value={command} label="Copy join command" />
      </div>
      <p className="break-all font-mono text-[11px] text-text-faint">CA fingerprint {fingerprint}</p>
      {joined ? (
        <p className="flex items-center gap-2 text-[13px] text-status-running">
          <span className="h-2 w-2 rounded-full bg-status-running" aria-hidden /> Agent joined — this server is ready.
        </p>
      ) : (
        <p className="flex items-center gap-2 text-[13px] text-text-mid" role="status">
          <span className="h-2 w-2 animate-status-pulse rounded-full bg-status-deploying" aria-hidden />
          Waiting for the agent to dial home…
        </p>
      )}
    </div>
  );
}
