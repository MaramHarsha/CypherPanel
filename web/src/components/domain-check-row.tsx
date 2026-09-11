// One line saying what the public internet actually gets back for a domain,
// with the fix under it when there is one.
//
// The verdict comes from the plane's own prober, so this draws it rather than
// deciding it: the panel and the operator must not be able to disagree about
// whether a domain resolves.
import type { DomainCheck } from "@/api/gen/model";
import { cn } from "@/lib/utils";

export function DomainCheckRow({ check }: { check: DomainCheck }) {
  const ok = check.verdict === "ok";
  return (
    <div
      className={cn(
        "rounded-md border px-3 py-2.5",
        ok ? "border-status-running/40 bg-status-running/5" : "border-border bg-pane",
      )}
    >
      <p className={cn("text-[12.5px] leading-[1.5]", ok ? "text-status-running" : "text-text")}>{check.summary}</p>
      {check.remedy && <p className="mt-1 text-[12px] leading-[1.5] text-text-mid">{check.remedy}</p>}
      {check.resolved_ips.length > 0 && (
        <p className="mono mt-1 text-[11px] text-text-faint">resolves to {check.resolved_ips.join(", ")}</p>
      )}
    </div>
  );
}
