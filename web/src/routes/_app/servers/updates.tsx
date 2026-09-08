// Servers → Updates (agent-updates.md §8, ADR-010).
//
// Two channels and a selector per server, because a desired version per host
// would make a forty-host fleet forty decisions that must agree.
//
// Both channels ship EMPTY, and empty means no instruction — upgrading a panel
// must not start replacing binaries across a fleet nobody asked it to touch.
// The cost is real: a fleet whose operator never opens this screen stays stale,
// which is the burden ADR-010 exists to end. Two things pay for it. The empty
// state IS the action (ui-principles §11) — the fleet's current versions and
// one button, "Match the panel". And nothing here promises more than the
// mechanism can deliver: the footer states the security model out loud,
// including the half that is a limitation.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import type { AgentServer, AgentUpdates, SetServerChannelRequestChannel } from "@/api/gen/model";
import {
  getGetAgentUpdatesQueryKey,
  useGetAgentUpdates,
  useGetPanelVersion,
  usePromoteAgentChannel,
  useSetAgentChannel,
} from "@/api/gen/panel/panel";
import { getListServersQueryKey, useSetServerAgentChannel } from "@/api/gen/servers/servers";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { ServersTabs } from "@/components/servers-tabs";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/servers/updates")({ component: AgentUpdatesPage });

const GRID = "flex flex-col gap-1.5 sm:grid sm:grid-cols-[1.6fr_1fr_1fr_0.9fr_1.4fr] sm:items-center sm:gap-4";

function AgentUpdatesPage() {
  useCrumbs([{ label: "servers", to: "/servers" }, { label: "updates" }]);
  // Phases move on their own as agents converge; polling is the honest way to
  // watch them, exactly as the fleet table does.
  const updates = useGetAgentUpdates({ query: { refetchInterval: 5_000 } });

  return (
    <>
      <PageHeader title="Servers" below={<ServersTabs />} />
      <PageBody className="space-y-7 pt-6 pb-9">
        <ControlPlaneCard />
        <PageState query={updates} isEmpty={() => false} skeletonRows={4}>
          {(data) => <Fleet data={data} />}
        </PageState>
        <Footer />
      </PageBody>
    </>
  );
}

// The control plane sits on top and says what it does NOT do. Guided panel
// upgrades are a separate feature; the only thing this screen asserts about the
// plane is that sentence.
function ControlPlaneCard() {
  const version = useGetPanelVersion();
  const v = version.data;
  return (
    <section className="rounded-lg border border-border bg-surface px-4 py-3.5">
      <p className="text-[13px] font-semibold text-text">
        Control plane — cypherd <span className="mono">{v?.version ?? "…"}</span>
      </p>
      <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
        The plane never updates itself. Update deliberately, release notes in hand.
      </p>
      {v?.latest?.notes_url && (
        <a
          href={v.latest.notes_url}
          target="_blank"
          rel="noreferrer noopener"
          className="mt-1 inline-block text-[12.5px] font-medium text-accent hover:underline"
        >
          Release notes ↗
        </a>
      )}
    </section>
  );
}

