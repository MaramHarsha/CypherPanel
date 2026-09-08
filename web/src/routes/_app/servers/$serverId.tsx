// Server detail: the facts a host is judged on, and the danger zone —
// revoking a server is a typed-name delete (ui-principles §2).
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  getGetServerQueryKey,
  getListServersQueryKey,
  useDeleteServer,
  useGetServer,
  useListServerWorkloads,
  useGetServerMetrics,
} from "@/api/gen/servers/servers";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Fact, FactCard } from "@/components/fact-card";
import { ServerPublicAddress } from "@/components/server-public-address";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { MetricsCard, useMetricsWindow } from "@/components/metrics-card";
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
  // What runs here — needed by the page AND by the remove confirm, which used
  // to claim "its apps survive" without being able to say what they were.
  const workloads = useListServerWorkloads(serverId).data?.workloads ?? [];

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
              <DegradedCard status={srv.status} health={srv.subsystem_health ?? []} />

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
                      workloads.length > 0
                        ? `nothing that runs here — the remove is REFUSED while ${workloadSummary(workloads)} remain on it; move or delete them first`
                        : "its place in the fleet (nothing runs here, so nothing is taken down)",
                    ]}
                    confirmName={srv.name}
                    actionLabel="Remove server"
                    pending={del.isPending}
                    pendingLabel="Removing…"
                    onConfirm={() => del.mutate({ id: srv.id })}
                  />
                </div>
              </section>

              {/* WHAT RUNS HERE. The plane assembles desired state from exactly
                  these three lists and no screen ever showed them, so a server
                  could be reported degraded — or offered for removal — without
                  the operator being able to see what was on it. */}
              <section className="rounded-lg border border-border bg-surface p-4.5">
                <h2 className="eyebrow">Workloads</h2>
                {workloads.length === 0 ? (
                  <p className="mt-3 text-[12.5px] leading-relaxed text-text-dim">
                    Nothing runs on this server yet. Applications, compose stacks and managed databases placed here
                    will be listed.
                  </p>
                ) : (
                  <ul className="mt-3 divide-y divide-border-subtle">
                    {workloads.map((w) => (
                      <li key={`${w.kind}-${w.id}`} className="flex flex-wrap items-baseline justify-between gap-2 py-2">
                        <span className="min-w-0">
                          <span className="text-[13px] text-text">{w.name}</span>{" "}
                          <span className="mono text-[11px] text-text-faint">
                            {w.kind.replace("_", " ")} · {w.project_name}
                          </span>
                        </span>
                        <span className="mono text-[11.5px] text-text-mid">{w.status}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </section>

              {/* The node's own load is the sum of what it runs. There is no
                  separate server sampler: a second source would be a second
                  answer to the same question, and the two would drift. */}
              <ServerMetrics serverId={srv.id} />
            </div>
          )}
        </PageState>
      </PageBody>
    </>
  );
}

function ServerMetrics({ serverId }: { serverId: string }) {
  const [win, setWin] = useMetricsWindow();
  const metrics = useGetServerMetrics(serverId, { window: win });
  return <MetricsCard query={metrics} title="Load" window={win} onWindow={setWin} />;
}

/**
 * WHY A DEGRADED SERVER IS AMBER, in the agent's own words.
 *
 * The agent has keyed its health by subsystem since ADR-010 — the Proxy and the
 * self-updater each report their own — but only the collapsed status word
 * crossed the wire, so this page showed amber and stopped there. The one thing
 * an operator needs at that moment is which part failed, and the architecture
 * (ADR-002, no SSH) gives them no other way to find out.
 *
 * It renders only while the server IS degraded: keeping the last finding on
 * screen beside a status that has since gone green would be a stale accusation.
 */
function DegradedCard({
  status,
  health,
}: {
  status?: string;
  health: { subsystem: string; message: string }[];
}) {
  if (status !== "degraded") return null;
  return (
    <section className="rounded-lg border border-status-degraded/40 bg-status-degraded/5 p-4.5">
      <h2 className="eyebrow text-status-degraded">Degraded</h2>
      {health.length === 0 ? (
        // An empty list beside "degraded" is an agent too old to say which part
        // failed — `repeated` has no presence on the wire, so silence and
        // health look the same and only the status word separates them. Say
        // that, rather than showing amber with no reason at all.
        <p className="mt-3 text-[12.5px] leading-relaxed text-text-mid">
          The agent reports itself degraded but does not say which part — it predates per-subsystem health. Update it,
          or read its log on the host.
        </p>
      ) : (
        <ul className="mt-3 space-y-2.5">
          {health.map((h) => (
            <li key={h.subsystem}>
              <p className="text-[13px] font-semibold text-text">{h.subsystem}</p>
              <p className="mono mt-0.5 break-words text-[11.5px] leading-relaxed text-text-mid">
                {h.message || "reported unhealthy, with no message"}
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
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

/** "2 applications and 1 database", for a refusal that names what blocks it. */
function workloadSummary(workloads: { kind: string }[]): string {
  const counts = new Map<string, number>();
  for (const w of workloads) counts.set(w.kind, (counts.get(w.kind) ?? 0) + 1);
  const parts = [...counts.entries()].map(([kind, n]) => {
    const noun = kind === "compose_stack" ? "compose stack" : kind;
    return `${n} ${noun}${n === 1 ? "" : "s"}`;
  });
  if (parts.length <= 1) return parts[0] ?? "nothing";
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}
