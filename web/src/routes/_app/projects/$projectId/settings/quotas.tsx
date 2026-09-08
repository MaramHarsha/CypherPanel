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
// The meter and the caps form live in components/quota-meter.tsx, because the
// team card in Settings → Teams is the same instrument one scope up and two
// copies would drift at the thresholds first.
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
import type { QuotaReport } from "@/api/gen/model";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import {
  capsToRequest,
  limitOf,
  limitToGB,
  limitToMB,
  QuotaCapsForm,
  QuotaCaveats,
  QuotaEnforcementNote,
  QuotaMeter,
  type QuotaCaps,
} from "@/components/quota-meter";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/settings/quotas")({ component: QuotasTab });

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

  const [caps, setCaps] = useState<QuotaCaps>(() => ({
    memoryMB: limitToMB(report, "memory"),
    diskGB: limitToGB(report, "disk"),
    previews: limitOf(report, "previews"),
  }));
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
        setCaps({ memoryMB: "", diskGB: "", previews: "" });
        toastSuccess("Quota removed — this project is uncapped again");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the quota", e, { retry: () => remove.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-4">
      <QuotaMeter report={report} />
      {/* Both honest admissions, on the page rather than only in the spec. */}
      <QuotaCaveats report={report} />
      <QuotaCapsForm
        caps={caps}
        onChange={setCaps}
        onSave={() => save.mutate({ id: projectId, data: capsToRequest(caps) })}
        onRemove={() => remove.mutate({ id: projectId })}
        saving={save.isPending}
        removing={remove.isPending}
        capped={capped}
        error={error}
        scopeNoun="this project"
      />
      {/* The one thing an operator needs to know about what a cap does NOT
          block, because it is the difference between a guardrail and an
          outage. */}
      <QuotaEnforcementNote />
    </div>
  );
}
