// Application · Promote (revision-promotion.md, canvas 13ag).
//
// Ship the artifact that was TESTED, rather than rebuilding one that should be
// the same. The whole card is the plan, because the plan is what an operator
// needs to decide with — a confirmation dialog that says "are you sure" is not
// information, and this replaces one.
//
// The two things this screen refuses to do are as important as what it does:
//
//   IT NEVER COPIES AN ENVIRONMENT VARIABLE. Not optionally, not behind a
//   checkbox, not with a confirmation. Copying variables across environments is
//   the most effective way there is to point production at a staging database,
//   and a panel that offers it will eventually do it. The disagreement is shown
//   and the operator is sent to the target's own env screen, where setting one
//   is an ordinary, audited, sealed write.
//
//   IT DOES NOT PRETEND TO DETECT BAKED-IN CONFIG. If an application compiles
//   an API URL into its bundle, promoting moves staging's URL into production
//   and the deploy looks perfectly successful. Nothing in the panel can see
//   that, so the card says so rather than implying safety it cannot provide.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useListApplications } from "@/api/gen/applications/applications";
import {
  getListDeploymentsQueryKey,
  usePlanPromotion,
  usePromoteRevision,
} from "@/api/gen/deployments/deployments";
import { useListEnvironments } from "@/api/gen/projects/projects";
import type { Application, PromotionPlan } from "@/api/gen/model";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export function PromoteCard({
  app,
  projectId,
  revisionId,
}: {
  app: Application;
  projectId: string;
  revisionId: string | undefined;
}) {
  const [open, setOpen] = useState(false);
  // Nothing to promote until something has been built and observed serving.
  if (!revisionId) return null;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="secondary">
          Promote this build
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Promote to another environment"
        description="Ships the exact image this application is running. No rebuild, so what arrives is what was tested."
      >
        <PromoteBody app={app} projectId={projectId} revisionId={revisionId} onDone={() => setOpen(false)} />
      </DialogContent>
    </Dialog>
  );
}

function PromoteBody({
  app,
  projectId,
  revisionId,
  onDone,
}: {
  app: Application;
  projectId: string;
  revisionId: string;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [targetId, setTargetId] = useState("");
  const envs = useListEnvironments(projectId);

  const promote = usePromoteRevision({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListDeploymentsQueryKey(targetId) });
        onDone();
        toastSuccess({
          title: "Promoting",
          detail: "The image moves to the target's server and rolls out there. Nothing is rebuilt.",
        });
      },
      onError: (e: unknown, vars) => toastFailed("Could not promote", e, { retry: () => promote.mutate(vars) }),
    },
  });

  return (
    <div className="space-y-4">
      <Field label="Promote to" hint="Another environment's copy of this application, in the same project.">
        {(id) => (
          <select
            id={id}
            value={targetId}
            onChange={(e) => setTargetId(e.target.value)}
            className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
          >
            <option value="">Pick an application…</option>
            {(envs.data ?? []).map((env) => (
              <EnvOptions key={env.id} envId={env.id} envName={env.name} excludeAppId={app.id} />
            ))}
          </select>
        )}
      </Field>

      {targetId && <Plan revisionId={revisionId} targetId={targetId} />}

      <div className="flex justify-end gap-2">
        <DialogClose asChild>
          <Button type="button" variant="ghost" size="lg">
            Cancel
          </Button>
        </DialogClose>
        <PromoteButton
          revisionId={revisionId}
          targetId={targetId}
          pending={promote.isPending}
          onPromote={() => promote.mutate({ id: revisionId, data: { target_application_id: targetId } })}
        />
      </div>
    </div>
  );
}

/** One environment's applications as an option group. */
function EnvOptions({ envId, envName, excludeAppId }: { envId: string; envName: string; excludeAppId: string }) {
  const apps = useListApplications(envId);
  const list = (apps.data ?? []).filter((a) => a.id !== excludeAppId);
  if (list.length === 0) return null;
  return (
    <optgroup label={envName}>
      {list.map((a) => (
        <option key={a.id} value={a.id}>
          {a.name}
        </option>
      ))}
    </optgroup>
  );
}

function PromoteButton({
  revisionId,
  targetId,
  pending,
  onPromote,
}: {
  revisionId: string;
  targetId: string;
  pending: boolean;
  onPromote: () => void;
}) {
  const plan = usePlanPromotion(revisionId, { target_application_id: targetId }, { query: { enabled: targetId !== "" } });
  const blocked = (plan.data?.blockers.length ?? 0) > 0;
  return (
    <ActionButton
      variant="primary"
      size="lg"
      state={pending ? "busy" : "idle"}
      busyLabel="Promoting…"
      disabledReason={!targetId ? "Pick where it goes" : blocked ? plan.data?.blockers[0] : undefined}
      onClick={onPromote}
    >
      Promote
    </ActionButton>
  );
}

function Plan({ revisionId, targetId }: { revisionId: string; targetId: string }) {
  const plan = usePlanPromotion(revisionId, { target_application_id: targetId }, { query: { retry: false } });
  if (plan.isPending) {
    return <p className="text-[12.5px] text-text-faint">Working out what would change…</p>;
  }
  if (plan.isError || !plan.data) {
    return <p className="text-[12.5px] text-danger">Could not work out what would change.</p>;
  }
  const p: PromotionPlan = plan.data;

  return (
    <div className="space-y-3 rounded-md border border-border bg-pane px-3.5 py-3">
      <dl className="space-y-1 text-[12.5px]">
        <Row label="Image">
          <span className="mono text-[11.5px]">{p.image || "not built"}</span>
        </Row>
        <Row label="Commit">
          <span className="mono text-[11.5px]">{p.source_commit ? p.source_commit.slice(0, 12) : "—"}</span>
        </Row>
        <Row label="Replaces">
          <span className="mono text-[11.5px]">
            {p.target_revision_id ? p.target_revision_id.slice(-8) : "nothing yet"}
          </span>
        </Row>
        <Row label="Transfer">
          {p.same_server ? "already on that server" : "relayed to the target's server"}
        </Row>
      </dl>

      {/* Drift, shown and never acted on. */}
      {(p.only_in_source.length > 0 || p.only_in_target.length > 0) && (
        <div className="border-t border-pane-border pt-2.5">
          <p className="text-[11.5px] font-medium text-pane-text">Environment variables differ</p>
          {p.only_in_source.length > 0 && (
            <p className="mono mt-0.5 text-[11.5px] text-status-degraded-text">
              + {p.only_in_source.join(", ")} — only here
            </p>
          )}
          {p.only_in_target.length > 0 && (
            <p className="mono mt-0.5 text-[11.5px] text-text-faint">
              − {p.only_in_target.join(", ")} — only there
            </p>
          )}
          <p className="mt-1 text-[11.5px] leading-[1.5] text-text-faint">
            Nothing is copied. Set what the target needs on its own environment page — pointing production at a
            staging database is exactly what copying these would eventually do.
          </p>
        </div>
      )}

      {p.blockers.length > 0 && (
        <div className="border-t border-pane-border pt-2.5">
          {p.blockers.map((b) => (
            <p key={b} className="text-[12px] leading-[1.5] text-danger">
              {b}
            </p>
          ))}
        </div>
      )}

      {/* The cost with no fix inside the panel, said rather than implied away. */}
      <p className={cn("border-t border-pane-border pt-2.5 text-[11.5px] leading-[1.5] text-text-faint")}>{p.note}</p>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <dt className="shrink-0 text-text-faint">{label}</dt>
      <dd className="min-w-0 truncate text-right text-pane-text">{children}</dd>
    </div>
  );
}
