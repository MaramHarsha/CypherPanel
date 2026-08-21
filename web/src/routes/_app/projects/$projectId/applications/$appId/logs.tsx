// Application · Logs: the running container's own output — replay from the
// retention window, then the live tail (web-ui-design.md §4). LogViewer owns
// the stream, the amber reconnect banner and the follow toggle; what this page
// owes it is the one thing the pane cannot know — why there is nothing in it.
// A container that has never run and a container that stopped an hour ago both
// produce an empty pane, and "Waiting for output…" is a lie in both cases.
import { createFileRoute, Link } from "@tanstack/react-router";
import { getStreamApplicationLogsUrl, useGetApplication } from "@/api/gen/applications/applications";
import type { Application } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/logs")({
  component: LogsTab,
});

function shortRev(id: string | null | undefined): string {
  if (!id) return "";
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

/** Whether the container can still write. The stream stays open either way —
 *  cypherd parks on the request rather than closing it — so this is what
 *  decides whether the pane promises more output (LogViewer's `live`). */
function canEmit(status: string | undefined): boolean {
  return status === "running" || status === "deploying" || status === "degraded";
}

/**
 * The line above the pane when the tail is not live. Each state gets the
 * sentence that is true for it, and where the log is the answer to the state —
 * a crash, a failing probe — it says so: the last lines before a container
 * died are the reason it died, and an operator who reads that once stops
 * hunting for a separate error screen (ui-principles §11).
 */
function tailNote(a: Application): string | null {
  switch (a.status) {
    case "running":
    case "deploying":
      return null;
    case "stopped":
      return "Not running — these are the lines it wrote before it stopped. The tail resumes when it starts again.";
    case "error":
      return "This app is in error. The last lines below are usually the reason.";
    case "degraded":
      return "Health checks are failing while the container is still up — the log usually says what it is answering with.";
    default:
      return "The agent hasn't reported this app's state recently, so what's below may be all there is until it does.";
  }
}

function LogsTab() {
  const { projectId, appId } = Route.useParams();
  const app = useGetApplication(appId);

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => {
        const rev = shortRev(a.observed_revision_id);
        const note = tailNote(a);
        return (
          <div className="space-y-3">
            <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
              <Eyebrow>Runtime log</Eyebrow>
              {rev && <span className="font-mono text-[11px] text-text-faint">rev {rev}</span>}
            </div>
            <p className="max-w-2xl text-[12.5px] leading-relaxed text-text-dim">
              What the container writes to stdout and stderr. Recent history replays first, then the tail continues
              live; older lines age out of the retention window. Build output lives on the Deployments tab.
            </p>

            {a.desired_revision_id == null ? (
              // Never deployed: there is no container, so there is no log —
              // and the next step is one tab away rather than nowhere.
              <EmptyState
                glyph="▤"
                title="Nothing has run yet"
                hint="This app hasn't been deployed, so no container has written anything. Deploy it and its output appears here, live."
                action={
                  <Link
                    to="/projects/$projectId/applications/$appId/deployments"
                    params={{ projectId, appId }}
                  >
                    <Button variant="secondary">See deployments</Button>
                  </Link>
                }
              />
            ) : (
              <>
                {note && (
                  <p className="flex items-start gap-2.5 text-[12.5px] leading-relaxed text-text-dim">
                    <StatusDot status={a.status} className="mt-[5px]" />
                    {note}
                  </p>
                )}
                <LogViewer
                  url={getStreamApplicationLogsUrl(appId)}
                  live={canEmit(a.status)}
                  // Tall enough to be a window rather than a slot, and clamped
                  // so a short laptop screen still leaves the pane usable
                  // instead of collapsing it to a couple of lines.
                  className="h-[55vh] min-h-[280px] lg:h-[calc(100dvh-23rem)]"
                />
              </>
            )}
          </div>
        );
      }}
    </PageState>
  );
}
