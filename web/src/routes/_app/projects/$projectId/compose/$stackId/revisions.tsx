// Compose stack · Revisions: the history, and the way back.
//
// A stack has no build and no distribute stage, so there is no deployment
// pipeline to page through — the revision list IS the history
// (compose-stacks.md), and rollback is re-pointing desired state at an older
// row rather than a separate machinery. That is why this tab exists where an
// application has "Deployments": same place in the strip, same job, one fewer
// abstraction.
//
// Each row can be expanded to its file, because "roll back to 3 days ago" is
// not a decision anyone should make from a timestamp alone — what changed is
// the whole question, and the file is the answer.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useState } from "react";
import {
  getGetComposeFileQueryKey,
  getGetComposeStackQueryKey,
  useGetComposeStack,
  useListComposeRevisions,
  useRollbackComposeStack,
} from "@/api/gen/compose-stacks/compose-stacks";
import type { ComposeRevision } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/revisions")({
  component: ComposeRevisionsTab,
});

function short(id: string): string {
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function ComposeRevisionsTab() {
  const { stackId } = Route.useParams();
  const stack = useGetComposeStack(stackId);
  const revisions = useListComposeRevisions(stackId);

  const desired = stack.data?.desired_revision_id ?? null;
  const observed = stack.data?.observed_revision_id ?? null;

  return (
    <div className="max-w-3xl space-y-3">
      <Eyebrow>Revisions</Eyebrow>
      <p className="max-w-xl text-[12.5px] leading-[1.5] text-text-mid">
        Every saved file is a revision. Deploying points desired state at the newest one; rolling back points it at an
        older one — same convergence, different target. Nothing is rebuilt either way, because a stack has nothing to
        build.
      </p>
      <PageState
        query={revisions}
        empty={
          <EmptyState
            title="No revisions yet"
            hint="The first one is minted when the stack is deployed — until then there is only the file you wrote."
          />
        }
      >
        {(list) => (
          <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((r) => (
              <RevisionRow
                key={r.id}
                stackId={stackId}
                rev={r}
                desired={r.id === desired}
                observed={r.id === observed}
              />
            ))}
          </ul>
        )}
      </PageState>
    </div>
  );
}

function RevisionRow({
  stackId,
  rev,
  desired,
  observed,
}: {
  stackId: string;
  rev: ComposeRevision;
  desired: boolean;
  observed: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <li>
      <div className="flex items-center gap-3 px-3.5 py-2.5">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
          className="flex min-w-0 flex-1 items-center gap-2.5 text-left"
        >
          <ChevronRight
            aria-hidden
            className={cn("h-3.5 w-3.5 shrink-0 text-text-faint transition-transform", open && "rotate-90")}
          />
          <span className="mono shrink-0 text-[12.5px] font-semibold text-text">{short(rev.id)}</span>
          <span className="mono min-w-0 truncate text-[11.5px] text-text-faint" title={absoluteTime(rev.created_at)}>
            {relativeTime(rev.created_at)}
          </span>
        </button>
        {/* Desired and observed are separate facts, and a converging stack is
            exactly the moment they differ — so both are labelled rather than
            collapsed into one "current" chip that would be wrong half the time. */}
        <span className="flex shrink-0 items-center gap-1.5">
          {desired && (
            <span className="mono rounded border border-border bg-raised px-2 py-[2px] text-[10.5px] text-text-mid">
              desired
            </span>
          )}
          {observed && (
            <span className="mono rounded border border-status-running/40 bg-status-running/[.08] px-2 py-[2px] text-[10.5px] text-status-running">
              running
            </span>
          )}
          {!desired && <RollbackDialog stackId={stackId} rev={rev} />}
        </span>
      </div>
      {open && (
        <pre className="overflow-x-auto border-t border-border bg-pane px-4 py-3 font-mono text-[11.5px] leading-[1.6] text-pane-text">
          {rev.compose_yaml}
        </pre>
      )}
    </li>
  );
}

function RollbackDialog({ stackId, rev }: { stackId: string; rev: ComposeRevision }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const rollback = useRollbackComposeStack({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetComposeStackQueryKey(stackId) });
        void qc.invalidateQueries({ queryKey: getGetComposeFileQueryKey(stackId) });
        toastSuccess({
          title: `Rolling back to ${short(rev.id)}`,
          detail: "The agent converges the stack to that file. Its output is on the Logs tab.",
        });
        setOpen(false);
      },
      onError: (e: unknown, vars) => toastFailed("Could not roll back", e, { retry: () => rollback.mutate(vars) }),
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <button type="button" className="shrink-0 text-[12px] font-medium text-text-mid hover:underline">
          Roll back
        </button>
      </DialogTrigger>
      <DialogContent
        title={`Roll back to ${short(rev.id)}?`}
        // What rollback does NOT do is the part worth stating: it re-points
        // desired state at an older file, so a service deleted from the newer
        // file comes back and one added to it is removed — and volumes are
        // never touched, which is the same rule databases follow.
        description="Desired state points back at this file and the agent converges to it — services it names come back, services only the newer file named are removed. Volumes are left alone, so data survives either way."
      >
        <pre className="mb-4 max-h-56 overflow-auto rounded-md border border-pane-border bg-pane px-3.5 py-3 font-mono text-[11.5px] leading-[1.6] text-pane-text">
          {rev.compose_yaml}
        </pre>
        <div className="flex items-center justify-end gap-2.5">
          <DialogClose asChild>
            <Button variant="ghost" size="lg">
              Cancel
            </Button>
          </DialogClose>
          <ActionButton
            variant="primary"
            size="lg"
            state={rollback.isPending ? "busy" : "idle"}
            busyLabel="Rolling back…"
            onClick={() => rollback.mutate({ id: stackId, data: { revision_id: rev.id } })}
          >
            Roll back
          </ActionButton>
        </div>
      </DialogContent>
    </Dialog>
  );
}
