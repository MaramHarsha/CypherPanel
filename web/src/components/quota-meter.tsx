// The quota meter and its caps form, shared by Project → Settings → Quotas and
// by the team card in Settings → Teams (resource-quotas.md §10).
//
// One instrument at two scopes rather than two screens that look alike: the
// same three dimensions, the same bar, the same warn and exceeded colours, the
// same "leave it empty to leave it uncapped". Two copies would drift, and the
// first thing to drift would be the thresholds — the one part of this an
// operator reads as a promise.
//
// What the TEAM scope adds is the line §3 owes: the sum of its projects' own
// caps against the team cap. Over-commitment is allowed on purpose — thin
// provisioning is the normal case, and eleven clients do not peak together — so
// it is shown rather than forbidden, and the projects with no cap at all are
// counted beside it, because a sum with nothing behind it reads as "nothing
// promised" when it means "nothing bounded".
//
// There is no price anywhere in this file and there is not going to be: a quota
// is a guardrail, and ADR-012 permits it precisely because it is not a meter.
import type { QuotaReport, QuotaUsage } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { formatBytes } from "@/lib/bytes";
import { cn } from "@/lib/utils";

export const DIMENSION_COPY: Record<string, { label: string; note: string }> = {
  memory: {
    label: "Memory",
    note: "The sum of each resource's declared limit times its replicas — not what they are using. A cap has to be able to answer “will this fit” before the container exists.",
  },
  disk: { label: "Disk", note: "Images, volumes and writable layers, from the newest reading the agents reported." },
  previews: { label: "Live previews", note: "Preview environments that have not been destroyed." },
};

/** The three dimensions as rows. */
export function QuotaMeter({ report }: { report: QuotaReport }) {
  return (
    <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
      {report.usage.map((u) => (
        <UsageRow key={u.dimension} usage={u} />
      ))}
    </ul>
  );
}

function UsageRow({ usage: u }: { usage: QuotaUsage }) {
  const copy = DIMENSION_COPY[u.dimension] ?? { label: u.dimension, note: "" };
  const pct = u.limit != null && u.limit > 0 ? Math.min(100, Math.round((u.used / u.limit) * 100)) : 0;
  const format = formatterFor(u.dimension);

  return (
    <li className="px-4 py-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="text-[13px] font-medium text-text">{copy.label}</span>
        <span className="mono text-[11.5px] text-text-mid">
          {format(u.used)}
          {u.limit != null ? (
            <>
              {" of "}
              {format(u.limit)}
              <span
                className={cn(
                  "ml-1.5",
                  u.state === "exceeded" && "text-danger",
                  u.state === "warn" && "text-status-degraded-text",
                )}
              >
                {pct}%
              </span>
            </>
          ) : (
            <span className="ml-1.5 text-text-faint">uncapped</span>
          )}
        </span>
      </div>
      {u.limit != null && (
        <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-border">
          <div
            className={cn(
              "h-full rounded-full",
              u.state === "exceeded" ? "bg-danger" : u.state === "warn" ? "bg-status-degraded" : "bg-accent",
            )}
            style={{ width: `${pct}%` }}
          />
        </div>
      )}
      {copy.note && <p className="mt-1 text-[11.5px] leading-[1.5] text-text-faint">{copy.note}</p>}
      <CommittedLine usage={u} />
    </li>
  );
}

/**
 * The sum of this team's project caps, on a team report only (§3).
 *
 * A project report carries no `committed` at all, which is why absence is the
 * test rather than a zero: nothing sits below a project, and a "0 B committed"
 * row there would be an answer to a question nobody asked.
 */
function CommittedLine({ usage: u }: { usage: QuotaUsage }) {
  if (u.committed == null) return null;
  const format = formatterFor(u.dimension);
  const uncapped = u.uncapped_projects ?? 0;
  const over = u.limit != null && u.committed > u.limit;

  if (u.committed === 0 && uncapped === 0) {
    return <p className="mt-1 text-[11.5px] leading-[1.5] text-text-faint">No projects in this team yet.</p>;
  }
  return (
    <p className={cn("mt-1 text-[11.5px] leading-[1.5]", over ? "text-status-degraded-text" : "text-text-faint")}>
      {u.committed > 0 && <>Its projects have caps of their own totalling {format(u.committed)}. </>}
      {over && (
        <>
          That is more than this cap, and it is allowed: they will not all peak together, and forcing two numbers into
          lockstep would refuse the operator at the moment they were being careful.{" "}
        </>
      )}
      {uncapped > 0 && (
        <>
          {uncapped === 1 ? "One project has" : `${uncapped} projects have`} no cap here, so only this number bounds{" "}
          {uncapped === 1 ? "it" : "them"}.
        </>
      )}
    </p>
  );
}

/** The two admissions the meter cannot make on its own. */
export function QuotaCaveats({ report }: { report: QuotaReport }) {
  return (
    <>
      {report.unlimited.length > 0 && (
        <div className="rounded-md border border-status-degraded/40 bg-status-degraded/5 px-3.5 py-3">
          <p className="text-[12.5px] leading-[1.5] text-status-degraded-text">
            {report.unlimited.length === 1 ? "This resource has" : "These resources have"} no memory limit, so a
            memory cap here could not be enforced: {report.unlimited.join(", ")}. Give each one a limit on its own
            settings page first.
          </p>
        </div>
      )}
      {report.uncounted_compose_stacks > 0 && (
        <p className="text-[11.5px] leading-[1.5] text-text-faint">
          {report.uncounted_compose_stacks === 1
            ? "1 compose stack is"
            : `${report.uncounted_compose_stacks} compose stacks are`}{" "}
          not counted in the memory figure — their memory is declared inside their own file, and reading half of it
          out would look complete while being wrong.
        </p>
      )}
    </>
  );
}

