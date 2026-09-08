// A team's capacity, on the team card (resource-quotas.md §10).
//
// It lives beside the members, the invitations and the access requests for the
// reason those two already record: they are all facts about this team, and a
// separate page per fact is a place nobody visits. A team quota is read by any
// member — a member whose deploy was refused must be able to see why, and a 409
// nobody can explain by opening a page is a dead end (ui-principles §11).
//
// SETTING one is PANEL admin, not team admin, and that asymmetry is the point
// of the feature (§3): a cap the capped team can raise guards nothing against
// the team, which is the only thing it exists to guard against. So a team admin
// sees the numbers and a disabled button that NAMES the rank, rather than a
// button that 403s.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import type { QuotaReport, Team } from "@/api/gen/model";
import { getGetTeamQuotaQueryKey, useDeleteTeamQuota, useGetTeamQuota, useSetTeamQuota } from "@/api/gen/teams/teams";
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
import { atLeast, type Role } from "@/lib/roles";
import { toastFailed, toastSuccess } from "@/lib/toast";

export function TeamQuota({ team }: { team: Team }) {
  const report = useGetTeamQuota(team.id, { query: { retry: false } });
  return (
    <div className="space-y-2.5 border-t border-border p-3">
      <h3 className="eyebrow">Capacity</h3>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        A cap on what this whole team may consume across the fleet. Its projects may each have their own; both apply,
        and the tighter one refuses.
      </p>
      <PageState query={report} isEmpty={() => false} skeletonRows={3}>
        {(r) => <QuotaBody teamId={team.id} report={r} />}
      </PageState>
    </div>
  );
}

function QuotaBody({ teamId, report }: { teamId: string; report: QuotaReport }) {
  const qc = useQueryClient();
  const me = useGetMe();
  const canSet = atLeast(me.data?.role as Role | undefined, "admin");
  const invalidate = () => void qc.invalidateQueries({ queryKey: getGetTeamQuotaQueryKey(teamId) });
  const capped = report.usage.some((u) => u.limit != null);

  const [caps, setCaps] = useState<QuotaCaps>(() => ({
    memoryMB: limitToMB(report, "memory"),
    diskGB: limitToGB(report, "disk"),
    previews: limitOf(report, "previews"),
  }));
  const [error, setError] = useState<string | null>(null);

  const save = useSetTeamQuota({
    mutation: {
      onSuccess: () => {
        invalidate();
        setError(null);
        toastSuccess("Quota saved");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save the quota"),
    },
  });
  const remove = useDeleteTeamQuota({
    mutation: {
      onSuccess: () => {
        invalidate();
        setCaps({ memoryMB: "", diskGB: "", previews: "" });
        toastSuccess("Quota removed — this team is uncapped again");
      },
      onError: (e: unknown, vars) => toastFailed("Could not remove the quota", e, { retry: () => remove.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-3">
      <QuotaMeter report={report} />
      <QuotaCaveats report={report} />
      <QuotaCapsForm
        caps={caps}
        onChange={setCaps}
        onSave={() => save.mutate({ id: teamId, data: capsToRequest(caps) })}
        onRemove={() => remove.mutate({ id: teamId })}
        saving={save.isPending}
        removing={remove.isPending}
        capped={capped}
        error={error}
        scopeNoun="this team"
        disabledReason={canSet ? undefined : "Capping a team is a panel admin — a cap this team could raise itself would guard nothing"}
      />
      <QuotaEnforcementNote />
    </div>
  );
}
