// Settings · Usage (metrics-and-usage.md §8, canvas 12g).
//
// This is the CAPACITY question — which project forces the next server — and
// it is deliberately not a billing one. There is no price, no rate and no
// currency anywhere on this page, and nothing here ever refuses an action:
// usage is reported, and it never gates a deploy. vision.md puts SaaS billing
// and metering out of scope, and this is where the line is walked, so it is
// drawn in the copy as well as in the schema.
//
// The CPU share has a stated denominator in words above the table, because a
// bare "38% of fleet CPU" shown to a member of one team silently discloses the
// size of every other team's load. A project the viewer cannot see contributes
// to neither the numerator nor the denominator.
import { createFileRoute } from "@tanstack/react-router";
import { Download } from "lucide-react";
import { useState } from "react";
import { getExportUsageUrl, useGetUsage } from "@/api/gen/panel/panel";
import type { Usage, UsageProject } from "@/api/gen/model";
import { apiBlob } from "@/api/client";
import { Eyebrow } from "@/components/eyebrow";
import { EmptyState } from "@/components/empty-state";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { formatBytes } from "@/lib/bytes";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/usage")({ component: UsageTab });

/** The last twelve UTC months, newest first. */
function recentMonths(): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = [];
  const now = new Date();
  for (let i = 0; i < 12; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    out.push({
      value: `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`,
      label: d.toLocaleString(undefined, { month: "long", year: "numeric", timeZone: "UTC" }),
    });
  }
  return out;
}

function hours(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${(seconds / 3600).toFixed(1)}h`;
}

function UsageTab() {
  useCrumbs([{ label: "settings" }, { label: "usage" }]);
  const months = recentMonths();
  const [month, setMonth] = useState(months[0]?.value ?? "");
  const usage = useGetUsage({ month });

  return (
    <div className="max-w-4xl space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Eyebrow>Usage</Eyebrow>
        <span className="ml-auto flex items-center gap-2">
          <select
            aria-label="Month"
            value={month}
            onChange={(e) => setMonth(e.target.value)}
            className="rounded-md border border-border-input bg-surface px-2.5 py-1.5 text-[12.5px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
          >
            {months.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
          <ExportButton month={month} />
        </span>
      </div>

      <PageState
        query={usage}
        isEmpty={(u: Usage) => u.projects.length === 0}
        skeletonColumns="1fr auto auto auto"
        skeletonRows={4}
        empty={
          <EmptyState
            glyph="▦"
            title="Nothing measured this month"
            hint="Figures appear once agents have published a few buckets. A panel with metrics collection turned off records nothing at all."
          />
        }
      >
        {(u) => <UsageTable usage={u} />}
      </PageState>
    </div>
  );
}

function UsageTable({ usage: u }: { usage: Usage }) {
  return (
    <div className="space-y-2.5">
      {/* The denominator, in words, above the number it belongs to. */}
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        Shares are of <strong className="font-medium text-text">{u.denominator}</strong> for{" "}
        {new Date(`${u.month}-01T00:00:00Z`).toLocaleString(undefined, {
          month: "long",
          year: "numeric",
          timeZone: "UTC",
        })}
        . Projects you cannot see are in neither the totals nor the shares.
      </p>

      <div className="overflow-x-auto rounded-lg border border-border bg-surface">
        <table className="w-full min-w-[640px] text-[12.5px]">
          <thead>
            <tr className="border-b border-border text-left text-[11px] tracking-[0.04em] text-text-faint uppercase">
              <th className="px-4 py-2.5 font-medium">Project</th>
              <th className="px-3 py-2.5 text-right font-medium">CPU</th>
              <th className="px-3 py-2.5 text-right font-medium">Memory</th>
              <th className="px-3 py-2.5 text-right font-medium">Disk</th>
              <th className="px-3 py-2.5 text-right font-medium">Requests</th>
              <th className="px-4 py-2.5 text-right font-medium">Deploys</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border-subtle">
            {u.projects.map((p: UsageProject) => (
              <tr key={p.project_id}>
                <td className="px-4 py-2.5">
                  <span className="font-medium text-text">{p.project_name || p.project_id}</span>
                </td>
                <td className="px-3 py-2.5 text-right">
                  <span className="mono text-text">{hours(p.cpu_core_seconds)}</span>
                  <span className="mono ml-1.5 text-text-faint">{(p.cpu_share * 100).toFixed(1)}%</span>
                </td>
                <td className="mono px-3 py-2.5 text-right text-text">
                  {formatBytes(p.memory_bytes_mean)}
                  <span className="ml-1.5 text-text-faint">{formatBytes(p.memory_bytes_peak)} peak</span>
                </td>
                <td className="mono px-3 py-2.5 text-right text-text">{formatBytes(p.disk_bytes)}</td>
                <td className="mono px-3 py-2.5 text-right text-text">
                  {p.requests.toLocaleString()}
                  {p.status_5xx > 0 && <span className="ml-1.5 text-danger">{p.status_5xx} 5xx</span>}
                </td>
                <td className="mono px-4 py-2.5 text-right text-text">
                  {p.deploy_count}
                  <span className="ml-1.5 text-text-faint">{hours(p.deploy_seconds)}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <p className="text-[11.5px] leading-[1.5] text-text-faint">
        CPU is core-time consumed; memory is the mean over the month with the highest single reading beside it. Disk is
        what each project is responsible for, and does not add up to a host's usage — image layers are shared. Deploy
        time is build-and-rollout only, so a deploy that waited for an approval is not counted against the project that
        waited.
      </p>
    </div>
  );
}

function ExportButton({ month }: { month: string }) {
  const [busy, setBusy] = useState(false);
  return (
    <ActionButton
      variant="secondary"
      size="sm"
      state={busy ? "busy" : "idle"}
      busyLabel="Preparing…"
      onClick={async () => {
        setBusy(true);
        try {
          const blob = await apiBlob(getExportUsageUrl({ month }));
          if (!blob) return;
          const url = URL.createObjectURL(blob);
          const a = document.createElement("a");
          a.href = url;
          a.download = `usage-${month}.csv`;
          a.click();
          URL.revokeObjectURL(url);
        } catch (e) {
          toastFailed("Could not export", e);
        } finally {
          setBusy(false);
        }
      }}
    >
      <Download className="h-3.5 w-3.5" aria-hidden /> CSV
    </ActionButton>
  );
}
