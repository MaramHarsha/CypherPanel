// Server detail: status, the workloads placed here, and the danger zone —
// revoking a server is a typed-name delete (ui-principles §2).
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useDeleteServer, useGetServer } from "@/api/gen/servers/servers";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { useCrumbs } from "@/lib/crumbs";
import { absoluteTime, relativeTime } from "@/lib/time";

export const Route = createFileRoute("/_app/servers/$serverId")({ component: ServerDetail });

function ServerDetail() {
  const { serverId } = Route.useParams();
  const navigate = useNavigate();
  const server = useGetServer(serverId, { query: { refetchInterval: 5_000 } });

  useCrumbs([{ label: "servers", to: "/servers" }, { label: server.data?.name ?? serverId }]);

  const del = useDeleteServer({
    mutation: {
      onSuccess: () => {
        toast.success("Server removed — its agent certificate is revoked");
        void navigate({ to: "/servers" });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not remove the server"),
    },
  });

  return (
    <PageState query={server} isEmpty={() => false}>
      {(s) => (
        <div className="space-y-6">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-3">
              <h1 className="text-base font-semibold text-text">{s.name}</h1>
              <StatusBadge status={s.enrolled ? s.status : "unknown"} />
            </div>
          </div>

          <dl className="grid max-w-xl gap-2 rounded-md border border-border bg-surface p-4 text-[13px] sm:grid-cols-2">
            <InfoRow label="Hostname" value={s.hostname || "—"} mono />
            <InfoRow label="Agent" value={s.agent_version || "—"} mono />
            <InfoRow label="Role" value={s.role ?? "all"} mono />
            <InfoRow label="Driver" value={s.driver} mono />
            <InfoRow label="Last seen" value={s.last_seen_at ? relativeTime(s.last_seen_at) : "never"} title={absoluteTime(s.last_seen_at)} />
            <InfoRow label="Joined" value={s.enrolled_at ? relativeTime(s.enrolled_at) : "not yet"} title={absoluteTime(s.enrolled_at)} />
          </dl>

          {/* "Workloads placed here" needs a per-server list endpoint the API
              doesn't expose yet — API-first (CLAUDE.md rule 4), so it arrives
              with that route, not as a client-side scan. */}

          <section className="space-y-2">
            <Eyebrow className="text-danger">Danger zone</Eyebrow>
            <div className="flex items-center justify-between gap-3 rounded-md border border-danger/30 p-4">
              <div>
                <p className="text-[13px] font-medium text-text">Remove this server</p>
                <p className="text-xs text-text-mid">Revokes its agent's certificate immediately. Its workloads must be moved or deleted first.</p>
              </div>
              <ConfirmDestructive
                trigger={<Button variant="danger">Remove</Button>}
                title={`Remove ${s.name}?`}
                blastRadius="Revokes the agent's certificate and disconnects it immediately. Apps still placed here block removal until they are moved or deleted."
                confirmName={s.name}
                actionLabel="Remove server"
                pending={del.isPending}
                onConfirm={() => del.mutate({ id: s.id })}
              />
            </div>
          </section>

          <p className="text-xs text-text-faint">
            Looking for another server? <Link to="/servers" className="text-accent hover:underline">Back to all servers</Link>
          </p>
        </div>
      )}
    </PageState>
  );
}

function InfoRow({ label, value, mono, title }: { label: string; value: string; mono?: boolean; title?: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-text-faint">{label}</dt>
      <dd className={mono ? "mono text-text" : "text-text"} title={title}>
        {value}
      </dd>
    </div>
  );
}
