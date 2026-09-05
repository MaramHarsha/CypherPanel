// PipelineStages — the signature element (web-ui-design.md §2, §6): the deploy
// pipeline rendered as the sequence it truly is. The active stage carries the
// pulse; a terminal failure turns its stage red. This is where the one
// celebrated animation lives.
import { Check, X } from "lucide-react";
import { Fragment } from "react";
import { cn } from "@/lib/utils";

type DeployStatus = "queued" | "building" | "distributing" | "rolling_out" | "succeeded" | "failed" | string;

// `short` is the 300px sheet's label (canvas 14c: BUILD · DIST · ROLLOUT ·
// SERVE) — four full words plus their connectors do not fit in the ~264px a
// phone leaves, and a rail that wraps is no longer a rail.
const STAGES = [
  { key: "building", label: "build", short: "build", word: "building" },
  { key: "distributing", label: "distribute", short: "dist", word: "distributing" },
  { key: "rolling_out", label: "rollout", short: "rollout", word: "rolling out" },
  { key: "succeeded", label: "serving", short: "serve", word: "serving" },
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

/** The stage a failed deployment died in, as a word — "failed at build" (14c). */
export function failedStage(detail: string | undefined): string {
  return STAGES[failedAt(detail)]!.label;
}

/** The stage in progress, in the present tense — "rolling out". Empty once terminal. */
export function stageWord(status: DeployStatus): string {
  if (status === "succeeded" || status === "failed") return "";
  const i = ORDER[status] ?? -1;
  return i < 0 ? "queued" : STAGES[i]!.word;
}

/**
 * What a screen reader hears when the rail moves (canvas 14g: "pipeline stage
 * changes announce via aria-live=polite"). One short sentence per transition;
 * the polite region only speaks when its text changes, so a re-render that
 * lands on the same stage says nothing.
 */
function announcement(status: DeployStatus, detail: string | undefined): string {
  if (status === "failed") return `Deploy failed at ${failedStage(detail)}`;
  if (status === "succeeded") return "Deploy complete — serving";
  const word = stageWord(status);
  return word ? `Deploy ${word}` : "";
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
    <>
      {/* In the drawer the rail spans the panel — the connectors stretch and the
          row never wraps, because the whole point is one continuous line from
          BUILD to SERVING (canvas 1c, and 14c on a 300px sheet). Inline in a
          deployment row it stays a compact badge strip instead. */}
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
          // The connector leading INTO the live stage is drawn in the stage's
          // own blue (14c): the line says how far the work has got, and the
          // work has got to here.
          const leadsIn = current;
          return (
            <Fragment key={stage.key}>
              {i > 0 && (
                <span
                  aria-hidden
                  className={cn(
                    "h-px",
                    ink ? "mx-[6px] flex-1 sm:mx-[7px]" : "mx-2 w-4 sm:w-6",
                    ink
                      ? leadsIn
                        ? "bg-pane-info"
                        : reached
                          ? "bg-pane-ok"
                          : "bg-pane-border"
                      : leadsIn
                        ? "bg-status-deploying"
                        : reached
                          ? "bg-border-strong"
                          : "bg-border",
                  )}
                />
              )}
              <li
                className={cn(
                  "inline-flex items-center gap-1.5 font-mono uppercase tracking-wider",
                  // 9px on the phone sheet (14c), 10px in the column.
                  ink ? "shrink-0 text-[9px] sm:text-[10px]" : "text-[10.5px]",
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
                          "rounded-[3px] border border-pane-info/40 bg-pane-info/12 px-1.5 py-px text-pane-info",
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
                {stage.short === stage.label ? (
                  stage.label
                ) : (
                  <>
                    {/* Only the displayed label is in the accessibility tree —
                        the other is display:none, not merely invisible. */}
                    <span className="sm:hidden">{stage.short}</span>
                    <span className="hidden sm:inline">{stage.label}</span>
                  </>
                )}
              </li>
            </Fragment>
          );
        })}
      </ol>
      <span role="status" aria-live="polite" className="sr-only">
        {announcement(status, detail)}
      </span>
    </>
  );
}
