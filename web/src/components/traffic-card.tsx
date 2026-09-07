// Application · Traffic (metrics-and-usage.md §11, canvas 13m's TOP PATHS).
//
// Everything here comes from one request, because it is one card: the summary,
// the series and the paths in three round trips is how a panel starts feeling
// slow.
//
// Two labels on this card are load-bearing. "Estimated" appears whenever the
// node fell back to sampling — a number the panel is not sure of is labelled,
// never quietly presented as exact. And the paths footnote says the table is
// directional rather than an inventory, because normalisation is a heuristic
// and the cap means a rare path can be missing.
import type { ResourceTraffic } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { WindowPicker } from "@/components/metrics-card";
import { formatBytes } from "@/lib/bytes";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { UseQueryResult } from "@tanstack/react-query";

export function TrafficCard({
  query,
  window: win,
  onWindow,
}: {
  query: UseQueryResult<ResourceTraffic, unknown>;
  window: string;
  onWindow: (v: string) => void;
}) {
  return (
    <section className="space-y-2.5">
      <div className="flex items-center gap-3">
        <Eyebrow>Traffic</Eyebrow>
        <span className="ml-auto">
          <WindowPicker value={win} onChange={onWindow} />
        </span>
      </div>
      <PageState query={query} isEmpty={() => false} skeletonRows={3}>
        {(t) => <TrafficBody traffic={t} />}
      </PageState>
    </section>
  );
}

function TrafficBody({ traffic: t }: { traffic: ResourceTraffic }) {
  if (t.points.length === 0) {
    return (
      <div className="rounded-lg border border-border bg-surface px-4 py-6 text-center">
        <p className="text-[13px] font-medium text-text">No traffic recorded</p>
        <p className="mx-auto mt-1 max-w-md text-[12.5px] leading-[1.5] text-text-mid">
          {t.collecting
            ? "Nothing has reached this application through the Proxy in this window. An application with no public route never appears here at all."
            : "Request analytics is off for this panel. It is off by default because request paths are your application's own data — a panel admin can turn it on in Settings."}
        </p>
      </div>
    );
  }

  const s = t.summary;
  const max = Math.max(1, ...t.points.map((p) => p.requests));
  const errorRate = s.requests > 0 ? (s.status_5xx / s.requests) * 100 : 0;

  return (
    <div className="space-y-3.5 rounded-lg border border-border bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1.5">
        <Stat label="requests" value={s.requests.toLocaleString()} />
        <Stat label="5xx" value={`${s.status_5xx.toLocaleString()} (${errorRate.toFixed(2)}%)`} bad={s.status_5xx > 0} />
        <Stat label="p50" value={`${s.p50_ms} ms`} />
        <Stat label="p95" value={`${s.p95_ms} ms`} />
        <Stat label="p99" value={`${s.p99_ms} ms`} />
        <Stat label="transferred" value={formatBytes(s.response_bytes)} />
        {s.redirects > 0 && <Stat label="redirects" value={s.redirects.toLocaleString()} />}
      </div>

      {s.sampled && (
        <p className="rounded-md border border-status-degraded/40 bg-status-degraded/5 px-3 py-2 text-[12px] leading-[1.5] text-status-degraded-text">
          Estimated. This node passed its line-rate ceiling and counted 1 request in {s.sample_rate}, so these totals
          are scaled up rather than exact. The percentiles are sound — a uniform sample of a high-rate stream is
          exactly where sampling is safe — but the top paths below are directional.
        </p>
      )}

      <div className="flex h-14 items-end gap-[1px]" role="img" aria-label="Requests over the window">
        {t.points.map((p) => {
          const h = Math.max(2, Math.round((p.requests / max) * 100));
          const bad = p.status_5xx > 0;
          return (
            <span
              key={p.at}
              title={`${new Date(p.at).toLocaleString()} — ${p.requests} requests, ${p.status_5xx} 5xx`}
              className={cn("min-w-0 flex-1 rounded-[1px]", bad ? "bg-danger" : "bg-accent")}
              style={{ height: `${h}%` }}
            />
          );
        })}
      </div>

      {t.paths.length > 0 && (
        <div className="border-t border-border-subtle pt-3">
          <p className="text-[10.5px] font-semibold tracking-[0.06em] text-text-faint uppercase">Top paths</p>
          <ul className="mt-1.5 space-y-1">
            {t.paths.map((p) => (
              <li key={p.path} className="flex items-baseline justify-between gap-3">
                <code className="mono min-w-0 truncate text-[12px] text-text">{p.path}</code>
                <span className="mono shrink-0 text-[11.5px] text-text-mid">
                  {p.requests.toLocaleString()}
                  {p.status_5xx > 0 && <span className="text-danger"> · {p.status_5xx} 5xx</span>}
                </span>
              </li>
            ))}
          </ul>
          {/* Said plainly, because the alternative is an operator trusting a
              number that was never meant to bear that weight. */}
          <p className="mt-2 text-[11.5px] leading-[1.5] text-text-faint">
            Paths are grouped on the node: the query string is dropped, only the first three segments are kept, and
            id-shaped segments become <code className="mono">:id</code>. <code className="mono">(other)</code> is
            everything past the tracking limit. This is a ranking, not an inventory.
          </p>
        </div>
      )}

      {t.as_of && <p className="text-[11px] text-text-faint">Newest bucket {relativeTime(t.as_of)}.</p>}
    </div>
  );
}

function Stat({ label, value, bad }: { label: string; value: string; bad?: boolean }) {
  return (
    <span className="flex items-baseline gap-1.5">
      <span className={cn("mono text-[14px] font-medium", bad ? "text-danger" : "text-text")}>{value}</span>
      <span className="text-[11px] text-text-faint">{label}</span>
    </span>
  );
}