function Fleet({ data }: { data: AgentUpdates }) {
  const qc = useQueryClient();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: getGetAgentUpdatesQueryKey() });
    void qc.invalidateQueries({ queryKey: getListServersQueryKey() });
  };

  const stable = data.channels.find((c) => c.channel === "stable");
  const canary = data.channels.find((c) => c.channel === "canary");
  const anyCanary = data.servers.some((s) => s.channel === "canary");
  const nothingDesired = !stable?.desired_version && !canary?.desired_version;

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="min-w-0">
          <Eyebrow>
            Agents{stable?.desired_version ? ` — desired ${stable.desired_version}` : ""}
          </Eyebrow>
          {/* WHERE THE BINARIES COME FROM, which no screen said. An agent
              fetches and verifies its own artifact; the panel serves none. So
              a mirror that was wiped, or one an operator never knew was set,
              is the difference between a fleet that can update and one that
              silently cannot — and it was invisible. */}
          {(stable?.resolved_artifact_base || canary?.resolved_artifact_base) && (
            <p className="mono mt-1 truncate text-[11px] text-text-faint">
              binaries from {stable?.resolved_artifact_base || canary?.resolved_artifact_base}
              {canary?.resolved_artifact_base &&
              stable?.resolved_artifact_base &&
              canary.resolved_artifact_base !== stable.resolved_artifact_base
                ? ` · canary from ${canary.resolved_artifact_base}`
                : ""}
            </p>
          )}
        </div>
        <div className="flex items-center gap-2">
          <SetVersionButton
            channel="stable"
            current={stable?.desired_version ?? ""}
            currentBase={stable?.artifact_base ?? ""}
            panelVersion={data.panel_version}
            onSaved={refresh}
          />
          {/* Canary is opt-in per server, so a fleet with nothing on it has one
              channel and no gate — and no button for a gate that does not
              apply. */}
          {anyCanary && (
            <>
              <SetVersionButton
                channel="canary"
                current={canary?.desired_version ?? ""}
                currentBase={canary?.artifact_base ?? ""}
                panelVersion={data.panel_version}
                onSaved={refresh}
              />
              <PromoteButton
                candidate={canary?.desired_version ?? ""}
                stableCount={data.servers.filter((s) => s.channel === "stable").length}
                onPromoted={refresh}
              />
            </>
          )}
        </div>
      </div>

      {nothingDesired && <EmptyChannels data={data} onSaved={refresh} />}

      <div className="overflow-hidden rounded-lg border border-border bg-surface">
        <div
          className={cn(
            GRID,
            "mono hidden border-b border-border-subtle px-4 py-2 text-[10.5px] uppercase tracking-wide text-text-faint sm:grid",
          )}
        >
          <span>Server</span>
          <span>Running</span>
          <span>Desired</span>
          <span>Channel</span>
          <span>Status</span>
        </div>
        <ul className="divide-y divide-border-subtle">
          {data.servers.map((s) => (
            <ServerRow key={s.id} server={s} onSaved={refresh} />
          ))}
          {data.servers.length === 0 && (
            <li className="px-4 py-6 text-[13px] text-text-mid">No servers have joined this panel yet.</li>
          )}
        </ul>
      </div>
    </section>
  );
}

// The empty state IS the action: what the fleet is running, and one button that
// names the version it would set.
function EmptyChannels({ data, onSaved }: { data: AgentUpdates; onSaved: () => void }) {
  const versions = Object.entries(data.running_versions).sort(([a], [b]) => a.localeCompare(b));
  const set = useSetAgentChannel({
    mutation: {
      onSuccess: () => {
        onSaved();
        toastSuccess({
          title: `Stable is now ${data.panel_version}`,
          detail: "Agents converge on it as each host goes quiet.",
        });
      },
      onError: (e: unknown) => toastFailed("Could not set the desired version", e),
    },
  });

  return (
    <div className="rounded-lg border border-border bg-pane px-4 py-3.5">
      <p className="text-[13px] font-semibold text-pane-text">No desired agent version is set</p>
      <p className="mt-0.5 text-[12.5px] leading-[1.5] text-pane-text/80">
        Agents keep running whatever they have. Nothing here changes until you name a version — upgrading the panel
        does not move the fleet.
      </p>
      {versions.length > 0 && (
        <p className="mono mt-1.5 text-[11.5px] text-pane-text/70">
          running now · {versions.map(([v, n]) => `${v} on ${n}`).join(" · ")}
        </p>
      )}
      <div className="mt-3">
        <ActionButton
          variant="primary"
          size="sm"
          state={set.isPending ? "busy" : "idle"}
          busyLabel="Setting…"
          disabledReason={
            data.panel_version.startsWith("v") ? undefined : "This panel is a development build with no release to match"
          }
          onClick={() => set.mutate({ channel: "stable", data: { version: data.panel_version } })}
        >
          Match the panel ({data.panel_version})
        </ActionButton>
      </div>
    </div>
  );
}

