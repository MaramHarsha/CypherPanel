// Application · Logs: the running container's own output — replay from the
// retention window, then the live tail (web-ui-design.md §4). LogViewer owns
// the stream, the amber reconnect banner, the LIVE/PAUSED word and the follow
// toggle; what this page owes it is the one thing the pane cannot know — why
// there is nothing in it. A container that has never run and a container that
// stopped an hour ago both produce an empty pane, and "Waiting for output…" is
// a lie in both cases.
//
// On a phone (canvas 14d) the page IS the pane: the explanatory prose goes,
// the pane bleeds to the gutters and runs to the bottom bar, and the one line
// above it is a crumb back to the app. The masthead and tab strip above are
// the application layout's, so they stay — the pane fills what is left.
import { createFileRoute, Link } from "@tanstack/react-router";
import { useLayoutEffect, useState } from "react";
import { getStreamApplicationLogsUrl, useGetApplication } from "@/api/gen/applications/applications";
import type { Application } from "@/api/gen/model";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { LogViewer } from "@/components/log-viewer";
import { PageState } from "@/components/page-state";
import { StatusDot } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/logs")({
  component: LogsTab,
});

/** Tailwind's `sm` — below it the pane takes the phone layout. */
const PHONE = "(max-width: 639px)";

/**
 * Where the replay starts (`?since=`, deployment-control.md). A picker rather
 * than a free field: the windows an operator actually reaches for are the last
 * few minutes, the last hour, the last day and everything retained, and the API
 * answers 400 for anything it cannot parse rather than quietly falling back —
 * not a failure worth exposing a text box to earn.
 */
const WINDOWS = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "", label: "all" },
] as const;

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

/**
 * On a phone the pane runs from wherever it starts to the top of the bottom
 * tab bar (14d: the pane is `flex:1` of the screen). The masthead above it is
 * not this route's to remove, and its height is not a constant — the app name
 * wraps, the redeploy chip comes and goes, the tail note appears when the
 * container stops — so the height is measured rather than guessed: the
 * viewport, less the pane's own offset, less the reserve `<main>` keeps for
 * the bar (which already includes the notch inset). Re-measured when the
 * window or anything above the pane changes size. From `sm` up the style is
 * cleared and the pane's own classes size it.
 *
 * Returns a callback ref: the pane mounts only once the application has
 * loaded, so a ref object read in an effect on mount would still be empty.
 */
function useFillToBottom(): (el: HTMLDivElement | null) => void {
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  const [phone, setPhone] = useState(() => window.matchMedia(PHONE).matches);
  useLayoutEffect(() => {
    const mq = window.matchMedia(PHONE);
    const sync = () => setPhone(mq.matches);
    mq.addEventListener("change", sync);
    return () => mq.removeEventListener("change", sync);
  }, []);
  useLayoutEffect(() => {
    if (!el) return;
    if (!phone) {
      el.style.height = "";
      return;
    }
    const apply = () => {
      const top = el.getBoundingClientRect().top + window.scrollY;
      const main = document.getElementById("main");
      const reserve = main ? parseFloat(getComputedStyle(main).paddingBottom) || 0 : 0;
      el.style.height = `${Math.max(280, window.innerHeight - top - reserve)}px`;
    };
    apply();
    window.addEventListener("resize", apply);
    // Whatever sits above the pane shares its parent; a change in the
    // parent's size is a change in where the pane starts. Setting the same
    // height again does not resize anything, so this cannot feed itself.
    const above = el.parentElement;
    const ro = above ? new ResizeObserver(apply) : undefined;
    if (above) ro?.observe(above);
    return () => {
      window.removeEventListener("resize", apply);
      ro?.disconnect();
      el.style.height = "";
    };
  }, [el, phone]);
  return setEl;
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
                <SincePicker value={since} onChange={setSince} />
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
                  <Link
                    to="/projects/$projectId/applications/$appId"
                    params={{ projectId, appId }}
                    aria-label={`Back to ${a.name}`}
                    className="eyebrow block px-4 pb-1.5 pt-3.5 text-pane-faint hover:text-pane-text sm:hidden"
                  >
                    ← {a.name} / logs
                  </Link>
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
                </div>
              </>
            )}
          </div>
        );
      }}
    </PageState>
  );
}

/** The replay-window control. Mono, bordered, the same chip row the audit
 *  filters and the compose log pane use. */
function SincePicker({ value, onChange }: { value: string; onChange: (next: string) => void }) {
  return (
    <span className="flex items-center gap-1" role="group" aria-label="Replay window">
      <span className="mono mr-0.5 text-[11px] text-text-faint">from</span>
      {WINDOWS.map((w) => (
        <button
          key={w.value}
          type="button"
          aria-pressed={value === w.value}
          onClick={() => onChange(w.value)}
          className={cn(
            "mono rounded border px-2 py-[3px] text-[11px] transition-colors",
            value === w.value
              ? "border-border-strong bg-raised font-medium text-text"
              : "border-border text-text-mid hover:text-text",
          )}
        >
          {w.label}
        </button>
      ))}
    </span>
  );
}
