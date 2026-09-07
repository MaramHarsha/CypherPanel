// Settings · Disaster recovery (plane-disaster-recovery.md §10).
//
// This screen has one job that no other screen in the panel has: it hands the
// operator a secret they must keep, and then it must be honest about not
// knowing whether they kept it.
//
// So two pieces of state are drawn separately and never collapsed into one:
//
//   ARMED — a configuration exists and snapshots are being written.
//   PROVEN — someone has actually decrypted one with the key they hold.
//
// A panel that showed only the first would be a year of green checkmarks
// ending in a discovery. The check that turns the first into the second is
// offered directly, because a backup nobody can open is worse than no backup.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useListBackupTargets } from "@/api/gen/backups/backups";
import {
  armPlaneDisasterRecovery,
  getGetPlaneDisasterRecoveryQueryKey,
  getListPlaneSnapshotsQueryKey,
  useDisarmPlaneDisasterRecovery,
  useGetPlaneDisasterRecovery,
  useListPlaneSnapshots,
  useRunPlaneSnapshot,
  useVerifyRecoveryKey,
} from "@/api/gen/panel/panel";
import type { PlaneDisasterRecovery, PlaneSnapshot } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyButton } from "@/components/copy-field";
import { CronField } from "@/components/cron-field";
import { EmptyState } from "@/components/empty-state";
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

export const Route = createFileRoute("/_app/settings/disaster-recovery")({ component: DisasterRecoveryTab });

const SELECT =
  "w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none";

function DisasterRecoveryTab() {
  useCrumbs([{ label: "settings" }, { label: "disaster recovery" }]);
  const dr = useGetPlaneDisasterRecovery({ query: { retry: false } });

  return (
    <div className="max-w-2xl space-y-5">
      <Eyebrow>Disaster recovery</Eyebrow>
      <p className="max-w-prose text-[12.5px] leading-[1.5] text-text-mid">
        A nightly encrypted archive of everything this panel knows — projects, desired state, sealed secrets, users,
        the audit log — pushed to a bucket you own. Losing the panel's machine then costs one command on a new one:
        agents re-adopt on their next heartbeat, desired state reconverges, and{" "}
        <strong className="font-medium text-text">nothing redeploys</strong>, because your applications never stopped
        running.
      </p>

      <PageState query={dr} isEmpty={() => false} skeletonRows={3}>
        {(cfg) => (cfg.armed ? <Armed config={cfg} /> : <NotArmed />)}
      </PageState>
    </div>
  );
}

function NotArmed() {
  return (
    <div className="rounded-lg border border-border bg-surface">
      <EmptyState
        glyph="⛊"
        title="This panel is not backing itself up"
        hint="Your applications and their data are on your servers and are unaffected by losing this machine. What is here is everything the panel knows about them — and without a copy, rebuilding it is manual."
        action={<ArmDialog primary />}
      />
    </div>
  );
}

