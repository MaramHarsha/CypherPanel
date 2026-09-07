// Settings · Updates (panel-updates.md §§4, 6, 9; canvas 12h).
//
// This screen exists because of three specific failures in the reference
// platforms: an update button that ran the updater twice and destroyed the
// encryption keys, a proxy broken by an update, and a version-to-version
// update failure. Every piece of copy here is downstream of not repeating them.
//
// Two sentences carry the whole feature and neither is decoration:
//
//   "Your apps keep serving — they don't depend on the plane." True by
//   construction: agents converge on desired state and a plane that restarts
//   mid-deploy does not interrupt one.
//
//   "Safe to leave." Also true by construction: the swap is performed by a root
//   process on the host with no dependency on this browser session, so closing
//   the tab changes nothing.
//
// And the pre-flight is shown BEFORE the button, not behind it: an operator
// deciding whether to upgrade at 22:00 needs the five facts, not a spinner.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  cancelPanelUpgrade,
  getGetPanelUpdatesQueryKey,
  useGetChangelog,
  useGetPanelUpdates,
  useListPanelUpgrades,
  usePreflightPanelUpdate,
  useRestorePanelSnapshot,
  useSetSnapshotRetention,
  useStartPanelUpgrade,
} from "@/api/gen/panel/panel";
import type { PanelSnapshot, PanelUpgrade, UpdateCheck, UpdatePreflight } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyField } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { formatBytes } from "@/lib/bytes";
import { useCrumbs } from "@/lib/crumbs";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/updates")({ component: UpdatesTab });

const PHASE_COPY: Record<string, string> = {
  preflight: "Checking",
  waiting_for_quiesce: "Waiting for running work to finish",
  snapshotting: "Saving a fallback snapshot",
  downloading: "Downloading",
  verifying: "Verifying the signature",
  migrating: "Migrating the database",
  restarting: "Restarting",
  health_gate: "Health check",
  succeeded: "Done",
  rolled_back: "Rolled back",
  failed: "Failed",
};

function UpdatesTab() {
  useCrumbs([{ label: "settings" }, { label: "updates" }]);
  // While an upgrade is in flight the plane restarts under us, so the poll is
  // what makes the progress screen work at all — and a failed poll during the
  // restart window is expected rather than an error.
  const updates = useGetPanelUpdates({
    query: {
      retry: false,
      refetchInterval: (q) => (q.state.data?.active ? 3_000 : false),
    },
  });

  return (
    <div className="max-w-2xl space-y-5">
      <Eyebrow>Updates</Eyebrow>
      <PageState query={updates} isEmpty={() => false} skeletonRows={3}>
        {(u) => (
          <>
            {u.active ? <Progress upgrade={u.active} /> : <Available updates={u} />}
            <History />
            <WhatsNew />
          </>
        )}
      </PageState>
    </div>
  );
}

function Available({
  updates: u,
}: {
  updates: { current: string; latest?: string; kind?: string; notes_url?: string; mode: string };
}) {
  if (!u.latest) {
    return (
      <div className="space-y-3 rounded-lg border border-border bg-surface px-4 py-4">
        <div>
          <p className="text-[13px] font-medium text-text">You&rsquo;re on {u.current}</p>
          <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
            This is the newest release the panel knows about — or the release check is off, in which case it knows
            about none.
          </p>
        </div>
        {/* A panel could only ever install whatever the feed called latest. So
            an operator who turned the check off, or who is behind a network
            that cannot reach it, could not upgrade AT ALL — and neither could
            one who wanted a specific version for a reason. The pre-flight and
            the same dialog do the rest; this only supplies the tag. */}
        <SpecificVersion />
      </div>
    );
  }

  return (
    <div className="space-y-3 rounded-lg border border-accent/40 bg-accent/5 px-4 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-[14px] font-semibold text-text">
            {u.latest} is available
            {u.kind && <span className="mono ml-2 text-[11px] text-text-faint">{u.kind}</span>}
          </p>
          <p className="mono mt-0.5 text-[11.5px] text-text-faint">you're on {u.current}</p>
        </div>
        {u.notes_url && (
          <a
            href={u.notes_url}
            target="_blank"
            rel="noreferrer noopener"
            className="shrink-0 text-[12.5px] font-medium text-accent hover:underline"
          >
            Release notes ↗
          </a>
        )}
      </div>
      {u.mode === "manual" ? <ManualUpgrade version={u.latest} /> : <UpgradeDialog version={u.latest} />}
    </div>
  );
}

