// Application · Overview: what is live, where, and how it's wired — outcomes
// first, machine detail in mono (web-ui-design.md §4).
import { createFileRoute } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { useGetApplication } from "@/api/gen/applications/applications";
import { getHandleGithubWebhookUrl } from "@/api/gen/deployments/deployments";
import { CopyField } from "@/components/copy-field";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { relativeTime, absoluteTime } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/")({
  component: OverviewTab,
});

function shortRev(id: string | null | undefined): string {
  if (!id) return "none yet";
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function OverviewTab() {
  const { appId } = Route.useParams();
  const app = useGetApplication(appId);

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => {
          const webhookUrl = new URL(getHandleGithubWebhookUrl(a.webhook_id), window.location.origin).toString();
          // Converged means the agent reports serving exactly what we asked
          // for. That equality is the whole desired-state story (ADR-005), so
          // it gets a mark rather than being left for the reader to diff.
          const converged =
            a.desired_revision_id != null && a.observed_revision_id === a.desired_revision_id;
          const proto = a.route.https ?? true ? "https" : "http";

          return (
            <div className="space-y-3.5">
              <div className="grid gap-3.5 lg:grid-cols-2">
                <FactCard title="Status">
                  <Fact label="State">
                    <span className="inline-flex items-center gap-2">
                      <StatusDot status={a.status} />
                      <span className="uppercase">{a.status ?? "unknown"}</span>
                    </span>
                  </Fact>
                  <Fact label="Serving revision">{shortRev(a.observed_revision_id)}</Fact>
                  <Fact label="Desired revision">
                    {shortRev(a.desired_revision_id)}
                    {converged && <span className="ml-1.5 text-status-running">=</span>}
                  </Fact>
                  <Fact label="Health check">
                    {a.health.path} every {a.health.interval_seconds}s
                  </Fact>
                  <Fact label="Created">
                    <span title={absoluteTime(a.created_at)}>{relativeTime(a.created_at)}</span>
                  </Fact>
                  {a.status_detail && (
                    <div className="border-t border-border pt-2.5 text-[12.5px] leading-relaxed text-text-mid">
                      {a.status_detail}
                    </div>
                  )}
                </FactCard>

                <FactCard title="Source">
                  <Fact label="Repository">{a.source.repo}</Fact>
                  <Fact label="Branch">{a.source.branch}</Fact>
                  <Fact label="Build">
                    {a.build.dockerfile_path} · ctx {a.build.context}
                  </Fact>
                  <Fact label="Domain">
                    {a.route.domain ? (
                      <a
                        href={`${proto}://${a.route.domain}`}
                        target="_blank"
                        rel="noreferrer"
                        className="inline-flex items-center gap-1 text-accent hover:underline"
                      >
                        {a.route.domain} <ExternalLink className="h-3 w-3" aria-hidden />
                      </a>
                    ) : (
                      "internal only"
                    )}
                  </Fact>
                  <Fact label="TLS">
                    {a.route.domain && (a.route.https ?? true) ? (
                      <span className="text-status-running">✓ auto-renews</span>
                    ) : (
                      "—"
                    )}
                  </Fact>
                </FactCard>
              </div>

              <FactCard title="Push to deploy">
                <p className="text-[12.5px] leading-relaxed text-text-mid">
                  Add this webhook to the GitHub repository (Settings → Webhooks, content type JSON) and every push
                  to <span className="font-mono text-[12px] text-text">{a.source.branch}</span> deploys
                  automatically.
                </p>
                <CopyField value={webhookUrl} className="mt-1" />
              </FactCard>
            </div>
          );
      }}
    </PageState>
  );
}