function Armed({ config }: { config: PlaneDisasterRecovery }) {
  const qc = useQueryClient();
  const proven = Boolean(config.recipient_verified_at);

  const run = useRunPlaneSnapshot({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListPlaneSnapshotsQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetPlaneDisasterRecoveryQueryKey() });
        toastSuccess("Snapshot taken");
      },
      onError: (e: unknown, vars) => toastFailed("Could not take a snapshot", e, { retry: () => run.mutate(vars) }),
    },
  });
  const disarm = useDisarmPlaneDisasterRecovery({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPlaneDisasterRecoveryQueryKey() });
        toastSuccess({
          title: "Disarmed",
          detail: "The archives already in your bucket are untouched — disarming is not the same as discarding.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not disarm", e, { retry: () => disarm.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-4">
      {/* Armed and proven are drawn separately, because they are different
          claims and collapsing them is how a panel lies by omission. */}
      <div
        className={cn(
          "space-y-2 rounded-lg border px-4 py-3.5",
          proven ? "border-status-running/40 bg-status-running/5" : "border-status-degraded/40 bg-status-degraded/5",
        )}
      >
        <p className="text-[13.5px] font-semibold text-text">
          {proven ? "Backing up, and the recovery key has been checked" : "Backing up — the recovery key is unchecked"}
        </p>
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          {proven ? (
            <>
              Someone opened a snapshot with the recovery key {relativeTime(config.recipient_verified_at ?? "")}. That
              is the only proof that matters.
            </>
          ) : (
            <>
              Snapshots are being written, but nobody has proved they can be opened. Without the recovery key an
              archive is unreadable — it is the master key, and this panel does not have a copy. Check it now while
              nothing is on fire.
            </>
          )}
        </p>
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <VerifyDialog />
          <ActionButton
            size="sm"
            variant="secondary"
            state={run.isPending ? "busy" : "idle"}
            busyLabel="Taking…"
            onClick={() => run.mutate()}
          >
            Snapshot now
          </ActionButton>
          <span className="ml-auto flex items-center gap-2">
            <ArmDialog config={config} />
            <ConfirmDestructive
              trigger={
                <Button size="sm" variant="ghost" className="text-danger">
                  Disarm
                </Button>
              }
              title="Stop backing this panel up?"
              lead="Disarming:"
              blastRadius={[
                "no further snapshots are taken",
                "the archives already in your bucket are untouched — this is not the same decision as deleting them",
                "losing this machine would then mean rebuilding the panel by hand",
              ]}
              actionLabel="Disarm"
              pendingLabel="Disarming…"
              pending={disarm.isPending}
              onConfirm={() => disarm.mutate()}
            />
          </span>
        </div>
      </div>

      <dl className="space-y-1.5 rounded-lg border border-border bg-surface px-4 py-3.5 text-[12.5px]">
        <Row label="Schedule">
          <span className="mono">{config.schedule}</span>
        </Row>
        <Row label="Keep">{config.retention_count} snapshots</Row>
        <Row label="Path">
          <span className="mono">{config.path_prefix}</span>
        </Row>
        <Row label="Encrypted to">
          <span className="mono text-[11.5px]">{config.recipient}</span>
        </Row>
        <Row label="Last run">
          {config.last_run_at ? (
            <span title={absoluteTime(config.last_run_at)}>
              {relativeTime(config.last_run_at)}
              {config.last_status && ` (${config.last_status})`}
            </span>
          ) : (
            "never"
          )}
        </Row>
        {config.last_detail && <p className="pt-1 text-[12px] text-danger">{config.last_detail}</p>}
      </dl>

      <Snapshots />

      {/* The command, written out, because the moment it is needed is not the
          moment to go looking for documentation. */}
      <section className="space-y-2">
        <Eyebrow>Recovering</Eyebrow>
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          On a new machine with the panel installed and an empty database:
        </p>
        <div className="flex items-start gap-2.5 rounded-md border border-pane-border bg-pane px-3.5 py-3">
          <code className="min-w-0 flex-1 break-all font-mono text-[12px] text-pane-text">
            cypherd restore --from s3://your-bucket/{config.path_prefix}/… --identity ./recovery.key --endpoint
            https://…
          </code>
          <span className="-mr-1 -mt-1 shrink-0">
            <CopyButton
              value={`cypherd restore --from s3://your-bucket/${config.path_prefix}/SNAPSHOT.tar.age --identity ./recovery.key --endpoint https://YOUR-S3-ENDPOINT`}
              label="Copy the restore command"
            />
          </span>
        </div>
        <p className="text-[11.5px] leading-[1.5] text-text-faint">
          The recovery key goes in a file, never on the command line — anything in a command line is readable by every
          process on the machine. The restore prints the master key to put back in the panel's environment file, and
          refuses a database that is not empty unless you pass <code className="mono">--force</code>.
        </p>
      </section>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <dt className="shrink-0 text-text-faint">{label}</dt>
      <dd className="min-w-0 truncate text-right text-text">{children}</dd>
    </div>
  );
}

function Snapshots() {
  const snaps = useListPlaneSnapshots({ query: { retry: false } });
  if (snaps.isError || !snaps.data || snaps.data.length === 0) return null;
  return (
    <section className="space-y-2">
      <Eyebrow>Snapshots</Eyebrow>
      <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
        {snaps.data.map((s: PlaneSnapshot) => (
          <li key={s.id} className="flex flex-wrap items-baseline justify-between gap-2 px-4 py-2.5">
            <span className="mono min-w-0 truncate text-[12px] text-text">{s.object_key}</span>
            <span className="mono shrink-0 text-[11px] text-text-faint">
              {s.status === "succeeded" ? (
                <>
                  {formatBytes(s.size_bytes)} · {s.row_count.toLocaleString()} rows
                </>
              ) : (
                <span className="text-danger">{s.status}</span>
              )}{" "}
              · <span title={absoluteTime(s.started_at)}>{relativeTime(s.started_at)}</span>
            </span>
          </li>
        ))}
      </ul>
      <p className="text-[11.5px] leading-[1.5] text-text-faint">
        This list is an index of what should be in your bucket, not proof of what is. The bucket is the truth.
      </p>
    </section>
  );
}

function ArmDialog({ config, primary }: { config?: PlaneDisasterRecovery; primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [targetId, setTargetId] = useState(config?.target_id ?? "");
  const [prefix, setPrefix] = useState(config?.path_prefix ?? "plane-state");
  const [schedule, setSchedule] = useState(config?.schedule ?? "30 3 * * *");
  const [retention, setRetention] = useState(config?.retention_count ?? 14);
  const [mode, setMode] = useState<"generate" | "provide">(config ? "provide" : "generate");
  const [recipient, setRecipient] = useState(config?.recipient ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Held only until the dialog closes. This is the one secret the panel hands
  // over and never has again — keeping it anywhere else would undo the design.
  const [issued, setIssued] = useState<string | null>(null);

  const targets = useListBackupTargets({ query: { enabled: open } });

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await armPlaneDisasterRecovery({
        target_id: targetId,
        path_prefix: prefix.trim(),
        schedule: schedule.trim(),
        retention_count: retention,
        generate: mode === "generate",
        recipient: mode === "provide" ? recipient.trim() : "",
      });
      void qc.invalidateQueries({ queryKey: getGetPlaneDisasterRecoveryQueryKey() });
      if (res.recovery_key) {
        setIssued(res.recovery_key);
        return;
      }
      setOpen(false);
      toastSuccess("Disaster recovery armed");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not arm");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setIssued(null);
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          {config ? "Change" : "Set up backups"}
        </Button>
      </DialogTrigger>
      <DialogContent
        title={issued ? "Save this recovery key" : config ? "Change disaster recovery" : "Back this panel up"}
        description={
          issued
            ? undefined
            : "Snapshots go to a bucket you already have, encrypted to a key only you hold."
        }
      >
        {issued ? (
          <div className="space-y-3">
            {/* The most consequential screen in the product. It is deliberately
                blunt, because a key nobody saved is a backup nobody has. */}
            <div className="flex items-start gap-2.5 rounded-md border border-pane-border bg-pane px-3.5 py-3">
              <code className="min-w-0 flex-1 break-all font-mono text-[12px] text-pane-text">{issued}</code>
              <span className="-mr-1 -mt-1 shrink-0">
                <CopyButton value={issued} label="Copy the recovery key" />
              </span>
            </div>
            <p className="text-[12.5px] leading-[1.5] text-status-degraded-text">
              This is the only time it is shown. The panel does not store it and cannot show it again — that is what
              makes the archive safe to keep in a bucket. Without it, every snapshot this panel ever writes is
              unreadable.
            </p>
            <p className="text-[12px] leading-[1.5] text-text-mid">
              Put it in a password manager, or on a machine that is not this one. It is exactly as powerful as the
              panel's master key, because the master key is inside the archive it opens.
            </p>
            <div className="flex justify-end">
              <DialogClose asChild>
                <Button variant="primary" size="lg" onClick={() => setOpen(false)}>
                  I have saved it
                </Button>
              </DialogClose>
            </div>
          </div>
        ) : (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
            className="space-y-4"
          >
            <Field label="Backup target" hint="Where the archives go. The same S3 targets your database backups use.">
              {(id) => (
                <select id={id} value={targetId} onChange={(e) => setTargetId(e.target.value)} className={SELECT}>
                  <option value="">Pick a target…</option>
                  {(targets.data ?? []).map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.name} — {t.bucket}
                    </option>
                  ))}
                </select>
              )}
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Path prefix">
                {(id) => <Input id={id} value={prefix} onChange={(e) => setPrefix(e.target.value)} className="mono" />}
              </Field>
              <Field label="Keep" qualifier="· snapshots" hint="A count, not an age.">
                {(id) => (
                  <Input
                    id={id}
                    type="number"
                    min={1}
                    max={365}
                    value={retention}
                    onChange={(e) => setRetention(Number(e.target.value))}
                    className="mono"
                  />
                )}
              </Field>
            </div>

            <Field label="Schedule">
              {(id, describedBy) => (
                <CronField id={id} describedBy={describedBy} value={schedule} onChange={setSchedule} />
              )}
            </Field>

            <Field label="Recovery key">
              {(id) => (
                <select
                  id={id}
                  value={mode}
                  onChange={(e) => setMode(e.target.value as "generate" | "provide")}
                  className={SELECT}
                >
                  <option value="generate">Generate one — shown once, then never again</option>
                  <option value="provide">I have an age public key</option>
                </select>
              )}
            </Field>

            {mode === "provide" && (
              <Field
                label="Public key"
                hint="An age recipient, starting age1. Keep the matching private key somewhere that is not this machine."
              >
                {(id, describedBy) => (
                  <Input
                    id={id}
                    aria-describedby={describedBy}
                    value={recipient}
                    onChange={(e) => setRecipient(e.target.value)}
                    className="mono"
                    placeholder="age1…"
                    spellCheck={false}
                  />
                )}
              </Field>
            )}

            <p className="text-[12px] leading-[1.5] text-text-faint">
              The archive contains this panel's master key, which is what makes it a complete recovery rather than a
              database nobody can open. That is also why it is encrypted to a key the panel itself does not have.
            </p>

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
                type="submit"
                variant="primary"
                size="lg"
                state={busy ? "busy" : "idle"}
                busyLabel="Arming…"
                disabledReason={!targetId ? "Pick a backup target" : undefined}
              >
                {config ? "Save" : "Arm backups"}
              </ActionButton>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function VerifyDialog() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);

  const verify = useVerifyRecoveryKey({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetPlaneDisasterRecoveryQueryKey() });
        setOpen(false);
        setKey("");
        toastSuccess({
          title: "The key opens your snapshots",
          detail: "That is the only proof that matters. Check again whenever you rotate where it is kept.",
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not check the key"),
    },
  });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm">
          Check the recovery key
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Check the recovery key"
        description="The panel decrypts your newest snapshot with the key you paste, then forgets it. It is never stored, never logged, and never sent anywhere."
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            verify.mutate({ data: { recovery_key: key.trim() } });
          }}
          className="space-y-4"
        >
          <Field label="Recovery key">
            {(id) => (
              <textarea
                id={id}
                required
                autoFocus
                value={key}
                onChange={(e) => setKey(e.target.value)}
                rows={3}
                spellCheck={false}
                placeholder="AGE-SECRET-KEY-1…"
                className="w-full rounded-md border border-border-input bg-surface px-3 py-2 font-mono text-[12px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
              />
            )}
          </Field>
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
              type="submit"
              variant="primary"
              size="lg"
              state={verify.isPending ? "busy" : "idle"}
              busyLabel="Checking…"
            >
              Check it
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