// The container install cannot be helped, and hands over cleanly rather than
// drawing a button that would not work.
function ManualUpgrade({ version }: { version: string }) {
  return (
    <div className="space-y-2 border-t border-accent/20 pt-3">
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        This panel runs as a container, so it cannot replace itself — it has no Docker socket, deliberately. Pull the
        new image and recreate it; nothing else changes, and your applications keep serving throughout.
      </p>
      <CopyField value={`docker compose pull cypherd && docker compose up -d cypherd  # ${version}`} />
    </div>
  );
}

function UpgradeDialog({ version }: { version: string }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [retention, setRetention] = useState(7);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<string | null>(null);
  const preflight = usePreflightPanelUpdate({ version }, { query: { enabled: open, retry: false } });

  const start = useStartPanelUpgrade({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPanelUpdatesQueryKey() });
        setOpen(false);
        toastSuccess({
          title: "Upgrade started",
          detail: "Safe to leave this page — the upgrade runs on the host, not in your browser.",
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not start the upgrade"),
    },
  });

  const pf = preflight.data;
  const needsTyped = pf?.needs_typed_confirm ?? false;
  const blocked = pf ? !pf.can_proceed && !(needsTyped && typed === version) : true;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="primary" size="sm">
          Update to {version}
        </Button>
      </DialogTrigger>
      <DialogContent
        title={`Update to ${version}?`}
        description="Nothing changes until you press the button. These checks are all reads."
      >
        <div className="space-y-4">
          <PageState query={preflight} isEmpty={() => false} skeletonRows={5}>
            {(p: UpdatePreflight) => (
              <ul className="space-y-1.5">
                {p.checks.map((c: UpdateCheck) => (
                  <li key={c.key} className="flex items-start gap-2">
                    <span
                      className={cn(
                        "mono mt-[1px] shrink-0 text-[12px]",
                        c.status === "ok" && "text-status-running",
                        c.status === "warn" && "text-status-degraded-text",
                        c.status === "refused" && "text-danger",
                      )}
                      aria-hidden
                    >
                      {c.status === "ok" ? "✓" : c.status === "warn" ? "⚠" : "✕"}
                    </span>
                    <span className="min-w-0">
                      <span className="block text-[12.5px] leading-[1.5] text-text">{c.text}</span>
                      {c.remedy && (
                        <span className="block text-[12px] leading-[1.5] text-text-faint">{c.remedy}</span>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </PageState>

          {/* The one decision the operator makes, and it must not be made for
              them: the fallback is the only thing between a bad release and a
              lost panel. */}
          <Field
            label="Keep the old version"
            hint="You decide when, if ever, the fallback snapshot is pruned."
          >
            {(id) => (
              <select
                id={id}
                value={retention}
                onChange={(e) => setRetention(Number(e.target.value))}
                className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
              >
                <option value={7}>7 days</option>
                <option value={30}>30 days</option>
                <option value={0}>Forever</option>
              </select>
            )}
          </Field>

          {needsTyped && (
            <Field
              label={`Type ${version} to proceed anyway`}
              hint="Agents below this release's floor stop being manageable by this panel. That is close enough to irreversible to ask you to type it."
            >
              {(id) => (
                <Input id={id} value={typed} onChange={(e) => setTyped(e.target.value)} className="mono" spellCheck={false} />
              )}
            </Field>
          )}

          {/* The single most important piece of copy in this feature. */}
          <div className="rounded-md border border-border bg-pane px-3.5 py-3">
            <p className="text-[12.5px] leading-[1.5] text-pane-text">
              The panel is read-only for about a minute, and unreachable for the few seconds of the restart inside
              that. <strong className="font-semibold">Your applications keep serving</strong> — they do not depend on
              the control plane.
            </p>
          </div>

          {error && (
            <p role="alert" className="text-[13px] text-danger">
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
              state={start.isPending ? "busy" : "idle"}
              busyLabel="Starting…"
              disabledReason={blocked ? "The pre-flight refused this upgrade" : undefined}
              onClick={() =>
                start.mutate({
                  data: {
                    version,
                    snapshot_retention_days: retention,
                    acknowledge_incompatible_agents: needsTyped ? typed : undefined,
                  },
                })
              }
            >
              Update now
            </ActionButton>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Progress({ upgrade: u }: { upgrade: PanelUpgrade }) {
  const qc = useQueryClient();
  const [cancelling, setCancelling] = useState(false);
  const beforeSwap = ["preflight", "waiting_for_quiesce", "snapshotting", "downloading", "verifying"].includes(u.phase);

  return (
    <div className="space-y-3 rounded-lg border border-accent/40 bg-accent/5 px-4 py-4">
      <div>
        {/* The heading that answers the only thing an operator wonders about
            their browser. */}
        <p className="text-[14px] font-semibold text-text">Updating to {u.to_version} — safe to leave this page</p>
        <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
          The upgrade runs on the host, not in your browser. Closing this tab, losing your connection or signing out
          changes nothing.
        </p>
      </div>

      <div className="rounded-md border border-border bg-surface px-3.5 py-3">
        <p className="text-[13px] font-medium text-text">{PHASE_COPY[u.phase] ?? u.phase}</p>
        {u.detail && <p className="mt-1 text-[12.5px] leading-[1.5] text-text-mid">{u.detail}</p>}
        <p className="mono mt-1.5 text-[11px] text-text-faint">
          from {u.from_version} · started {relativeTime(u.started_at)}
        </p>
      </div>

      {beforeSwap && (
        <div className="flex justify-end">
          <ActionButton
            variant="secondary"
            size="sm"
            state={cancelling ? "busy" : "idle"}
            busyLabel="Cancelling…"
            onClick={async () => {
              setCancelling(true);
              try {
                await cancelPanelUpgrade();
                void qc.invalidateQueries({ queryKey: getGetPanelUpdatesQueryKey() });
                toastSuccess("Upgrade cancelled — nothing was changed");
              } catch (e) {
                toastFailed("Could not cancel", e);
              } finally {
                setCancelling(false);
              }
            }}
          >
            Cancel
          </ActionButton>
        </div>
      )}
    </div>
  );
}

/**
 * What's new. Embedded in the binary rather than fetched, so it works
 * air-gapped and cannot be written by anyone who compromises a feed — and the
 * AVAILABLE release shows only its version and a link out, because this build
 * predates it and cannot honestly have its notes.
 */
function WhatsNew() {
  const log = useGetChangelog({ query: { retry: false } });
  if (log.isError || !log.data) return null;
  const { entries, current, available } = log.data;

  return (
    <section className="space-y-2">
      <Eyebrow>What's new</Eyebrow>
      <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
        {available && (
          <li className="px-4 py-3">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="mono text-[12.5px] font-medium text-text">{available.version}</span>
              <span className="rounded-full border border-accent/40 bg-accent/10 px-1.5 py-[1px] text-[10px] font-semibold tracking-wide text-accent uppercase">
                Available
              </span>
              {available.notes_url && (
                <a
                  href={available.notes_url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="ml-auto text-[12px] font-medium text-accent hover:underline"
                >
                  Release notes ↗
                </a>
              )}
            </div>
            <p className="mt-0.5 text-[12px] leading-[1.5] text-text-faint">
              This build predates it, so its notes are on the release page rather than here.
            </p>
          </li>
        )}
        {entries.map((e) => (
          <li key={e.version} className="px-4 py-3">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="mono text-[12.5px] font-medium text-text">{e.version}</span>
              {e.version === current && (
                <span className="mono text-[10.5px] text-text-faint">you're on this</span>
              )}
              {e.date && <span className="mono ml-auto text-[11px] text-text-faint">{e.date}</span>}
            </div>
            {e.notes && e.notes.length > 0 && (
              <ul className="mt-1.5 space-y-1">
                {e.notes.map((n) => (
                  <li key={n} className="text-[12.5px] leading-[1.5] text-text-mid">
                    {n}
                  </li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function History() {
  const history = useListPanelUpgrades({ query: { retry: false } });
  if (history.isError || !history.data) return null;
  const { upgrades, snapshots } = history.data;
  if (upgrades.length === 0 && snapshots.length === 0) return null;

  return (
    <div className="space-y-4">
      {upgrades.length > 0 && (
        <section className="space-y-2">
          <Eyebrow>Version history</Eyebrow>
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {upgrades.map((u: PanelUpgrade) => (
              <li key={u.id} className="px-4 py-2.5">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="mono text-[12.5px] text-text">
                    {u.from_version || "—"} → {u.to_version}
                  </span>
                  <span
                    className={cn(
                      "mono text-[11px]",
                      u.phase === "succeeded" ? "text-status-running" : u.phase === "failed" ? "text-danger" : "text-status-degraded-text",
                    )}
                  >
                    {PHASE_COPY[u.phase] ?? u.phase}
                  </span>
                </div>
                <p className="mono mt-0.5 text-[11px] text-text-faint" title={absoluteTime(u.started_at)}>
                  {relativeTime(u.started_at)} ·{" "}
                  {u.actor === "external"
                    ? "installed outside the panel"
                    : u.actor || "unknown"}
                </p>
                {u.detail && <p className="mt-0.5 text-[12px] leading-[1.5] text-text-mid">{u.detail}</p>}
                {/* Going BACK to a version this host already ran, keeping every
                    row written since. It is offered only on a succeeded upgrade
                    that came FROM somewhere — there is nothing to return to
                    otherwise — and the helper refuses any version this host has
                    not run, so the button cannot invent a target.

                    Until this existed the only backward move was the snapshot
                    restore below, which rewinds the database and discards every
                    deploy, user, token and audit row since. An owner who hit a
                    bad release had to choose between losing an hour of work and
                    editing systemd by hand. */}
                {u.phase === "succeeded" && u.from_version && (
                  <RollBackButton toVersion={u.from_version} fromVersion={u.to_version} />
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {snapshots.length > 0 && (
        <section className="space-y-2">
          <Eyebrow>Fallback snapshots</Eyebrow>
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {snapshots.map((s: PanelSnapshot) => (
              <SnapshotRow key={s.id} snapshot={s} />
            ))}
          </ul>
          <p className="text-[11.5px] leading-[1.5] text-text-faint">
            A snapshot is the panel's own database as it was at the start of an upgrade. It does not contain your
            applications' data — those live on your servers and are untouched by any of this.
          </p>
        </section>
      )}
    </div>
  );
}

function SnapshotRow({ snapshot: s }: { snapshot: PanelSnapshot }) {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: ["/api/v1/panel/updates/history"] });

  const pin = useSetSnapshotRetention({
    mutation: {
      onSuccess: () => {
        invalidate();
        toastSuccess(s.pinned ? "Unpinned" : "Pinned — it will never be pruned");
      },
      onError: (e: unknown) => toastFailed("Could not change the retention", e),
    },
  });
  const restore = useRestorePanelSnapshot({
    mutation: {
      onSuccess: () => toastSuccess({ title: "Restore started", detail: "The panel restarts when it finishes." }),
      onError: (e: unknown) => toastFailed("Could not start the restore", e),
    },
  });

  return (
    <li className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5">
      <div className="min-w-0">
        <span className="mono text-[12.5px] text-text">{s.version}</span>
        <span className="mono ml-2 text-[11px] text-text-faint">{formatBytes(s.size_bytes)}</span>
        <p className="mono mt-0.5 text-[11px] text-text-faint" title={absoluteTime(s.created_at)}>
          {relativeTime(s.created_at)} ·{" "}
          {s.pinned ? "pinned" : s.expires_at ? `expires ${relativeTime(s.expires_at)}` : "kept forever"}
        </p>
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <Button
          size="sm"
          variant="ghost"
          onClick={() => pin.mutate({ id: s.id, data: { pinned: !s.pinned, retention_days: 0 } })}
        >
          {s.pinned ? "Unpin" : "Pin"}
        </Button>
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="ghost" className="text-danger">
              Restore
            </Button>
          }
          title={`Restore the panel's database to ${s.version}?`}
          lead="This is the last resort, not a rollback."
          blastRadius={[
            "the panel's database is rewound to the moment this snapshot was taken",
            "everything recorded since — deploys, users, tokens, audit rows — is gone",
            "your applications are untouched: they run on your servers, not here",
          ]}
          confirmName="restore"
          actionLabel="Restore the database"
          pendingLabel="Starting…"
          pending={restore.isPending}
          onConfirm={() => restore.mutate({ id: s.id, data: { confirm: "restore" } })}
        />
      </div>
    </li>
  );
}

/**
 * Putting an earlier version back.
 *
 * Deliberately NOT the upgrade dialog with a different label. The pre-flight it
 * runs is about moving forward — incompatible agents, a typed confirmation for
 * orphaning the fleet — and none of those questions are the ones a rollback
 * raises. What a rollback needs said is what it KEEPS, because the control
 * beside it (snapshot restore) keeps nothing.
 */
function RollBackButton({ toVersion, fromVersion }: { toVersion: string; fromVersion: string }) {
  const qc = useQueryClient();
  const start = useStartPanelUpgrade({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPanelUpdatesQueryKey() });
        toastSuccess({
          title: `Rolling back to ${toVersion}`,
          detail: "Safe to leave this page — it runs on the host, not in your browser.",
        });
      },
      onError: (e: unknown) => toastFailed("Could not start the rollback", e),
    },
  });

  return (
    <div className="mt-1.5">
      <ConfirmDestructive
        trigger={
          <Button variant="ghost" size="sm" className="h-auto px-0 text-[12px]">
            ↺ Roll back to {toVersion}
          </Button>
        }
        title={`Roll back to ${toVersion}?`}
        lead={`The panel restarts on ${toVersion} instead of ${fromVersion}.`}
        blastRadius={[
          "Nothing you created is lost — every project, deploy, user, token and audit row written since stays exactly as it is.",
          "The panel is briefly unavailable while it swaps and restarts; agents keep running and reconverge on their own.",
          "A schema change that shipped in the newer version is migrated back down, so anything only the newer version could store is what you lose.",
        ]}
        actionLabel={`Roll back to ${toVersion}`}
        pendingLabel="Starting…"
        pending={start.isPending}
        onConfirm={() =>
          start.mutate({
            data: { version: toVersion, snapshot_retention_days: 7, rollback: true },
          })
        }
      />
    </div>
  );
}

/**
 * Installing a version by name.
 *
 * The screen offered exactly one target — the release feed's `latest` — so a
 * panel with the update check off, or behind a network that cannot reach the
 * feed, had no way to upgrade from the panel at all. The typed tag runs the
 * same pre-flight and opens the same dialog; nothing about the upgrade path
 * changes, only how its target is chosen.
 */
function SpecificVersion() {
  const [tag, setTag] = useState("");
  const [armed, setArmed] = useState(false);
  const valid = /^v?\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$/.test(tag.trim());

  return (
    <div className="border-t border-border-subtle pt-3">
      <p className="text-[12px] font-semibold text-text">Install a specific version</p>
      <div className="mt-1.5 flex flex-wrap items-center gap-2">
        <Input
          value={tag}
          onChange={(e) => {
            setTag(e.target.value);
            setArmed(false);
          }}
          placeholder="v1.2.0"
          className="mono max-w-[160px]"
          aria-label="Version to install"
        />
        {armed && valid ? (
          <UpgradeDialog version={tag.trim()} />
        ) : (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={!valid}
            onClick={() => setArmed(true)}
          >
            Check {valid ? tag.trim() : "version"}
          </Button>
        )}
      </div>
      <p className="mt-1.5 text-[11.5px] leading-[1.5] text-text-faint">
        The pre-flight runs against the tag you name, exactly as it does for an offered release — a version that does
        not exist, or that this panel cannot move to, is refused there rather than half-installed.
      </p>
    </div>
  );
}
