// Resource metrics (metrics-and-usage.md §11, canvas 13m).
//
// Three honesty rules run through this whole card, and they are the reason it
// is not just two sparklines:
//
//   1. A resource with NO DATA says why. "No data yet" with the reason beats a
//      flat line at 0%, which reads as an idle application and is a different
//      claim entirely (ADR-010: nothing is unknown, never zero).
//   2. A PARTIAL bucket is drawn as partial. The agent restarted, the proxy was
//      recreated, a deploy happened — without this every one of those reads as
//      an outage on the graph.
//   3. PEAK is shown beside mean. A 5-minute mean hides exactly the spike an
//      operator opened the page to find; a container that OOMs once every four
//      minutes looks calm in the mean.
import { useState } from "react";
import type { ResourceMetricPoint, ResourceMetrics } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { formatBytes } from "@/lib/bytes";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { UseQueryResult } from "@tanstack/react-query";

export const WINDOWS = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
  { value: "168h", label: "7d" },
] as const;

export function WindowPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="flex gap-0.5 rounded-md border border-border p-0.5" role="tablist" aria-label="Time window">
      {WINDOWS.map((w) => (
        <button
          key={w.value}
          type="button"
          role="tab"
          aria-selected={value === w.value}
          onClick={() => onChange(w.value)}
          className={cn(
            "rounded-[3px] px-2 py-[3px] font-mono text-[11px] transition-colors",
            value === w.value ? "bg-pane text-pane-text" : "text-text-faint hover:text-text-mid",
          )}
        >
          {w.label}
        </button>
      ))}
    </div>
  );
}

export function useMetricsWindow() {
  return useState<string>("6h");
}

export function MetricsCard({
  query,
  title = "Resources",
  window: win,
  onWindow,
}: {
  query: UseQueryResult<ResourceMetrics, unknown>;
  title?: string;
  window: string;
  onWindow: (v: string) => void;
}) {
  return (
    <section className="space-y-2.5">
      <div className="flex items-center gap-3">
        <Eyebrow>{title}</Eyebrow>
        <span className="ml-auto">
          <WindowPicker value={win} onChange={onWindow} />
        </span>
      </div>
      <PageState query={query} isEmpty={() => false} skeletonRows={3}>
        {(m) => <MetricsBody metrics={m} />}
      </PageState>
    </section>
  );
}

function MetricsBody({ metrics: m }: { metrics: ResourceMetrics }) {
  if (m.points.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-surface px-4 py-6 text-center">
        <p className="text-[13px] font-medium text-text">No data yet</p>
        <p className="mx-auto mt-1 max-w-md text-[12.5px] leading-[1.5] text-text-mid">
          {m.collecting
            ? "The agent publishes one bucket every few minutes. A resource that has just been created, or one on a node running an older agent, has nothing to show yet."
            : "Metrics collection is off for this panel. A panel admin can turn it on in Settings — until then this is not an idle resource, it is an unmeasured one."}
        </p>
      </div>
    );
  }

  const s = m.summary;
  return (
    <div className="space-y-3 rounded-lg border border-border bg-surface px-4 py-3.5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Series
          label="CPU"
          points={m.points}
          value={(p) => p.cpu_percent}
          peak={(p) => p.cpu_percent_peak}
          max={Math.max(1, ...m.points.map((p) => p.cpu_percent_peak))}
          format={(v) => `${v.toFixed(1)}%`}
          summary={`${s.cpu_percent_mean.toFixed(1)}% mean · ${s.cpu_percent_peak.toFixed(1)}% peak`}
        />
        <Series
          label="Memory"
          points={m.points}
          value={(p) => p.memory_bytes}
          peak={(p) => p.memory_bytes_peak}
          max={Math.max(1, ...m.points.map((p) => p.memory_bytes_peak))}
          format={(v) => formatBytes(v)}
          summary={
            s.memory_limit_bytes > 0
              ? `${formatBytes(s.memory_bytes_mean)} of ${formatBytes(s.memory_limit_bytes)} · ${formatBytes(s.memory_bytes_peak)} peak`
              : `${formatBytes(s.memory_bytes_mean)} mean · ${formatBytes(s.memory_bytes_peak)} peak`
          }
        />
      </div>

      {m.disk && (
        <div className="border-t border-border-subtle pt-3">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <span className="text-[12.5px] font-medium text-text">Disk</span>
            <span className="mono text-[11.5px] text-text-mid">
              {formatBytes(m.disk.image_bytes)} image · {formatBytes(m.disk.volume_bytes)} volumes ·{" "}
              {formatBytes(m.disk.container_bytes)} writable
            </span>
          </div>
          {/* The one thing an operator will otherwise assume and be wrong about. */}
          <p className="mt-1 text-[11.5px] leading-[1.5] text-text-faint">
            These do not add up to the host's usage — image layers are shared, so a base layer used by four
            applications is counted for each. For capacity, read the server's own free space. Measured{" "}
            {relativeTime(m.disk.measured_at)}.
          </p>
        </div>
      )}

      {m.as_of && (
        <p className="text-[11px] text-text-faint">
          Newest bucket {relativeTime(m.as_of)}. Grey bars are periods the agent was not observed for.
        </p>
      )}
    </div>
  );
}

function Series({
  label,
  points,
  value,
  peak,
  max,
  format,
  summary,
}: {
  label: string;
  points: ResourceMetricPoint[];
  value: (p: ResourceMetricPoint) => number;
  peak: (p: ResourceMetricPoint) => number;
  max: number;
  format: (v: number) => string;
  summary: string;
}) {
  return (
    <div className="min-w-0">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-[12.5px] font-medium text-text">{label}</span>
        <span className="mono truncate text-[11.5px] text-text-mid">{summary}</span>
      </div>
      <div className="mt-2 flex h-16 items-end gap-[1px]" role="img" aria-label={`${label} over the window`}>
        {points.map((p) => {
          const full = p.bucket_seconds > 0 ? p.covered_seconds / p.bucket_seconds : 1;
          // A bucket the agent barely covered is drawn grey rather than short:
          // a short bar reads as low usage, which is a different claim.
          const partial = full < 0.5;
          const h = Math.max(2, Math.round((value(p) / max) * 100));
          const ph = Math.max(h, Math.round((peak(p) / max) * 100));
          return (
            <span
              key={p.at}
              title={
                partial
                  ? `${new Date(p.at).toLocaleString()} — only ${p.covered_seconds}s of ${p.bucket_seconds}s observed`
                  : `${new Date(p.at).toLocaleString()} — ${format(value(p))} (peak ${format(peak(p))})`
              }
              className="relative flex min-w-0 flex-1 items-end"
              style={{ height: "100%" }}
            >
              {/* The peak sits behind the mean, so the spike is visible without
                  a second chart. */}
              <span
                className={cn("absolute bottom-0 w-full rounded-[1px]", partial ? "bg-border" : "bg-accent/25")}
                style={{ height: `${ph}%` }}
              />
              <span
                className={cn("relative w-full rounded-[1px]", partial ? "bg-border-strong" : "bg-accent")}
                style={{ height: `${h}%` }}
              />
            </span>
          );
        })}
      </div>
    </div>
  );
}
