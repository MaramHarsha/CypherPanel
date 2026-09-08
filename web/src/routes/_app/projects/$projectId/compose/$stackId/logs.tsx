// Compose stack · Logs: every service's output, interleaved, replayed from the
// retention window and then tailed.
//
// The application log page owes its pane one thing the pane cannot know — why
// there is nothing in it — and a stack owes the same, with one more state to
// explain: `degraded` here means SOME of the services compose was asked for,
// so an empty-looking pane may simply belong to the half that never started.
//
// `?since=` is the stream's own parameter (deployment-control.md). It is a
// picker rather than a free field because the three windows an operator
// actually reaches for are the last few minutes, the last hour, and everything
// retained — and anything the API cannot parse is a 400 rather than a silent
// fall back to the whole window, which is not a failure worth exposing a text
// box to earn.
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { getStreamComposeStackLogsUrl, useGetComposeStack } from "@/api/gen/compose-stacks/compose-stacks";
import { Eyebrow } from "@/components/eyebrow";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/logs")({
  component: ComposeLogsTab,
});

const WINDOWS = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "", label: "all" },
] as const;

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
  const { stackId } = Route.useParams();
  const stack = useGetComposeStack(stackId);
  const [since, setSince] = useState<string>("");

  return (
    <PageState query={stack} skeletonRows={1}>
      {(s) => {
        const note = tailNote(s.status, s.status_detail);
        return (
          <div className="space-y-2.5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <Eyebrow>Logs — every service, interleaved</Eyebrow>
              <div className="flex items-center gap-1" role="group" aria-label="Replay window">
                <span className="mono mr-1 text-[11px] text-text-faint">from</span>
                {WINDOWS.map((w) => (
                  <button
                    key={w.value}
                    type="button"
                    aria-pressed={since === w.value}
                    onClick={() => setSince(w.value)}
                    className={cn(
                      "mono rounded border px-2 py-[3px] text-[11px] transition-colors",
                      since === w.value
                        ? "border-border-strong bg-raised font-medium text-text"
                        : "border-border text-text-mid hover:text-text",
                    )}
                  >
                    {w.label}
                  </button>
                ))}
              </div>
            </div>
            {note && <p className="max-w-2xl text-[12.5px] leading-[1.5] text-text-mid">{note}</p>}
            <LogViewer
              // The key restarts the stream when the window changes: the
              // replay is chosen at connect time, so a new `since` is a new
              // stream rather than a filter over the one already open.
              key={since}
              url={getStreamComposeStackLogsUrl(stackId, since ? { since } : undefined)}
              live={canEmit(s.status)}
              className="h-[min(60vh,560px)]"
            />
          </div>
        );
      }}
    </PageState>
  );
}
