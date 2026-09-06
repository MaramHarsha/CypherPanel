// Application · Overview: what is live, where it runs, and how it's wired —
// outcomes first, machine detail in mono (web-ui-design.md §4). The three fact
// cards answer those three questions in that order; the two surfaces that own
// an action of their own — the domain check and the push-to-deploy webhook —
// sit under them rather than competing with the facts for attention.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { RotateCw } from "lucide-react";
import {
  getGetApplicationQueryKey,
  useCheckApplicationDomain,
  useGetApplication,
  useRestartApplication,
} from "@/api/gen/applications/applications";
import { getHandleGithubWebhookUrl } from "@/api/gen/deployments/deployments";
import { useGetServer } from "@/api/gen/servers/servers";
import type { Application } from "@/api/gen/model";
import { CopyField } from "@/components/copy-field";
import { DomainLink } from "@/components/domain-link";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { StatusBadge, StatusDot } from "@/components/status-badge";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { relativeTime, absoluteTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/")({
  component: OverviewTab,
});

function shortRev(id: string | null | undefined): string {
  if (!id) return "none yet";
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

/** What the rollout gate actually does. A `tcp` or `none` probe never looks at
 *  `health.path`, so printing the path for those states a check that is not
 *  running — the one thing an overview must not do. */
function healthSentence(a: Application): string {
  const every = `every ${a.health.interval_seconds}s`;
  switch (a.health.kind ?? "http") {
    case "none":
      return "none · container liveness only";
    case "tcp":
      return `tcp :${a.runtime.port} ${every}`;
    default:
      return `${a.health.path} ${every}`;
  }
}

/** How the checkout becomes an image, in one value. */
function buildSentence(a: Application): string {
  if (a.build.kind === "static") return "static site · nginx";
  return `${a.build.kind} · ${a.build.dockerfile_path}`;
}

function OverviewTab() {
  const { appId } = Route.useParams();
  const app = useGetApplication(appId);

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => {
        const webhookUrl = new URL(getHandleGithubWebhookUrl(a.webhook_id), window.location.origin).toString();
        // Desired and observed are allowed to differ (ADR-005), and the gap is
        // the only interesting thing about them: while it is open the agent is
        // still working, and while it is closed there is nothing to say. So the
        // second revision appears exactly when it disagrees with the first,
        // rather than sitting there permanently with a mark to decode.
        const rollingOut =
          a.desired_revision_id != null && a.observed_revision_id !== a.desired_revision_id;
        const fromImage = a.source.kind === "image";
        const ports = a.ports ?? [];
        const volumes = a.volumes ?? [];
        const limited = Boolean(a.runtime.cpu_limit || a.runtime.memory_limit_mb);

        return (
          <div className="space-y-3.5">
            <div className="grid gap-3.5 lg:grid-cols-2">
              <FactCard title="Status" actions={<RestartAction appId={appId} status={a.status} />}>
                <Fact label="State">
                  <StatusBadge status={a.status} />
                </Fact>
                <Fact label="Serving revision">{shortRev(a.observed_revision_id)}</Fact>
                {rollingOut && (
                  <Fact label="Rolling out">
                    <span className="text-status-deploying">{shortRev(a.desired_revision_id)}</span>
                  </Fact>
                )}
                <Fact label="Health check">{healthSentence(a)}</Fact>
                <Fact label="Created">
                  <span title={absoluteTime(a.created_at)}>{relativeTime(a.created_at)}</span>
                </Fact>
                {a.status_detail && (
                  // The quiet rule, not the row hairline: this is a note inside
                  // a card rather than another fact in the list.
                  <div className="border-t border-border-subtle pt-2.5 text-[12.5px] leading-relaxed text-text-dim">
                    {a.status_detail}
                  </div>
                )}
              </FactCard>

              <FactCard title="Source">
                {fromImage ? (
                  <>
                    <Fact label="Image">
                      <span title={a.source.image}>{a.source.image}</span>
                    </Fact>
                    <Fact label="Build">no build step · the agent pulls it</Fact>
                  </>
                ) : (
                  <>
                    <Fact label="Repository">
                      <span title={a.source.repo}>{a.source.repo}</span>
                    </Fact>
                    <Fact label="Branch">{a.source.branch}</Fact>
                    <Fact label="Build">{buildSentence(a)}</Fact>
                    <Fact label="Context">{a.build.context}</Fact>
                  </>
                )}
              </FactCard>

              <FactCard title="Where it runs">
                <Fact label="Server">
                  <ServerFact serverId={a.runtime.server_id} />
                </Fact>
                <Fact label="Container port">{a.runtime.port}</Fact>
                <Fact label="Domain">
                  <DomainLink applicationId={appId} domain={a.route.domain ?? ""} https={a.route.https} />
                </Fact>
                {a.route.domain && a.route.path_prefix && a.route.path_prefix !== "/" && (
                  <Fact label="Path">{a.route.path_prefix}</Fact>
                )}
                {/* What is requested, not what was issued: the certificate
                    lives in the serving node's acme.json and the plane never
                    sees it (routing-and-tls.md §7), so a green tick here would
                    be the panel asserting a thing it cannot check. The route
                    row in Settings carries the fuller condition (13c). */}
                <Fact label="TLS">
                  {!a.route.domain ? "—" : a.route.https ? (
                    "https · Let's Encrypt"
                  ) : (
                    <span className="text-status-degraded-text">off · plain http</span>
                  )}
                </Fact>
                {ports.length > 0 && (
                  <Fact label="Published ports">
                    {ports.map((p) => `${p.host_port}:${p.container_port}/${p.protocol}`).join(" · ")}
                  </Fact>
                )}
                {volumes.length > 0 && (
                  <Fact label="Volumes">{volumes.map((v) => `${v.name} → ${v.path}`).join(" · ")}</Fact>
                )}
                {limited && (
                  <Fact label="Limits">
                    {[
                      a.runtime.cpu_limit ? `${a.runtime.cpu_limit} cpu` : null,
                      a.runtime.memory_limit_mb ? `${a.runtime.memory_limit_mb} MB` : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </Fact>
                )}
              </FactCard>
            </div>

            {a.route.domain && <DomainCheck appId={appId} domain={a.route.domain} serverId={a.runtime.server_id} />}

            {/* An image-source app has no repository to hang a webhook on, and
                telling its operator to open GitHub → Settings → Webhooks is a
                dead end. It gets the sentence that is true for it instead:
                what a deploy actually does. */}
            {fromImage ? (
              <section className="rounded-lg border border-border bg-surface p-4.5">
                <h2 className="eyebrow">Deploying</h2>
                <p className="mt-3 max-w-2xl text-[12.5px] leading-relaxed text-text-dim">
                  No build runs — the agent pulls the image and rolls it out. A digest is fetched once and never
                  changes; a tag is re-fetched every deploy, so redeploying a tag that has moved runs the new
                  image.
                </p>
              </section>
            ) : (
              <section className="rounded-lg border border-border bg-surface p-4.5">
                <h2 className="eyebrow">Push to deploy</h2>
                <p className="mt-3 max-w-2xl text-[12.5px] leading-relaxed text-text-dim">
                  Add this webhook to the GitHub repository (Settings → Webhooks, content type JSON) and every push
                  to <span className="font-mono text-[12px] text-text">{a.source.branch}</span> deploys
                  automatically.
                </p>
                <CopyField value={webhookUrl} className="mt-2.5" />
              </section>
            )}
          </div>
        );
      }}
    </PageState>
  );
}

/** The host this app is placed on, by its name rather than its `srv_…` id —
 *  and as a link, because "which machine is this on?" is almost always
 *  followed by "how is that machine doing?". */
function ServerFact({ serverId }: { serverId: string }) {
  const server = useGetServer(serverId, { query: { retry: false } });
  // The id is the fallback, not the first draft: printing `srv_01J…` for a
  // frame and then swapping it for a name is a flicker the canvas never has
  // (canvas 10e).
  if (server.isPending) {
    return <span aria-hidden className="inline-block h-3 w-[90px] animate-pulse rounded bg-border-subtle" />;
  }
  return (
    <Link to="/servers/$serverId" params={{ serverId }} className="hover:text-accent">
      {server.data?.name ?? serverId}
    </Link>
  );
}

/** "Is this domain actually reaching my app?" — a domain can resolve perfectly
 *  and still be answered by another program on port 80, which nothing in the
 *  panel could see before. Checked on demand rather than polled: it makes an
 *  outbound request to the public internet. */
function DomainCheck({ appId, domain, serverId }: { appId: string; domain: string; serverId: string }) {
  const check = useCheckApplicationDomain(appId, { query: { enabled: false, retry: false } });
  // The server's public address is the thing an A record should point at, and
  // the plane already stores it — so a "no DNS" verdict can name the address
  // (canvas 13c) instead of leaving "your server's public IP" to be looked up.
  const server = useGetServer(serverId, { query: { retry: false } });
  const address = server.data?.public_address;
  const r = check.data;
  const ok = r?.verdict === "ok";
  const answered = Boolean(r) || check.isError;

  const tone = check.isError
    ? "border-status-error/40 bg-status-error/[0.05]"
    : ok
      ? "border-status-running/40 bg-status-running/[0.06]"
      : r
        ? "border-status-degraded/40 bg-status-degraded/[0.07]"
        : "border-border bg-surface";

  return (
    <section className={cn("rounded-lg border p-4.5", tone)}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="eyebrow">Domain</h2>
        <ActionButton
          size="sm"
          state={check.isFetching ? "busy" : "idle"}
          busyLabel="Checking…"
          onClick={() => void check.refetch()}
        >
          {answered ? "Check again" : "Check domain"}
        </ActionButton>
      </div>
      {check.isError ? (
        // The check itself failing is a third outcome, and saying nothing about
        // it leaves the last verdict on screen as though it were current
        // (ui-principles §1, §10).
        <p className="mt-3 text-[13px] leading-relaxed text-danger">
          The check couldn&rsquo;t run. The panel makes this request itself, so it needs outbound internet access
          from the CypherPanel host — try again once it has it.
        </p>
      ) : r ? (
        <div className="mt-3 space-y-1.5">
          <p className="flex items-start gap-2.5 text-[13px] leading-relaxed text-text">
            <StatusDot status={ok ? "running" : "degraded"} className="mt-[5px]" />
            {r.summary}
          </p>
          {r.remedy && (
            <p className="pl-[18px] text-[12.5px] leading-relaxed text-text-dim">
              {r.remedy}
              {r.verdict === "no_dns" && address && (
                <>
                  {" "}
                  Here, that is <span className="font-mono text-[12px] text-text">{address}</span>.
                </>
              )}
            </p>
          )}
          {(r.resolved_ips.length > 0 || r.http_status || r.served_by) && (
            <p className="pl-[18px] font-mono text-[11.5px] text-text-faint">
              {[
                r.resolved_ips.length > 0 ? `resolves to ${r.resolved_ips.join(", ")}` : null,
                r.http_status ? `answered ${r.http_status}` : null,
                r.served_by ? `served by ${r.served_by}` : null,
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
          )}
        </div>
      ) : (
        <p className="mt-3 text-[12.5px] leading-relaxed text-text-dim">
          Confirms that <span className="font-mono text-[12px] text-text">{domain}</span> resolves here and that
          this app — not something else on the server — is what answers.
        </p>
      )}
    </section>
  );
}

/**
 * Restart, expressed as desired state (deployment-control.md). A fresh restart
 * token rides on the AppSpec and is stamped on the container, so the drift the
 * existing recreate path already closes is what performs the restart: the
 * replacement starts alongside, health-gates, takes the route, and only then
 * does the old one drain. That is the whole reason the button can promise
 * zero downtime, and the reason it says so before the click rather than after.
 *
 * It is deliberately NOT a deploy: no revision, no build, no deployment row,
 * and the desired revision is unmoved — restarting a wedged container must not
 * silently ship an hour-old edit. Nothing to restart is a real state, so a
 * stopped application gets the reason rather than a button that would 409.
 */
function RestartAction({ appId, status }: { appId: string; status: string | undefined }) {
  const qc = useQueryClient();
  const restart = useRestartApplication({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetApplicationQueryKey(appId) });
        toastSuccess({
          title: "Restarting",
          detail: "The replacement starts alongside and takes the route once it is healthy.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not restart", e, { retry: () => restart.mutate(vars) }),
    },
  });
  const state = useMutationActionState(restart);
  const idle = status === "stopped" || status === "unknown";

  return (
    <ActionButton
      size="sm"
      variant="ghost"
      state={state}
      busyLabel="Restarting…"
      successLabel="Restarting"
      failedLabel="Retry"
      disabledReason={idle ? "Nothing is running to restart — deploy it first" : undefined}
      onClick={() => restart.mutate({ id: appId })}
      title="Replaces the container with a fresh one of the same revision"
    >
      <RotateCw className="h-3.5 w-3.5" aria-hidden /> Restart
    </ActionButton>
  );
}