/** What a cap does NOT block — the difference between a guardrail and an outage. */
export function QuotaEnforcementNote() {
  return (
    <p className="text-[11.5px] leading-[1.5] text-text-faint">
      Reaching a cap refuses new deploys here. It never refuses a rollback — recovery always works, because a
      guardrail that blocks recovery has become the outage it was installed to prevent.
    </p>
  );
}

export interface QuotaCaps {
  memoryMB: string;
  diskGB: string;
  previews: string;
}

export interface QuotaCapsFormProps {
  caps: QuotaCaps;
  onChange: (caps: QuotaCaps) => void;
  onSave: () => void;
  onRemove: () => void;
  saving: boolean;
  removing: boolean;
  /** Whether a quota exists to remove. */
  capped: boolean;
  error: string | null;
  /** "this project" / "this team", for the destructive confirm. */
  scopeNoun: string;
  /**
   * Absent means the viewer may edit. Present is the rank they would need,
   * named on the disabled control rather than left as a mystery
   * (web-ui-design.md §3) — a team cap is panel admin's, deliberately.
   */
  disabledReason?: string;
}

export function QuotaCapsForm({
  caps,
  onChange,
  onSave,
  onRemove,
  saving,
  removing,
  capped,
  error,
  scopeNoun,
  disabledReason,
}: QuotaCapsFormProps) {
  const readOnly = disabledReason != null;
  return (
    <div className="space-y-4 rounded-lg border border-border bg-surface px-4 py-4">
      <p className="text-[13px] font-semibold text-text">Set the caps</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Memory" qualifier="· MiB">
          {(id) => (
            <Input
              id={id}
              type="number"
              min={1}
              value={caps.memoryMB}
              disabled={readOnly}
              onChange={(e) => onChange({ ...caps, memoryMB: e.target.value })}
              placeholder="uncapped"
              className="mono"
            />
          )}
        </Field>
        <Field label="Disk" qualifier="· GiB">
          {(id) => (
            <Input
              id={id}
              type="number"
              min={1}
              value={caps.diskGB}
              disabled={readOnly}
              onChange={(e) => onChange({ ...caps, diskGB: e.target.value })}
              placeholder="uncapped"
              className="mono"
            />
          )}
        </Field>
        <Field label="Live previews">
          {(id) => (
            <Input
              id={id}
              type="number"
              min={1}
              value={caps.previews}
              disabled={readOnly}
              onChange={(e) => onChange({ ...caps, previews: e.target.value })}
              placeholder="uncapped"
              className="mono"
            />
          )}
        </Field>
      </div>
      <p className="text-[12px] leading-[1.5] text-text-faint">
        Leave a field empty to leave that dimension uncapped. Zero is not a cap — to stop deploys entirely, use a
        freeze window, which says so where somebody will read it.
      </p>
      {error && (
        <p role="alert" className="text-[13px] text-danger">
          {error}
        </p>
      )}
      <div className="flex items-center justify-end gap-2">
        {capped && !readOnly && (
          <ConfirmDestructive
            trigger={
              <Button size="sm" variant="ghost" className="text-danger">
                Remove the quota
              </Button>
            }
            title={`Remove ${scopeNoun}'s quota?`}
            blastRadius={`Nothing will bound what ${scopeNoun} consumes across the fleet. Individual container limits still apply.`}
            actionLabel="Remove quota"
            pendingLabel="Removing…"
            pending={removing}
            onConfirm={onRemove}
          />
        )}
        <ActionButton
          variant="secondary"
          size="sm"
          state={saving ? "busy" : "idle"}
          busyLabel="Saving…"
          disabledReason={disabledReason}
          onClick={onSave}
        >
          Save caps
        </ActionButton>
      </div>
    </div>
  );
}

function formatterFor(dimension: string): (n: number) => string {
  return dimension === "previews" ? (n: number) => String(n) : formatBytes;
}

/** The stored cap for one dimension, as the string a number field holds. */
export function limitOf(report: QuotaReport, dimension: string): string {
  const u = report.usage.find((x) => x.dimension === dimension);
  return u?.limit != null ? String(u.limit) : "";
}

export function limitToMB(report: QuotaReport, dimension: string): string {
  const raw = limitOf(report, dimension);
  return raw ? String(Math.round(Number(raw) / 1024 / 1024)) : "";
}

export function limitToGB(report: QuotaReport, dimension: string): string {
  const raw = limitOf(report, dimension);
  return raw ? String(Math.round(Number(raw) / 1024 / 1024 / 1024)) : "";
}

/** The three fields as the request body, with empty meaning uncapped. */
export function capsToRequest(caps: QuotaCaps) {
  return {
    memory_limit_bytes: caps.memoryMB ? Number(caps.memoryMB) * 1024 * 1024 : null,
    disk_limit_bytes: caps.diskGB ? Number(caps.diskGB) * 1024 * 1024 * 1024 : null,
    preview_limit: caps.previews ? Number(caps.previews) : null,
  };
}
