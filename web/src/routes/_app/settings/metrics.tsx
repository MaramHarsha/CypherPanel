// Settings · Metrics (metrics-and-usage.md §5).
//
// Three knobs, not one per dimension, and each one's copy says the thing an
// operator would otherwise learn the hard way:
//
//   - Turning request analytics on RECREATES the Proxy container on every node,
//     because the access-log block is part of its static configuration. That is
//     a few seconds with no routing there, and it is said before the click.
//   - Request analytics is off by default because paths are the operator's
//     application's own data. The sanitiser is described honestly — it removes
//     id-shaped segments and depth, not everything — rather than implied to be
//     complete.
//   - The bucket is load-bearing for the write budget, so its help text says
//     what raising it buys.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  getGetMetricsSettingsQueryKey,
  useGetMetricsSettings,
  useSetMetricsSettings,
} from "@/api/gen/panel/panel";
import type { MetricsSettings } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Field } from "@/components/ui/field";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/settings/metrics")({ component: MetricsSettingsTab });

const BUCKETS = [
  { value: 60, label: "1 minute", note: "finest detail, five times the rows" },
  { value: 300, label: "5 minutes", note: "the default" },
  { value: 900, label: "15 minutes", note: "a third of the rows" },
  { value: 1800, label: "30 minutes", note: "a sixth of the rows" },
];

function MetricsSettingsTab() {
  useCrumbs([{ label: "settings" }, { label: "metrics" }]);
  const settings = useGetMetricsSettings();
  return (
    <div className="max-w-2xl space-y-4">
      <Eyebrow>Metrics collection</Eyebrow>
      <PageState query={settings} isEmpty={() => false} skeletonRows={3}>
        {(s) => <Form key={`${s.enabled}-${s.request_analytics}-${s.bucket_seconds}`} settings={s} />}
      </PageState>
    </div>
  );
}

function Form({ settings }: { settings: MetricsSettings }) {
  const qc = useQueryClient();
  const [enabled, setEnabled] = useState(settings.enabled);
  const [analytics, setAnalytics] = useState(settings.request_analytics);
  const [bucket, setBucket] = useState(settings.bucket_seconds);

  const save = useSetMetricsSettings({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetMetricsSettingsQueryKey() });
        toastSuccess({
          title: "Saved",
          detail:
            analytics !== settings.request_analytics
              ? "Each node's Proxy restarts once to pick up the change."
              : "Every node picks this up on its next sync.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not save", e, { retry: () => save.mutate(vars) }),
    },
  });

  const dirty =
    enabled !== settings.enabled ||
    analytics !== settings.request_analytics ||
    bucket !== settings.bucket_seconds;

  return (
    <div className="space-y-4 rounded-lg border border-border bg-surface px-4 py-4">
      <label className="flex items-start gap-2.5">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => setEnabled(e.currentTarget.checked)}
          className="mt-0.5 size-3.5 accent-accent"
        />
        <span className="min-w-0">
          <span className="block text-[13px] font-semibold text-text">Collect container metrics</span>
          <span className="block text-[12.5px] leading-[1.5] text-text-mid">
            CPU, memory and disk for every application, stack and database the panel manages. Containers the panel
            does not manage are never sampled, so your own workloads on a shared box stay out of it. Turning this off
            stops collection everywhere; what has already been recorded stays until it ages out.
          </span>
        </span>
      </label>

      <label className="flex items-start gap-2.5 border-t border-border-subtle pt-4">
        <input
          type="checkbox"
          checked={analytics}
          disabled={!enabled}
          onChange={(e) => setAnalytics(e.currentTarget.checked)}
          className="mt-0.5 size-3.5 accent-accent disabled:opacity-40"
        />
        <span className="min-w-0">
          <span className="block text-[13px] font-semibold text-text">Aggregate the Proxy's access log</span>
          <span className="block text-[12.5px] leading-[1.5] text-text-mid">
            Request counts, status codes, latency percentiles and top paths per application. Client addresses, user
            agents, referrers, cookies, headers and the whole query string are dropped at the Proxy and never
            written anywhere.
          </span>
          {/* The two things an operator must know BEFORE the click. */}
          <span className="mt-1.5 block text-[12px] leading-[1.5] text-text-faint">
            Paths are grouped on the node — the query string goes, only the first three segments are kept, and
            id-shaped segments become <code className="mono">:id</code>. That covers the usual{" "}
            <code className="mono">/reset/&lt;token&gt;</code> shape but not every shape, so if your URLs are
            themselves sensitive, leave this off.
          </span>
          {analytics !== settings.request_analytics && (
            <span className="mt-1.5 block text-[12px] leading-[1.5] text-status-degraded-text">
              Saving this restarts the Proxy container on every node once — a few seconds with no routing there.
            </span>
          )}
        </span>
      </label>

      <div className="border-t border-border-subtle pt-4">
        <Field
          label="Bucket size"
          hint="How much time one stored row covers. Nothing is stored per sample or per request, so this is what decides the database's growth: a bigger bucket is proportionally fewer rows and proportionally coarser charts."
        >
          {(id, describedBy) => (
            <select
              id={id}
              aria-describedby={describedBy}
              value={bucket}
              disabled={!enabled}
              onChange={(e) => setBucket(Number(e.target.value))}
              className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text disabled:opacity-40 focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
            >
              {BUCKETS.map((b) => (
                <option key={b.value} value={b.value}>
                  {b.label} — {b.note}
                </option>
              ))}
            </select>
          )}
        </Field>
      </div>

      <div className="flex justify-end border-t border-border-subtle pt-4">
        <ActionButton
          variant="secondary"
          size="sm"
          state={save.isPending ? "busy" : "idle"}
          busyLabel="Saving…"
          disabledReason={!dirty ? "Nothing has changed" : undefined}
          onClick={() =>
            save.mutate({
              data: { enabled, request_analytics: enabled && analytics, bucket_seconds: bucket },
            })
          }
        >
          Save
        </ActionButton>
      </div>
    </div>
  );
}
