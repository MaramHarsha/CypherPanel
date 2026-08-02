// Application · Previews: environments created from pull requests, each with
// its TTL. Read-mostly — they're created and destroyed by PR lifecycle events
// (preview-environments.md); the operator can tear one down early.
import { createFileRoute } from "@tanstack/react-router";
import { ExternalLink, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useDeletePreview, useListPreviews } from "@/api/gen/previews/previews";
import type { Preview } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { PageState } from "@/components/page-state";
import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { relativeTime, absoluteTime } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/previews")({
  component: PreviewsTab,
});

function PreviewsTab() {
  const { appId } = Route.useParams();
  const previews = useListPreviews(appId);

  return (
    <PageState
      query={previews}
      empty={
        <EmptyState
          title="No preview environments"
          hint="Open a pull request against this app's repository and CypherPanel spins up a throwaway copy at its own domain, then tears it down when the PR closes. Enable previews in Settings."
        />
      }
    >
      {(list) => (
        <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
          {list.map((p) => (
            <PreviewRow key={p.id} preview={p} />
          ))}
        </ul>
      )}
    </PageState>
  );
}

function PreviewRow({ preview: p }: { preview: Preview }) {
  const del = useDeletePreview({
    mutation: {
      onSuccess: () => toast.success(`Preview for PR #${p.pr_number} torn down`),
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
            expires {relativeTime(p.expires_at)}
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
          blastRadius="Removes this preview environment and its containers now. It will be recreated if the pull request is updated."
          actionLabel="Tear down"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: p.id })}
        />
      </span>
    </li>
  );
}
