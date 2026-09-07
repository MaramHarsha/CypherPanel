// Project settings · Quotas (resource-quotas.md §10; permitted by ADR-012).
//
// What this screen is careful about, because it is what the feature is for:
//
//   The panel can already cap one CONTAINER. What no number could express is
//   the aggregate — one project opening forty previews and filling the disk a
//   paying client's database writes to, with every individual limit respected.
//
// And what it is careful to admit:
//
//   Memory is metered from DECLARED limits, not observed usage, because a
//   workload that has not started uses nothing. A resource with no limit makes
//   the cap a fiction, so the screen names those resources instead of counting
//   them as zero. Compose stacks are not counted at all, and it says so.
//
// There is no price anywhere on this page and there is not going to be: a
// quota is a guardrail, and ADR-012 is what permits it precisely because it is
// not a meter.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  getGetProjectQuotaQueryKey,
  useDeleteProjectQuota,
  useGetProjectQuota,
  useSetProjectQuota,
} from "@/api/gen/projects/projects";
import type { QuotaReport, QuotaUsage } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { formatBytes } from "@/lib/bytes";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/settings/quotas")({ component: QuotasTab });

const DIMENSION_COPY: Record<string, { label: string; note: string }> = {
  memory: {
    label: "Memory",
    note: "The sum of each resource's declared limit times its replicas — not what they are using. A cap has to be able to answer “will this fit” before the container exists.",
  },
  disk: { label: "Disk", note: "Images, volumes and writable layers, from the newest reading the agents reported." },
  previews: { label: "Live previews", note: "Preview environments that have not been destroyed." },
};

function QuotasTab() {
  const { projectId } = Route.useParams();
  useCrumbs([{ label: "settings" }, { label: "quotas" }]);
  const report = useGetProjectQuota(projectId, { query: { retry: false } });

  return (
    <div className="max-w-2xl space-y-4">
      <Eyebrow>Quotas</Eyebrow>
      <p className="max-w-prose text-[12.5px] leading-[1.5] text-text-mid">
        A cap on what this project may consume across the fleet. Individual containers already have their own limits;
        this is the number that stops one project from starving the rest even when every container is behaving.
      </p>
      <PageState query={report} isEmpty={() => false} skeletonRows={3}>
        {(r) => <QuotaBody projectId={projectId} report={r} />}
      </PageState>
    </div>
  );
}

function QuotaBody({ projectId, report }: { projectId: string; report: QuotaReport }) {
  const qc = useQueryClient();
  const invalidate = () => void qc.invalidateQueries({ queryKey: getGetProjectQuotaQueryKey(projectId) });
  const capped = report.usage.some((u) => u.limit != null);

  const [memoryMB, setMemoryMB] = useState(() => limitToMB(report, "memory"));
  const [diskGB, setDiskGB] = useState(() => limitToGB(report, "disk"));
  const [previews, setPreviews] = useState(() => limitOf(report, "previews"));
  const [error, setError] = useState<string | null>(null);

  const save = useSetProjectQuota({
    mutation: {
      onSuccess: () => {
        invalidate();
        setError(null);
        toastSuccess("Quota saved");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the quota"),
    },
  });
  const remove = useDeleteProjectQuota({
    mutation: {
      onSuccess: () => {
        invalidate();
        setMemoryMB("");
        setDiskGB("");
        setPreviews("");
        toastSuccess("Quota removed — this project is uncapped again");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the quota", e, { retry: () => remove.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-4">
      <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
        {report.usage.map((u: QuotaUsage) => (
          <UsageRow key={u.dimension} usage={u} />
        ))}
      </ul>

      {/* Both honest admissions, on the page rather than only in the spec. */}
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

      <div className="space-y-4 rounded-lg border border-border bg-surface px-4 py-4">
        <p className="text-[13px] font-semibold text-text">Set the caps</p>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Memory" qualifier="· MiB">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={1}
                value={memoryMB}
                onChange={(e) => setMemoryMB(e.target.value)}
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
                value={diskGB}
                onChange={(e) => setDiskGB(e.target.value)}
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
                value={previews}
                onChange={(e) => setPreviews(e.target.value)}
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
          {capped && (
            <ConfirmDestructive
              trigger={
                <Button size="sm" variant="ghost" className="text-danger">
                  Remove the quota
                </Button>
              }
              title="Remove this project's quota?"
              blastRadius="Nothing will bound what this project consumes across the fleet. Individual container limits still apply."
              actionLabel="Remove quota"
              pendingLabel="Removing…"
              pending={remove.isPending}
              onConfirm={() => remove.mutate({ id: projectId })}
            />
          )}
          <ActionButton
            variant="secondary"
            size="sm"
            state={save.isPending ? "busy" : "idle"}
            busyLabel="Saving…"
            onClick={() =>
              save.mutate({
                id: projectId,
                data: {
                  memory_limit_bytes: memoryMB ? Number(memoryMB) * 1024 * 1024 : null,
                  disk_limit_bytes: diskGB ? Number(diskGB) * 1024 * 1024 * 1024 : null,
                  preview_limit: previews ? Number(previews) : null,
                },
              })
            }
          >
            Save caps
          </ActionButton>
        </div>
      </div>

      {/* The one thing an operator needs to know about what a cap does NOT
          block, because it is the difference between a guardrail and an
          outage. */}
      <p className="text-[11.5px] leading-[1.5] text-text-faint">
        Reaching a cap refuses new deploys here. It never refuses a rollback — recovery always works, because a
        guardrail that blocks recovery has become the outage it was installed to prevent.
      </p>
    </div>
  );
}

function UsageRow({ usage: u }: { usage: QuotaUsage }) {
  const copy = DIMENSION_COPY[u.dimension] ?? { label: u.dimension, note: "" };
  const pct = u.limit != null && u.limit > 0 ? Math.min(100, Math.round((u.used / u.limit) * 100)) : 0;
  const format = u.dimension === "previews" ? (n: number) => String(n) : formatBytes;

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
    </li>
  );
}

function limitOf(report: QuotaReport, dimension: string): string {
  const u = report.usage.find((x) => x.dimension === dimension);
  return u?.limit != null ? String(u.limit) : "";
}

function limitToMB(report: QuotaReport, dimension: string): string {
  const raw = limitOf(report, dimension);
  return raw ? String(Math.round(Number(raw) / 1024 / 1024)) : "";
}

function limitToGB(report: QuotaReport, dimension: string): string {
  const raw = limitOf(report, dimension);
  return raw ? String(Math.round(Number(raw) / 1024 / 1024 / 1024)) : "";
}
