// Compose stack · Logs: every service's output, interleaved, replayed from the
// retention window and then tailed.
//
// The application log page owes its pane one thing the pane cannot know — why
// there is nothing in it — and a stack owes the same, with one more state to
// explain: `degraded` here means SOME of the services compose was asked for,
// so an empty-looking pane may simply belong to the half that never started.
//
// The phone layout is the application log page's, because it is the same
// screen for a different resource (canvas 14d): the pane bleeds to the
// gutters, loses its frame and runs to the bottom bar, with a crumb back to
// the stack and the replay window on the line above it.
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { getStreamComposeStackLogsUrl, useGetComposeStack } from "@/api/gen/compose-stacks/compose-stacks";
import { Eyebrow } from "@/components/eyebrow";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { ReplayWindowChips, ReplayWindowMenu } from "@/components/replay-window";
import { useFillToBottom } from "@/lib/fill-to-bottom";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/logs")({
  component: ComposeLogsTab,
});

/** Whether the stack can still write. The stream stays open either way —
 *  cypherd parks on the request rather than closing it — so this is what
 *  decides whether the pane promises more output. */
function canEmit(status: string | undefined): boolean {
  return status === "running" || status === "deploying" || status === "degraded";
}

function tailNote(status: string | undefined, detail: string | undefined): string | null {
  switch (status) {
    case "running":
    case "deploying":
      return null;
    case "degraded":
      return "Some of the services are up and some are not — the ones that are missing are usually the ones that explain themselves here.";
    case "stopped":
      return "Nothing is running — these are the lines the services wrote before they stopped. The tail resumes when the stack is deployed again.";
    case "error":
      return detail || "This stack is in error. The last lines below are usually the reason.";
    default:
      return "The agent hasn’t reported this stack’s state recently, so what’s below may be all there is until it does.";
  }
}

function ComposeLogsTab() {
  const { projectId, stackId } = Route.useParams();
  const stack = useGetComposeStack(stackId);
  const fill = useFillToBottom();
  const [since, setSince] = useState<string>("");

  return (
    <PageState query={stack} skeletonRows={1}>
      {(s) => {
        const note = tailNote(s.status, s.status_detail);
        return (
          <div className="space-y-2.5">
            {/* The eyebrow and the chip row are the desktop's: a phone shows
                the pane first (14d), and carries the window on the pane. */}
            <div className="hidden flex-wrap items-center justify-between gap-3 sm:flex">
              <Eyebrow>Logs — every service, interleaved</Eyebrow>
              <ReplayWindowChips value={since} onChange={setSince} />
            </div>
            {note && <p className="max-w-2xl text-[12.5px] leading-[1.5] text-text-mid">{note}</p>}
            {/* Ink to the gutters and down to the bar (14d). The negative
                margins undo the layout's PageBody padding on a phone only;
                with no note above it the pane also takes back the body's top
                padding so it hangs straight off the tab strip. */}
            <div
              ref={fill}
              className={cn("flex flex-col max-sm:-mx-4 max-sm:-mb-6 max-sm:bg-pane", !note && "max-sm:-mt-6")}
            >
              <div className="flex items-center justify-between gap-3 px-4 pb-1.5 pt-3.5 sm:hidden">
                <Link
                  to="/projects/$projectId/compose/$stackId"
                  params={{ projectId, stackId }}
                  aria-label={`Back to ${s.name}`}
                  className="eyebrow min-w-0 truncate text-pane-faint hover:text-pane-text"
                >
                  ← {s.name} / logs
                </Link>
                <ReplayWindowMenu value={since} onChange={setSince} />
              </div>
              <LogViewer
                // The key restarts the stream when the window changes: the
                // replay is chosen at connect time, so a new `since` is a new
                // stream rather than a filter over the one already open.
                key={since}
                url={getStreamComposeStackLogsUrl(stackId, since ? { since } : undefined)}
                live={canEmit(s.status)}
                // On a phone the wrapper's measured height is the size and the
                // pane's own frame goes — the screen is the frame.
                className="min-h-0 flex-1 max-sm:rounded-none max-sm:border-x-0 max-sm:border-b-0 sm:h-[min(60vh,560px)]"
              />
            </div>
          </div>
        );
      }}
    </PageState>
  );
}
