// MaintenanceBadge — "a holding page is serving in place of this application"
// (app-access-control.md §10).
//
// A badge beside the status, never a status word: the application is running
// perfectly well, its containers are passing their health gate, and only its
// route points somewhere else. The status vocabulary in ui-principles §5 is
// closed, and `redeploy_pending` already set the precedent for a fact that is
// not a status.
//
// The elapsed time is the whole point of it. This feature's failure mode is not
// a bug — it is maintenance LEFT ON, a Friday migration and a Monday of
// silence. An auto-expiring window was rejected (a page that lifts itself while
// the migration is still running publishes a half-migrated app to the
// internet), so the panel's job is to make the on-state impossible to miss
// rather than to guess when the operator is finished.
//
// Red rather than amber, unlike `redeploy_pending`: this is downtime on
// purpose, and something IS unreachable right now.
import { elapsedSince } from "@/lib/time";
import { cn } from "@/lib/utils";

export function MaintenanceBadge({ since, className }: { since?: string | null; className?: string }) {
  return (
    <span
      className={cn(
        "mono inline-flex shrink-0 items-center whitespace-nowrap rounded border border-danger/40",
        "bg-danger/10 px-1.5 py-px text-[10.5px] text-danger",
        className,
      )}
      title="A maintenance page is serving in place of this application. Visitors get a 503 until it is lifted."
    >
      maintenance{since ? ` · ${elapsedSince(since)}` : ""}
    </span>
  );
}
