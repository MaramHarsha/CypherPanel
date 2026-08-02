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
    <ol className={cn("flex flex-wrap items-center gap-y-1", className)} aria-label="Deploy pipeline">
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
                  "mx-2 h-px w-4 sm:w-6",
                  ink
                    ? reached
                      ? "bg-[#5cbf7f]"
                      : "bg-[#3a3630]"
                    : reached
                      ? "bg-border-strong"
                      : "bg-border",
                )}
              />
            )}
            <li
              className={cn(
                "inline-flex items-center gap-1.5 font-mono text-[10.5px] uppercase tracking-wider",
                ink
                  ? cn(
                      serving && "text-[#5cbf7f]",
                      done && "text-[#5cbf7f]",
                      current &&
                        "rounded border border-[#5f9fe8]/40 bg-[#5f9fe8]/12 px-1.5 py-0.5 text-[#5f9fe8]",
                      failedHere && "text-[#ff6a5e]",
                      !done && !current && !failedHere && !serving && "text-[#6f695e]",
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
                    current ? "bg-status-deploying animate-status-pulse" : "border border-current",
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
