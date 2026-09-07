// The public status page, drawn inside the panel (status-pages.md §9).
//
// It renders the SAME payload the public routes return, from the same
// endpoint, because the preview is the disclosure control: an operator has to
// be able to read the thing customers will read, not a summary of it. If this
// showed anything the public page does not, or hid anything it does, the
// control would be worthless.
//
// Deliberately not an iframe of the live page: the page under review is
// usually not enabled yet, so there is nothing to frame — and an iframe of a
// page that 404s would teach the operator nothing about what publishing does.
import type { PublicStatusDay, PublicStatusPage } from "@/api/gen/model";
import { absoluteTime } from "@/lib/time";
import { cn } from "@/lib/utils";

const DOT: Record<string, string> = {
  operational: "bg-status-running",
  degraded: "bg-status-degraded",
  down: "bg-danger",
  unknown: "bg-border-strong",
};

const BAR: Record<string, string> = {
  operational: "bg-status-running",
  degraded: "bg-status-degraded",
  down: "bg-danger",
  unknown: "bg-border",
};

export function StatusPagePreview({ page }: { page: PublicStatusPage }) {
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-surface">
      <div className="flex flex-wrap items-baseline justify-between gap-2 border-b border-border px-4 py-3">
        <span className="text-[15px] font-semibold text-text">{page.title}</span>
        {page.domain && <span className="mono text-[11px] text-text-faint">{page.domain}</span>}
      </div>

      <div className="flex items-center gap-2.5 border-b border-border px-4 py-3">
        <span className={cn("size-2.5 shrink-0 rounded-full", DOT[page.state] ?? DOT.unknown)} />
        <span className="text-[13.5px] font-semibold text-text">{page.headline}</span>
      </div>

      {page.components.length === 0 ? (
        <p className="px-4 py-6 text-[13px] text-text-mid">No systems are being reported here yet.</p>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {page.components.map((c) => (
            <li key={c.label} className="px-4 py-3.5">
              <div className="flex items-baseline justify-between gap-3">
                <span className="text-[13px] font-medium text-text">{c.label}</span>
                <span className="mono shrink-0 text-[11.5px] text-text-mid">
                  {c.state} ·{" "}
                  {/* "—", never "100%": a page that has measured nothing must
                      not print a perfect score. */}
                  {typeof c.uptime === "number" ? `${c.uptime.toFixed(2)}%` : "—"}
                </span>
              </div>
              <div className="mt-2 flex gap-[2px]" role="img" aria-label={`${c.label} daily history`}>
                {c.days.map((d: PublicStatusDay) => (
                  <span
                    key={d.date}
                    title={d.title}
                    className={cn("h-6 min-w-0 flex-1 rounded-[1.5px]", BAR[d.state] ?? BAR.unknown)}
                  />
                ))}
              </div>
              <div className="mt-1 flex justify-between text-[10.5px] text-text-faint">
                <span>{page.window_days} days ago</span>
                <span>Today</span>
              </div>
            </li>
          ))}
        </ul>
      )}

      {page.incidents.length > 0 && (
        <div className="border-t border-border px-4 py-3">
          <p className="text-[10.5px] font-semibold tracking-[0.06em] text-text-faint uppercase">Recent incidents</p>
          <ul className="mt-2 space-y-2">
            {page.incidents.map((i) => (
              <li key={i.id} className="text-[12.5px] text-text">
                <span className="font-medium">{i.component}</span> — down for {i.duration}
                {!i.ended_at && " and counting"}
                <span className="mono ml-1.5 text-[11px] text-text-faint">{absoluteTime(i.started_at)}</span>
                {i.message && <p className="mt-0.5 text-[12px] text-text-mid">{i.message}</p>}
              </li>
            ))}
          </ul>
        </div>
      )}

      <p className="border-t border-border px-4 py-2.5 text-[11px] leading-[1.5] text-text-faint">
        Measured by health checks run against each service from the machine it runs on, about once a minute. Outages
        shorter than a minute do not appear. Grey means the service was not being observed, and that time is left out
        of the percentage rather than counted as up.
      </p>
    </div>
  );
}
