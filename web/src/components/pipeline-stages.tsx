// PipelineStages — the signature element (web-ui-design.md §2, §6): the deploy
// pipeline rendered as the sequence it truly is. The active stage carries the
// pulse; a terminal failure turns its stage red. This is where the one
// celebrated animation lives.
import { Check, X } from "lucide-react";
import { Fragment } from "react";
import { cn } from "@/lib/utils";

type DeployStatus = "queued" | "building" | "distributing" | "rolling_out" | "succeeded" | "failed" | string;

const STAGES = [
  { key: "building", label: "build" },
  { key: "distributing", label: "distribute" },
  { key: "rolling_out", label: "rollout" },
  { key: "succeeded", label: "serving" },
] as const;

const ORDER: Record<string, number> = {
  queued: -1,
  building: 0,
  distributing: 1,
  rolling_out: 2,
  succeeded: 3,
  failed: 99,
};

/** Which stage index was active when the deployment failed. */
function failedAt(detail: string | undefined): number {
  const d = detail ?? "";
  if (d.startsWith("build")) return 0;
  if (d.startsWith("distribut") || d.startsWith("relay") || d.startsWith("push")) return 1;
  return 2;
}

export function PipelineStages({
  status,
  detail,
  className,
  tone = "paper",
}: {
  status: DeployStatus;
  detail?: string;
  className?: string;
  /** `ink` is the live-deploy drawer, which is ink in both themes. */
  tone?: "paper" | "ink";
}) {
  const failed = status === "failed";
  const active = failed ? failedAt(detail) : (ORDER[status] ?? -1);
  const ink = tone === "ink";

  return (
    // In the drawer the rail spans the panel — the connectors stretch and the
    // row never wraps, because the whole point is one continuous line from
    // BUILD to SERVING (canvas 1c, and 14c on a 300px sheet). Inline in a
    // deployment row it stays a compact badge strip instead.
    <ol
      className={cn("flex items-center", ink ? "gap-y-0" : "flex-wrap gap-y-1", className)}
      aria-label="Deploy pipeline"
    >
      {STAGES.map((stage, i) => {
        const done = !failed && active > i;
        const current = !failed && active === i && status !== "succeeded";
        const failedHere = failed && i === active;
        const serving = status === "succeeded" && i === STAGES.length - 1;
        const reached = done || serving || (failed && i <= active);
        return (
          <Fragment key={stage.key}>
            {i > 0 && (
              <span
                aria-hidden
                className={cn(
                  "h-px",
                  ink ? "mx-[7px] flex-1" : "mx-2 w-4 sm:w-6",
                  ink
                    ? reached
                      ? "bg-pane-ok"
                      : "bg-pane-border"
                    : reached
                      ? "bg-border-strong"
                      : "bg-border",
                )}
              />
            )}
            <li
              className={cn(
                "inline-flex items-center gap-1.5 font-mono uppercase tracking-wider",
                ink ? "shrink-0 text-[10px]" : "text-[10.5px]",
                // The drawer is ink in both themes, so the rail takes the
                // terminal palette rather than the surface --status-* ramp:
                // the lifted dark greens would be a step off the log beneath.
                // A pending stage is #6f695e (1c), one step brighter than
                // --pane-faint: that darker grey belongs to the timestamp
                // column, and a 10px label wearing it falls under 3:1.
                ink
                  ? cn(
                      serving && "text-pane-ok",
                      done && "text-pane-ok",
                      current &&
                        "rounded border border-pane-info/40 bg-pane-info/12 px-1.5 py-0.5 text-pane-info",
                      failedHere && "text-pane-error",
                      !done && !current && !failedHere && !serving && "text-pane-dim",
                    )
                  : cn(
                      serving && "text-status-running",
                      done && "text-text-mid",
                      current && "text-status-deploying",
                      failedHere && "text-status-error",
                      !done && !current && !failedHere && !serving && "text-text-faint",
                    ),
              )}
              aria-current={current ? "step" : undefined}
            >
              {serving || done ? (
                <Check className="h-3 w-3" aria-hidden />
              ) : failedHere ? (
                <X className="h-3 w-3" aria-hidden />
              ) : (
                <span
                  aria-hidden
                  className={cn(
                    "h-1.5 w-1.5 rounded-full",
                    current
                      ? cn("animate-status-pulse", ink ? "bg-pane-info" : "bg-status-deploying")
                      : "border border-current",
                  )}
                />
              )}
              {stage.label}
            </li>
          </Fragment>
        );
      })}
    </ol>
  );
}
