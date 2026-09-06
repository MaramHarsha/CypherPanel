// Settings · Diagnostics — the panel's own build, its disk, and a bounded tail
// of its log (control-plane-hardening.md §3–§4, disk-management.md §6).
//
// SOURCING NOTE: no canvas card — the panel log tail and the panel's own disk
// figure both post-date the design. The page is built from the audit table's
// shape (mono, ink pane, filter chips) because it is the same act: reading what
// the panel recorded about itself.
//
// The reason this exists rather than "ssh in and tail the file" is the whole
// point of the feature: there is no file. The log lives in an in-memory ring —
// no shell, no log shipper, nothing to rotate — and this is the only way to
// read it. It is meant to be attached to a bug report alongside a trace id,
// which is why the copy control takes the whole tail at once.
//
// Panel OWNER only, and only from an interactive session: the log names hosts,
// resources and users, so an API token — which may live in a CI runner — must
// never be able to lift it. Secrets never reach a log line by construction
// (ENGINEERING rule 20), which is what makes it safe to render verbatim.
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { useGetPanelLogs, useGetPanelVersion } from "@/api/gen/panel/panel";
import { CopyButton } from "@/components/copy-field";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { disk } from "@/lib/bytes";
import { useCrumbs } from "@/lib/crumbs";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/diagnostics")({ component: DiagnosticsTab });

/** The ring holds at most 500; these are the sizes worth asking for. */
const TAILS = [50, 100, 250, 500] as const;

function DiagnosticsTab() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "diagnostics" }]);
  const me = useGetMe();
  const isOwner = me.data?.role === "owner";
  const version = useGetPanelVersion();

  return (
    <div className="max-w-3xl space-y-6">
      <section className="space-y-2.5">
        <Eyebrow>Control plane</Eyebrow>
        <PageState query={version} isEmpty={() => false} skeletonRows={4}>
          {(v) => {
            const d = disk(v.data_dir_total_bytes, v.data_dir_free_bytes, false);
            return (
              <FactCard title="This panel">
                <Fact label="Version">{v.version}</Fact>
                <Fact label="Commit">{v.commit}</Fact>
                <Fact label="Built with">{v.go_version}</Fact>
                {v.built_at && <Fact label="Built">{relativeTime(v.built_at)}</Fact>}
                {/* The panel reserves nothing and enforces nothing about its own
                    host — refusing to write when its disk is low would turn a
                    disk problem into an outage of the thing that reports disk
                    problems — so this is a figure, not a guard. */}
                <Fact label="Data directory">
                  {d ? `${d.freeLabel} free of ${d.totalLabel} · ${d.usedPercent}% used` : "usage unknown"}
                </Fact>
                {v.latest && (
                  <Fact label="Available">
                    <a
                      href={v.latest.notes_url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-accent underline decoration-accent/40 underline-offset-2"
                    >
                      {v.latest.version} ({v.latest.kind})
                    </a>
                  </Fact>
                )}
              </FactCard>
            );
          }}
        </PageState>
      </section>

      {me.isSuccess && !isOwner ? (
        <EmptyState
          title="The panel log is owner-only"
          hint="It names hosts, resources and users, so reading it is the panel owner's alone — and only from a signed-in session, never an API token."
        />
      ) : (
        <PanelLog enabled={isOwner} />
      )}
    </div>
  );
}

function PanelLog({ enabled }: { enabled: boolean }) {
  const [tail, setTail] = useState<number>(100);
  const logs = useGetPanelLogs({ tail }, { query: { enabled } });

  return (
    <section className="space-y-2.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Eyebrow>Panel log</Eyebrow>
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1" role="group" aria-label="How many lines">
            <span className="mono mr-1 text-[11px] text-text-faint">lines</span>
            {TAILS.map((n) => (
              <button
                key={n}
                type="button"
                aria-pressed={tail === n}
                onClick={() => setTail(n)}
                className={cn(
                  "mono rounded border px-2 py-[3px] text-[11px] transition-colors",
                  tail === n
                    ? "border-border-strong bg-raised font-medium text-text"
                    : "border-border text-text-mid hover:text-text",
                )}
              >
                {n}
              </button>
            ))}
          </div>
          <ActionButton
            size="sm"
            variant="secondary"
            state={logs.isFetching ? "busy" : "idle"}
            busyLabel="Reading…"
            onClick={() => void logs.refetch()}
          >
            Refresh
          </ActionButton>
        </div>
      </div>
      <p className="max-w-2xl text-[12.5px] leading-[1.5] text-text-mid">
        The most recent lines of cypherd’s structured log, oldest first, from an in-memory ring — there is no file to
        tail and nothing ships it anywhere. Quote a trace id from an error and search for it here.
      </p>
      <PageState query={logs} isEmpty={(d) => d.lines.length === 0} empty={<EmptyState title="Nothing in the ring yet" />}>
        {(d) => (
          <>
            <div className="relative">
              <div className="absolute right-2 top-2 z-10">
                {/* The whole tail at once: this is meant to be pasted into a
                    bug report, and line-by-line copying of 250 lines is not a
                    thing anyone does. */}
                <CopyButton value={d.lines.join("\n")} label="Copy the whole tail" />
              </div>
              <pre className="max-h-[min(60vh,560px)] overflow-auto rounded-lg border border-pane-border bg-pane px-4 py-3.5 font-mono text-[11.5px] leading-[1.7] text-pane-text">
                {d.lines.join("\n")}
              </pre>
            </div>
            <p className="mono text-[11px] text-text-faint">
              showing {d.lines.length} of {d.capacity} the ring holds
            </p>
          </>
        )}
      </PageState>
    </section>
  );
}
