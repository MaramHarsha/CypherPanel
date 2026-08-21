// RedeployPending — "a shared variable this app reads has changed since the
// environment it is running was frozen" (shared-variables.md §5, §8).
//
// It is a BADGE BESIDE THE STATUS, never a status word. The status vocabulary
// in ui-principles §5 is exactly six words and closed, and "needs a redeploy"
// is not an observed state — the container is running perfectly well, it is
// just running yesterday's value. Rendering it as a seventh status would make
// the panel lie about what the agent reported.
//
// Amber, because it is the same register as `degraded`: nothing is broken, but
// something needs attention. It is not a link — every place it appears already
// carries the "Deploy now" action, and a badge that navigates somewhere to find
// the real button is a dead end (ui-principles §11).
import { cn } from "@/lib/utils";

export function RedeployPending({ className, title }: { className?: string; title?: string }) {
  return (
    <span
      className={cn(
        "mono inline-flex shrink-0 items-center whitespace-nowrap rounded border border-status-degraded/40",
        "bg-status-degraded/10 px-1.5 py-px text-[10.5px] text-status-degraded",
        className,
      )}
      title={title ?? "A shared variable this app references changed after its last deploy. Deploy to apply it."}
    >
      redeploy to apply
    </span>
  );
}
