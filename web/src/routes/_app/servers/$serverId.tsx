// Server detail: the facts a host is judged on, and the danger zone —
// revoking a server is a typed-name delete (ui-principles §2).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { getGetServerQueryKey, getListServersQueryKey, useDeleteServer, useGetServer } from "@/api/gen/servers/servers";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Fact, FactCard } from "@/components/fact-card";
import { ServerPublicAddress } from "@/components/server-public-address";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { PageState } from "@/components/page-state";
import { StatusBadge, StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { disk } from "@/lib/bytes";
import { useCrumbs } from "@/lib/crumbs";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/servers/$serverId")({ component: ServerDetail });

function ServerDetail() {
  const { serverId } = Route.useParams();
  const navigate = useNavigate();
  const server = useGetServer(serverId, { query: { refetchInterval: 5_000 } });

  useCrumbs([{ label: "servers", to: "/servers" }, { label: server.data?.name ?? serverId }]);

  const qc = useQueryClient();

  const del = useDeleteServer({
    mutation: {
      onSuccess: () => {
        // /servers renders from cache, and nothing streams server changes in —
        // so the host we just revoked would still be listed, heartbeat and all,
        // on the page we land on. Drop the fleet list and this server's own
        // entry before navigating, so what we arrive at is a fleet we can
        // vouch for.
        void qc.invalidateQueries({ queryKey: getListServersQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetServerQueryKey(serverId) });
        toastSuccess("Server removed — its agent certificate is revoked");
        void navigate({ to: "/servers" });
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the server", e, { retry: () => del.mutate(vars) }),
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
          // The state word takes the status colour so an errored host doesn't
          // read like a healthy one. A host that never joined has no observed
          // status to colour at all — it keeps the hollow marker, but says the
          // truer "not joined" rather than the system's generic "unknown".
          s &&
          (s.enrolled ? (
            <StatusBadge status={s.status} />
          ) : (
            <span className="flex items-center gap-2">
              <StatusDot status="unknown" decorative />
              <span className="font-mono text-[11px] font-medium uppercase tracking-wide text-status-unknown">
                not joined
              </span>
            </span>
          ))
        }
      />
      <PageBody>
        <PageState query={server} isEmpty={() => false}>
          {(srv) => (
            <div className="max-w-2xl space-y-3.5">
              <FactCard title="Host">
                <Fact label="Hostname">{srv.hostname || "—"}</Fact>
                <Fact label="Public address">
                  <ServerPublicAddress serverId={srv.id} value={srv.public_address ?? ""} />
                </Fact>
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

              <DiskCard
                total={srv.disk_total_bytes}
                free={srv.disk_free_bytes}
                low={srv.disk_low}
                enrolled={srv.enrolled}
              />

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
                  {/* Canvas 13af: the kind in the title, and the blast radius
                      as what DELETE /servers/{id} actually does (servers.go
                      Delete, openapi deleteServer) — nothing it cannot do.
                      The one survival is stated where it applies: the plane
                      refuses the remove while apps still run here (409), so
                      no workload is ever taken down by it. */}
                  <ConfirmDestructive
                    trigger={<Button variant="danger">Remove</Button>}
                    title={`Remove server ${srv.name}?`}
                    lead="Removing this server, immediately:"
                    blastRadius={[
                      "its agent's identity — the live connection is cut and the certificate is refused on any reconnect",
                      "its pending join tokens — an install still in progress can't complete",
                      "its place in the fleet (its apps survive: the remove is refused while any still runs here — move or delete them first)",
                    ]}
                    confirmName={srv.name}
                    actionLabel="Remove server"
                    pending={del.isPending}
                    pendingLabel="Removing…"
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

/**
 * Disk on the Docker data root — read from the daemon's own `/info`, not
 * assumed to be /var/lib/docker, because an operator who moved it is exactly
 * the operator who will not have moved the alert with it (disk-management.md
 * §4). The card exists so "how much room is left" is answerable without opening
 * a shell, which is the whole point of the feature.
 *
 * Reclaiming is deliberately NOT an action here. There is no "clean up now"
 * button because there is nothing for it to do that the agent is not already
 * doing: the plane ships a retain set and the agent converges to it, which
 * makes GC a reconciler rather than a thing a person triggers. A button would
 * promise a lever that does not exist.
 */
function DiskCard({
  total,
  free,
  low,
  enrolled,
}: {
  total: number | undefined;
  free: number | undefined;
  low: boolean | undefined;
  enrolled: boolean;
}) {
  const d = disk(total, free, low);
  return (
    <FactCard title="Disk">
      {d === null ? (
        // Zero is "not reported" and never "full" — an older agent, or a host
        // where the figure could not be read. Saying so is the honest state;
        // an empty bar would read as a disk with nothing on it.
        <Fact label="Docker data root">
          {enrolled ? "not reported — the agent predates disk reporting, or could not read it" : "not joined yet"}
        </Fact>
      ) : (
        <>
          <Fact label="Free">
            {d.freeLabel} of {d.totalLabel}
          </Fact>
          <Fact label="Used">{d.usedPercent}%</Fact>
          <div className="pt-0.5">
            <div
              className="h-[5px] overflow-hidden rounded-full bg-border-subtle"
              role="progressbar"
              aria-label="Disk used"
              aria-valuenow={d.usedPercent}
              aria-valuemin={0}
              aria-valuemax={100}
            >
              <div
                className={cn(
                  "h-full rounded-full transition-[width] duration-500",
                  d.low ? "bg-status-degraded" : "bg-primary",
                )}
                style={{ width: `${d.usedPercent}%` }}
              />
            </div>
            <p className="mt-2 text-[12px] leading-[1.5] text-text-faint">
              {d.low
                ? "Past the panel's threshold. The owners and admins were notified once, when it crossed — CypherPanel keeps only the retained revisions per application and never prunes anything it did not put there."
                : "CypherPanel converges this host to its retain set — the deployed revision plus the most recent others a rollback could name. It never prunes images it did not put here."}
            </p>
          </div>
        </>
      )}
    </FactCard>
  );
}