function ServerRow({ server, onSaved }: { server: AgentServer; onSaved: () => void }) {
  const move = useSetServerAgentChannel({
    mutation: {
      onSuccess: onSaved,
      onError: (e: unknown) => toastFailed("Could not change the release channel", e),
    },
  });

  return (
    <li className={cn(GRID, "px-4 py-3")}>
      <span className="truncate text-[13px] font-medium text-text">{server.name}</span>
      <span className="mono text-[12px] text-text-mid">{server.running_version || "—"}</span>
      <span className="mono text-[12px] text-text-mid">{server.desired_version || "—"}</span>
      <span>
        <select
          value={server.channel}
          disabled={move.isPending}
          onChange={(e) =>
            move.mutate({ id: server.id, data: { channel: e.currentTarget.value as SetServerChannelRequestChannel } })
          }
          aria-label={`Release channel for ${server.name}`}
          className={cn(
            "mono rounded border border-border-input bg-surface px-1.5 py-0.5 text-[11.5px] text-text",
            "focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none",
          )}
        >
          <option value="stable">stable</option>
          <option value="canary">canary</option>
        </select>
      </span>
      <PhaseCell server={server} />
    </li>
  );
}

// The phase, in the panel's own voice. A status nobody can expand is a status
// nobody can act on, so a rollback carries its detail behind a disclosure —
// which version failed and what it said.
function PhaseCell({ server }: { server: AgentServer }) {
  const [open, setOpen] = useState(false);

  if (server.status === "unknown") {
    // Never stale-fresh (ui-principles §§1, 10): an agent we have not heard
    // from has no phase we can currently verify.
    return <span className="mono text-[11.5px] text-status-unknown">unknown</span>;
  }
  if (server.phase === "rolled_back") {
    return (
      <span className="min-w-0">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="mono text-left text-[11.5px] text-status-degraded-text hover:underline"
          aria-expanded={open}
        >
          degraded — rolled back {open ? "▾" : "▸"}
        </button>
        {open && (
          <span className="mt-1 block text-[11.5px] leading-[1.5] text-text-mid">
            {server.target_version && (
              <span className="mono block">
                {server.target_version} failed
              </span>
            )}
            {server.detail || "The agent returned to its previous version."}
          </span>
        )}
      </span>
    );
  }
  if (server.phase === "disabled") {
    return (
      <span className="min-w-0">
        <span className="mono block text-[11.5px] text-text-faint">excluded</span>
        <span className="block text-[11.5px] leading-[1.5] text-text-faint">{server.detail}</span>
      </span>
    );
  }
  if (server.phase === "failed") {
    return (
      <span className="min-w-0">
        <span className="mono block text-[11.5px] text-danger">update failed</span>
        <span className="block text-[11.5px] leading-[1.5] text-text-mid">{server.detail}</span>
      </span>
    );
  }
  if (server.converged) {
    return <span className="mono text-[11.5px] text-status-running">✓ converged</span>;
  }
  return <span className="mono text-[11.5px] text-status-deploying">{IN_FLIGHT[server.phase] ?? "pending…"}</span>;
}

const IN_FLIGHT: Record<string, string> = {
  pending: "waiting for the host to go quiet…",
  downloading: "downloading…",
  verifying: "verifying signature…",
  swapping: "installing…",
  idle: "pending…",
};

