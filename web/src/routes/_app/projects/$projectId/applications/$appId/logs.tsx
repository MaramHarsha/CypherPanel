// Application · Logs: the running container's own output — replay from the
// retention window, then the live tail (web-ui-design.md §4). LogViewer owns
// the stream, the amber reconnect banner, the LIVE/PAUSED word and the follow
// toggle; what this page owes it is the one thing the pane cannot know — why
// there is nothing in it. A container that has never run and a container that
// stopped an hour ago both produce an empty pane, and "Waiting for output…" is
// a lie in both cases.
//
// On a phone (canvas 14d) the page IS the pane: the explanatory prose goes,
// the pane bleeds to the gutters and runs to the bottom bar, the line above it
// is a crumb back to the app beside the replay window, and the screen ends on
// the action a bad log leads to. The masthead and tab strip above are the
// application layout's, so they stay — the pane fills what is left.
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { getStreamApplicationLogsUrl, useGetApplication } from "@/api/gen/applications/applications";
import type { Application } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { ReplayWindowChips, ReplayWindowMenu } from "@/components/replay-window";
import { RestartApplicationButton } from "@/components/restart-application";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { useFillToBottom } from "@/lib/fill-to-bottom";
import { cn } from "@/lib/utils";

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
  const fill = useFillToBottom();
  const [since, setSince] = useState<string>("");

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => {
        const rev = shortRev(a.observed_revision_id);
        const note = tailNote(a);
        return (
          <div className="space-y-3">
            {/* The prose is the desktop's. A phone has no room to explain the
                pane before showing it, and 14d shows it first. */}
            <div className="hidden flex-wrap items-baseline justify-between gap-x-4 gap-y-1 sm:flex">
              <Eyebrow>Runtime log</Eyebrow>
              <span className="flex items-center gap-2">
                {rev && <span className="font-mono text-[11px] text-text-faint">rev {rev}</span>}
                <ReplayWindowChips value={since} onChange={setSince} />
              </span>
            </div>
            <p className="hidden max-w-2xl text-[12.5px] leading-relaxed text-text-dim sm:block">
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
                {/* Ink to the gutters and down to the bar (14d). The negative
                    margins undo the layout's PageBody padding on a phone only;
                    with no note above it the pane also takes back the body's
                    top padding so it hangs straight off the tab strip. */}
                <div
                  ref={fill}
                  className={cn(
                    "flex flex-col max-sm:-mx-4 max-sm:-mb-6 max-sm:bg-pane",
                    !note && "max-sm:-mt-6",
                  )}
                >
                  {/* 14d's first line: where you are, and how far back what
                      you are reading goes. The chip row above is the
                      desktop's — four chips do not fit beside a crumb. */}
                  <div className="flex items-center justify-between gap-3 px-4 pb-1.5 pt-3.5 sm:hidden">
                    <Link
                      to="/projects/$projectId/applications/$appId"
                      params={{ projectId, appId }}
                      aria-label={`Back to ${a.name}`}
                      className="eyebrow min-w-0 truncate text-pane-faint hover:text-pane-text"
                    >
                      ← {a.name} / logs
                    </Link>
                    <ReplayWindowMenu value={since} onChange={setSince} />
                  </div>
                  <LogViewer
                    // A new window is a new stream: the replay is chosen at
                    // connect time, not filtered over the one already open.
                    key={since}
                    url={getStreamApplicationLogsUrl(appId, since ? { since } : undefined)}
                    live={canEmit(a.status)}
                    // Tall enough to be a window rather than a slot, and clamped
                    // so a short laptop screen still leaves the pane usable
                    // instead of collapsing it to a couple of lines. On a phone
                    // the wrapper's measured height is the size, and the pane's
                    // own frame goes — the screen is the frame.
                    className="min-h-0 flex-1 max-sm:rounded-none max-sm:border-x-0 max-sm:border-b-0 sm:h-[55vh] sm:min-h-[280px] lg:h-[calc(100dvh-23rem)]"
                  />
                  {/* 14d ends the screen on the action the log leads to: a
                      container that is failing gets restarted from the screen
                      where its reason is being read, not two tabs away. The
                      canvas's second half, "Open terminal", waits for an exec
                      endpoint — a button that opens nothing is the dead end
                      14d exists to close (ui-principles §11).
                      The row is paper rather than ink: --pane-* do not invert
                      with the theme, so a filled pill drawn on the pane would
                      be ink on ink in light. */}
                  <div className="flex flex-none border-t border-border bg-bg px-4 py-3 sm:hidden">
                    <RestartApplicationButton
                      appId={appId}
                      status={a.status}
                      label="Restart app"
                      variant="primary"
                      size="md"
                      className="flex-1"
                    />
                  </div>
                </div>
              </>
            )}
          </div>
        );
      }}
    </PageState>
  );
}
