// Application · Overview: what is live, where, and how it's wired — outcomes
// first, machine detail in mono (web-ui-design.md §4).
import { createFileRoute } from "@tanstack/react-router";
import { useGetApplication } from "@/api/gen/applications/applications";
import { getHandleGithubWebhookUrl } from "@/api/gen/deployments/deployments";
import { CopyField } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { relativeTime, absoluteTime } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/")({
  component: OverviewTab,
});

function OverviewTab() {
  const { appId } = Route.useParams();
  const app = useGetApplication(appId);

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => {
        const webhookUrl = new URL(getHandleGithubWebhookUrl(a.webhook_id), window.location.origin).toString();
        return (
          <div className="grid gap-6 lg:grid-cols-2">
            <section className="space-y-3">
              <Eyebrow>Status</Eyebrow>
              <dl className="space-y-2 rounded-md border border-border bg-surface p-4 text-[13px]">
                <Row label="State">
                  <span className="flex items-center gap-2">
                    <StatusBadge status={a.status} />
                    {a.status_detail && <span className="text-xs text-text-mid">{a.status_detail}</span>}
                  </span>
                </Row>
                <Row label="Serving revision">
                  <span className="mono">{a.observed_revision_id ?? "none yet"}</span>
                </Row>
                <Row label="Desired revision">
                  <span className="mono">{a.desired_revision_id ?? "none yet"}</span>
                </Row>
                <Row label="Created">
                  <span title={absoluteTime(a.created_at)}>{relativeTime(a.created_at)}</span>
                </Row>
              </dl>
            </section>

            <section className="space-y-3">
              <Eyebrow>Source</Eyebrow>
              <dl className="space-y-2 rounded-md border border-border bg-surface p-4 text-[13px]">
                <Row label="Repository">
                  <span className="mono">{a.source.repo}</span>
                </Row>
                <Row label="Branch">
                  <span className="mono">{a.source.branch}</span>
                </Row>
                <Row label="Build">
                  <span className="mono">
                    {a.build.dockerfile_path} · ctx {a.build.context}
                  </span>
                </Row>
                <Row label="Health check">
                  <span className="mono">
                    {a.health.path} every {a.health.interval_seconds}s
                  </span>
                </Row>
              </dl>
            </section>

            <section className="space-y-2 lg:col-span-2">
              <Eyebrow>Push to deploy</Eyebrow>
              <p className="text-[13px] text-text-mid">
                Add this webhook to the GitHub repository (Settings → Webhooks, content type JSON) and every push to{" "}
                <span className="mono text-text">{a.source.branch}</span> deploys automatically.
              </p>
              <CopyField value={webhookUrl} />
            </section>
          </div>
        );
      }}
    </PageState>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <dt className="shrink-0 text-text-faint">{label}</dt>
      <dd className="min-w-0 text-right text-text">{children}</dd>
    </div>
  );
}