function SetVersionButton({
  channel,
  current,
  currentBase,
  panelVersion,
  onSaved,
}: {
  channel: "stable" | "canary";
  current: string;
  /** The mirror already configured, so setting a version does not erase it. */
  currentBase: string;
  panelVersion: string;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [version, setVersion] = useState(current);
  const [base, setBase] = useState("");
  const [error, setError] = useState<string | null>(null);

  const set = useSetAgentChannel({
    mutation: {
      onSuccess: () => {
        setError(null);
        setOpen(false);
        onSaved();
        toastSuccess({
          title: version ? `${channel} is now ${version}` : `${channel} has no desired version`,
          detail: version ? "Agents converge as each host goes quiet." : "Agents keep whatever they are running.",
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not set the version"),
    },
  });

  const backwards = current !== "" && version !== "" && compareVersions(version, current) < 0;

  return (
    <>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={() => {
          setVersion(current);
          // Seeded, not blanked. Blanking it meant that setting a version — the
          // ordinary reason to open this dialog — silently erased the artifact
          // mirror, and no screen ever showed where agent binaries came from,
          // so the erasure was invisible until a host tried to fetch one.
          setBase(currentBase);
          setError(null);
          setOpen(true);
        }}
      >
        Set {channel}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          title={`Desired agent version — ${channel}`}
          description="Every server on this channel converges on it as soon as its host goes quiet. Agents fetch and verify the artifact themselves; the panel never serves a binary."
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setError(null);
              set.mutate({ channel, data: { version: version.trim(), artifact_base: base.trim() } });
            }}
            className="space-y-4"
          >
            <Field
              label="Version"
              qualifier={`· this panel is ${panelVersion}`}
              hint="Empty clears the instruction, and agents keep whatever they are running. A version newer than the panel is refused."
              error={error ?? undefined}
            >
              {(id, describedBy) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  autoFocus
                  value={version}
                  onChange={(e) => setVersion(e.target.value)}
                  placeholder={panelVersion}
                  className="mono"
                />
              )}
            </Field>
            <Field
              label="Artifact base"
              qualifier="· optional"
              hint="A prefix holding cypher-agent-linux-<arch>, SHA256SUMS and SHA256SUMS.sig. Empty uses the project's releases. Agents verify a signature either way — a mirror does not weaken that."
            >
              {(id, describedBy) => (
                <Input
                  id={id}
                  aria-describedby={describedBy}
                  value={base}
                  onChange={(e) => setBase(e.target.value)}
                  placeholder="https://…"
                  className="mono"
                />
              )}
            </Field>
            {/* The blast radius, stated before it happens (ui-principles §2). */}
            {backwards && (
              <p className="rounded-md border border-status-degraded/35 bg-status-degraded/[0.07] px-3 py-2 text-[12.5px] leading-[1.5] text-status-degraded-text">
                This is older than {current}. Agents on this channel will be permitted to move backwards — which they
                otherwise refuse — and will download and verify the older release again rather than trusting the copy
                they kept.
              </p>
            )}
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton
                type="submit"
                variant="primary"
                size="lg"
                state={set.isPending ? "busy" : "idle"}
                busyLabel="Setting…"
              >
                Set version
              </ActionButton>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}

function PromoteButton({
  candidate,
  stableCount,
  onPromoted,
}: {
  candidate: string;
  stableCount: number;
  onPromoted: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const promote = usePromoteAgentChannel({
    mutation: {
      onSuccess: () => {
        setError(null);
        setOpen(false);
        onPromoted();
        toastSuccess({ title: `Stable is now ${candidate}`, detail: "Every stable host converges as it goes quiet." });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not promote canary"),
    },
  });

  return (
    <>
      <Button
        type="button"
        variant="secondary"
        size="sm"
        disabledReason={candidate ? undefined : "Canary names no version yet"}
        onClick={() => {
          setError(null);
          setOpen(true);
        }}
      >
        Promote canary → stable
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          title={`Promote ${candidate} to stable?`}
          description={`All ${stableCount} stable ${stableCount === 1 ? "server" : "servers"} move to it at once — there are no waves and no percentages.`}
          size="alert"
        >
          <div className="space-y-3">
            <p className="text-[12.5px] leading-[1.5] text-text-mid">
              Each agent waits for its host to go quiet, verifies the release against a key baked into its own binary,
              and restarts. Containers keep running throughout. A host that cannot reach the bus on the new version
              puts its previous one back by itself.
            </p>
            {error && (
              <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[12.5px] text-danger">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost" size="lg">
                  Cancel
                </Button>
              </DialogClose>
              <ActionButton
                variant="primary"
                size="lg"
                state={promote.isPending ? "busy" : "idle"}
                busyLabel="Promoting…"
                onClick={() => promote.mutate()}
              >
                Promote
              </ActionButton>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

// The footer is the security model, and it is the copy. Both halves: what the
// signature buys, and what a failed update leaves behind.
function Footer() {
  return (
    <p className="max-w-3xl text-[12px] leading-[1.6] text-text-faint">
      Agents fetch signed artifacts themselves over outbound HTTPS and verify against a baked-in key — a compromised
      panel can only pick among genuine releases. A failed update re-execs the previous binary and reports degraded:
      stranded at the old version, never off the bus.
    </p>
  );
}

/** Enough of semver to warn about a downgrade before it is confirmed. */
function compareVersions(a: string, b: string): number {
  const parse = (v: string) => (v.replace(/^v/, "").split("-")[0] ?? "").split(".").map((n) => Number.parseInt(n, 10));
  const [x, y] = [parse(a), parse(b)];
  for (let i = 0; i < 3; i++) {
    const l = x[i] ?? 0;
    const r = y[i] ?? 0;
    if (Number.isNaN(l) || Number.isNaN(r)) return 0;
    if (l !== r) return l < r ? -1 : 1;
  }
  return 0;
}
