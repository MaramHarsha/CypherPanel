// Server detail: the facts a host is judged on, and the danger zone —
// revoking a server is a typed-name delete (ui-principles §2).
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useDeleteServer, useGetServer } from "@/api/gen/servers/servers";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Fact, FactCard } from "@/components/fact-card";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
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

  if (server.isError) {
    return <ResourceGone kind="server" error={server.error} backTo="/servers" backLabel="Back to servers" />;
  }

  const s = server.data;

  return (
    <>
      <PageHeader
        title={s?.name ?? "…"}
        badge={
          <span className="flex items-center gap-2">
            <StatusDot status={s ? (s.enrolled ? s.status : "unknown") : undefined} />
            <span className="font-mono text-[11px] font-medium uppercase tracking-wide text-text-mid">
              {s ? (s.enrolled ? s.status : "not joined") : "…"}
            </span>
          </span>
        }
      />
      <PageBody>
        <PageState query={server} isEmpty={() => false}>
          {(srv) => (
            <div className="max-w-2xl space-y-3.5">
              <FactCard title="Host">
                <Fact label="Hostname">{srv.hostname || "—"}</Fact>
                <Fact label="Agent version">{srv.agent_version || "—"}</Fact>
                <Fact label="Role">{srv.role ?? "all"}</Fact>
                <Fact label="Driver">{srv.driver}</Fact>
                <Fact label="Last heartbeat">
                  <span title={absoluteTime(srv.last_seen_at)}>
                    {srv.last_seen_at ? relativeTime(srv.last_seen_at) : "never"}
                  </span>
                </Fact>
                <Fact label="Joined">
                  <span title={absoluteTime(srv.enrolled_at)}>
                    {srv.enrolled_at ? relativeTime(srv.enrolled_at) : "not yet"}
                  </span>
                </Fact>
              </FactCard>

              {/* "Workloads placed here" needs a per-server list endpoint the
                  API doesn't expose yet — API-first (CLAUDE.md rule 4), so it
                  arrives with that route, not as a client-side scan. */}

              <section className="rounded-lg border border-danger/35 p-4.5">
                <h2 className="eyebrow text-danger">Danger zone</h2>
                <div className="mt-3.5 flex flex-wrap items-center justify-between gap-3">
                  <div className="min-w-0">
                    <p className="text-[13px] font-semibold text-text">Remove this server</p>
                    <p className="mt-0.5 text-[12.5px] leading-relaxed text-text-mid">
                      Revokes its agent's certificate immediately. Its workloads must be moved or deleted first.
                    </p>
                  </div>
                  <ConfirmDestructive
                    trigger={<Button variant="danger">Remove</Button>}
                    title={`Remove ${srv.name}?`}
                    blastRadius="Revokes the agent's certificate and disconnects it immediately. Apps still placed here block removal until they are moved or deleted."
                    confirmName={srv.name}
                    actionLabel="Remove server"
                    pending={del.isPending}
                    onConfirm={() => del.mutate({ id: srv.id })}
                  />
                </div>
              </section>
            </div>
          )}
        </PageState>
      </PageBody>
    </>
  );
}
