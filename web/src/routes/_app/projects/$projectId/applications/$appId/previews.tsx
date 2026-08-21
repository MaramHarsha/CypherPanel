// Application · Previews: environments created from pull requests, each with
// its TTL. Read-mostly — they're created and destroyed by PR lifecycle events
// (preview-environments.md); the operator can tear one down early.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ExternalLink, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useGetApplication } from "@/api/gen/applications/applications";
import { getListPreviewsQueryKey, useDeletePreview, useListPreviews } from "@/api/gen/previews/previews";
import type { Preview } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { absoluteTime, timeUntil } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/previews")({
  component: PreviewsTab,
});

function PreviewsTab() {
  const { projectId, appId } = Route.useParams();
  const previews = useListPreviews(appId);
  const app = useGetApplication(appId);

  // An empty list means two different things and they need different answers:
  // the feature is off, or it is on and no PR is open. Telling someone who has
  // already enabled it to "enable it in Settings" is a dead end, and until the
  // spec carried preview_enabled the UI could not tell the two apart
  // (ui-principles §11).
  const enabled = app.data?.preview_enabled ?? false;

  return (
    <PageState
      query={previews}
      empty={
        enabled ? (
          <EmptyState
            glyph="⎇"
            title="No preview environments"
            hint="Open a pull request and one appears here with its own URL — torn down when the PR closes."
          />
        ) : (
          <EmptyState
            emphasis
            glyph="⎇"
            title="Previews are off for this app"
            hint="Turn them on and every pull request gets a live copy of the app at its own subdomain, torn down automatically when the PR closes."
            action={
              <Link to="/projects/$projectId/applications/$appId/settings" params={{ projectId, appId }}>
                <Button variant="primary">Turn on previews</Button>
              </Link>
            }
          />
        )
      }
    >
      {(list) => (
        <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
          {list.map((p) => (
            <PreviewRow key={p.id} appId={appId} preview={p} />
          ))}
        </ul>
      )}
    </PageState>
  );
}

function PreviewRow({ appId, preview: p }: { appId: string; preview: Preview }) {
  const qc = useQueryClient();
  const del = useDeletePreview({
    mutation: {
      onSuccess: () => {
        // Nothing else refreshes this list: the live stream carries only
        // application and database invalidations, so a torn-down preview would
        // sit here looking alive until the page was reloaded.
        void qc.invalidateQueries({ queryKey: getListPreviewsQueryKey(appId) });
        toast.success(`Preview for PR #${p.pr_number} torn down`);
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the preview"),
    },
  });

  return (
    <li className="flex items-center justify-between gap-3 px-4 py-3">
      <span className="flex min-w-0 flex-col">
        <span className="flex items-center gap-2">
          <span className="text-sm font-medium text-text">PR #{p.pr_number}</span>
          <span className="mono truncate text-xs text-text-faint">{p.pr_branch}</span>
        </span>
        <a
          href={`https://${p.domain}`}
          target="_blank"
          rel="noreferrer"
          className="mono inline-flex w-fit items-center gap-1 text-xs text-text-mid hover:text-accent"
        >
          {p.domain} <ExternalLink className="h-3 w-3" aria-hidden />
        </a>
      </span>
      <span className="flex shrink-0 items-center gap-3">
        {p.expires_at && (
          <span className="mono hidden text-xs text-text-faint sm:inline" title={absoluteTime(p.expires_at)}>
            expires {timeUntil(p.expires_at)}
          </span>
        )}
        <StatusBadge status={p.status} />
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" aria-label={`Tear down preview for PR ${p.pr_number}`}>
              <Trash2 className="h-3.5 w-3.5 text-danger" />
            </Button>
          }
          title={`Tear down the PR #${p.pr_number} preview?`}
          // One entry per consequence, not a paragraph (canvas 13af): the last
          // one is why this confirm is not "Delete forever" — a teardown is a
          // pause, and the operator should read that before they hesitate.
          // For the same reason there is no typed-name gate: ui-principles §2
          // reserves that for what cannot be undone, and the next push to the
          // PR rebuilds this. The red rule and the enumerated consequences are
          // the right weight for an action the copy calls reversible.
          lead="Tearing this preview down:"
          blastRadius={[
            "stops its containers and frees the server now",
            `${p.domain} stops resolving`,
            "the next push to the pull request builds it again",
          ]}
          actionLabel="Tear down"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: p.id })}
        />
      </span>
    </li>
  );
}
