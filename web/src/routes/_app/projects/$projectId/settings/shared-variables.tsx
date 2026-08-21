// Project settings · Shared variables (canvas 13h, shared-variables.md §8):
// set once, reference everywhere.
//
// The screen is a list of rows and almost nothing else, on purpose. Three
// decisions carry it:
//
//   · SCOPE IS A WORD, NOT A CONTROL. Each row reads `project` or the
//     environment's name, because scope is a property of the variable, not of
//     the reference — an app writes `{{shared.KEY}}` and never says where the
//     value came from. That is also why scope is fixed at create: changing it
//     would silently re-point every referencing application.
//   · THE MASK IS FIXED, WITH NO REVEAL. Unlike a notifier's config hint, a
//     shared variable carries no masked summary at all — it is already named by
//     its key, so a hint would be partial disclosure for nothing (§6). `•••••`
//     is a constant, not a redaction of anything.
//   · REACH IS SHOWN BEFORE IT IS SPENT. `used by N apps` is the one number
//     that makes the blast radius of a shared value legible, and the delete
//     confirmation enumerates exactly which applications stand in the way.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { toast } from "sonner";
import { useGetProject, useListEnvironments } from "@/api/gen/projects/projects";
import {
  getListSharedVariableUsageQueryKey,
  getListSharedVariablesQueryKey,
  useCreateSharedVariable,
  useDeleteSharedVariable,
  useListSharedVariableUsage,
  useListSharedVariables,
  useUpdateSharedVariable,
} from "@/api/gen/shared-variables/shared-variables";
import type { SharedVariable } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { InlineHint } from "@/components/inline-hint";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { absoluteTime, relativeTime } from "@/lib/time";

export const Route = createFileRoute("/_app/projects/$projectId/settings/shared-variables")({
  component: SharedVariablesTab,
});

/** The mask is a constant, not a redaction: no value ever reaches the client. */
const MASK = "•••••";

function SharedVariablesTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const variables = useListSharedVariables(projectId);

  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings", to: `/projects/${projectId}/settings` },
    { label: "shared variables" },
  ]);

  return (
    <div className="max-w-2xl space-y-2">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Shared variables</Eyebrow>
        <NewVariableDialog projectId={projectId} />
      </div>
      <InlineHint>
        Defined once per project or environment, referenced from any app's env vars as{" "}
        <code className="mono text-[11.5px] text-text">{"{{shared.KEY}}"}</code>. Values sealed and write-only, like
        everything else.
      </InlineHint>

      <PageState
        query={variables}
        skeletonColumns="1fr auto auto auto"
        empty={
          <EmptyState
            title="No shared variables"
            hint="A value six apps need — an API key, an SMTP host — belongs here once instead of six times."
            action={<NewVariableDialog projectId={projectId} primary />}
          />
        }
      >
        {(list) => (
          <>
            <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-surface">
              {list.map((v) => (
                <VariableRow key={v.id} projectId={projectId} variable={v} />
              ))}
            </ul>
            <p className="pt-1 text-xs leading-relaxed text-text-faint">
              Changing one marks every referencing app &ldquo;redeploy to apply&rdquo; — nothing is redeployed for you.
            </p>
          </>
        )}
      </PageState>
    </div>
  );
}

function VariableRow({ projectId, variable: v }: { projectId: string; variable: SharedVariable }) {
  const qc = useQueryClient();
  const [replacing, setReplacing] = useState(false);
  const [value, setValue] = useState("");

  // Nothing about shared variables rides the SSE stream, so both the list and
  // this variable's usage have to be invalidated by hand or the count goes
  // stale until a reload.
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: getListSharedVariablesQueryKey(projectId) });
    void qc.invalidateQueries({ queryKey: getListSharedVariableUsageQueryKey(v.id) });
  };

  const update = useUpdateSharedVariable({
    mutation: {
      onSuccess: () => {
        invalidate();
        setReplacing(false);
        setValue("");
        toast.success(
          v.used_by_count === 0
            ? "Value replaced"
            : `Value replaced — ${v.used_by_count} app${v.used_by_count === 1 ? "" : "s"} now need a redeploy`,
        );
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not replace the value"),
    },
  });

  const del = useDeleteSharedVariable({
    mutation: {
      onSuccess: () => {
        invalidate();
        toast.success(`${v.key} deleted`);
      },
      onError: (e: unknown) =>
        toast.error(e instanceof Error ? e.message : "Could not delete the shared variable"),
    },
  });

  return (
    <li className="flex flex-col gap-2 px-3 py-2.5 sm:grid sm:grid-cols-[minmax(0,1fr)_auto_auto_auto] sm:items-center sm:gap-3">
      <span className="mono min-w-0 truncate text-[13px] text-text" title={v.key}>
        {v.key}
      </span>
      <span
        className="mono text-[11px] text-text-faint sm:justify-self-end"
        title={
          v.environment_id
            ? `Scoped to ${v.environment_name} — shadows any project-scoped variable of the same key`
            : "Applies to every environment in this project"
        }
      >
        {v.environment_id ? v.environment_name : "project"}
      </span>

      {replacing ? (
        <span className="flex items-center gap-1.5 sm:col-span-2 sm:justify-self-end">
          <Input
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder="new value"
            className="mono h-7 w-44"
            aria-label={`New value for ${v.key}`}
            autoComplete="off"
            autoFocus
          />
          {/* An empty box is nothing to save, not an instruction to blank the
              variable: the value is write-only, so a blank Replace destroys a
              credential nobody can read back or recover. Deliberately emptying
              one is an API call. */}
          <Button
            size="sm"
            variant="primary"
            disabled={update.isPending || value === ""}
            title={value === "" ? "Type the new value first" : undefined}
            onClick={() => update.mutate({ id: v.id, data: { value } })}
          >
            Save
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setReplacing(false)}>
            Cancel
          </Button>
        </span>
      ) : (
        <>
          <span className="mono text-xs text-text-faint sm:justify-self-end">{MASK}</span>
          <span className="flex items-center gap-1.5 sm:justify-self-end">
            <UsedByCount count={v.used_by_count} variableId={v.id} />
            <Button size="sm" variant="ghost" onClick={() => setReplacing(true)}>
              Replace
            </Button>
            <DeleteVariable variable={v} pending={del.isPending} onConfirm={() => del.mutate({ id: v.id })} />
          </span>
        </>
      )}

      <span className="mono text-[11px] text-text-faint sm:col-span-4 sm:justify-self-start" title={absoluteTime(v.updated_at)}>
        changed {relativeTime(v.updated_at)}
      </span>
    </li>
  );
}

/** The reach of a shared value, and — on click — exactly whose. */
function UsedByCount({ count, variableId }: { count: number; variableId: string }) {
  if (count === 0) {
    return <span className="mono whitespace-nowrap text-[11px] text-text-faint">used by no apps</span>;
  }
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button size="sm" variant="ghost" className="mono text-[11px] text-text-mid">
          used by {count} app{count === 1 ? "" : "s"}
        </Button>
      </DialogTrigger>
      <DialogContent title="Where this value goes" description="Every application whose env vars reference this key.">
        <UsedByList variableId={variableId} />
      </DialogContent>
    </Dialog>
  );
}

function UsedByList({ variableId }: { variableId: string }) {
  const usage = useListSharedVariableUsage(variableId);
  return (
    <PageState
      query={usage}
      skeletonRows={2}
      empty={
        <EmptyState
          title="Nothing references this yet"
          hint="Add {{shared.KEY}} to an application's env vars and it will appear here."
        />
      }
    >
      {(rows) => (
        <ul className="divide-y divide-border-subtle">
          {rows.map((u) => (
            <li key={u.application_id} className="flex items-center justify-between gap-3 py-2">
              <span className="min-w-0 truncate text-[13px] text-text">{u.application_name}</span>
              <span className="flex shrink-0 items-center gap-2">
                <span className="mono text-[11px] text-text-faint">{u.environment_name}</span>
                {u.redeploy_pending && (
                  <span className="mono rounded border border-status-degraded/40 bg-status-degraded/10 px-1.5 py-px text-[10.5px] text-status-degraded">
                    redeploy to apply
                  </span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </PageState>
  );
}

/** The delete confirmation names the applications that stand in the way, because
 *  the server refuses while any of them still references the key and there is no
 *  force override (ui-principles §2, spec §7). */
function DeleteVariable({
  variable: v,
  pending,
  onConfirm,
}: {
  variable: SharedVariable;
  pending: boolean;
  onConfirm: () => void;
}) {
  const usage = useListSharedVariableUsage(v.id, {
    // Only asked for once the operator is actually looking at the confirmation
    // is tempting, but the blast radius has to be right the moment the dialog
    // opens — and this list is at most a handful of rows.
    query: { enabled: v.used_by_count > 0 },
  });

  const blastRadius =
    v.used_by_count === 0
      ? [
          `Removes ${v.key} from ${v.environment_id ? `the ${v.environment_name} environment` : "this project"}.`,
          "Nothing references it, so no application changes.",
        ]
      : [
          `${v.used_by_count} application${v.used_by_count === 1 ? "" : "s"} still reference {{shared.${v.key}}}${
            usage.data ? `: ${usage.data.map((u) => u.application_name).join(", ")}` : ""
          }.`,
          "The delete is refused until those references are removed — remove them first, then try again.",
        ];

  return (
    <ConfirmDestructive
      trigger={
        <Button size="sm" variant="ghost" aria-label={`Delete ${v.key}`} className="text-danger">
          ✕
        </Button>
      }
      title={`Delete ${v.key}?`}
      lead="Deleting a shared variable is not reversible — the sealed value cannot be read back to re-create it."
      blastRadius={blastRadius}
      actionLabel="Delete variable"
      pending={pending}
      onConfirm={onConfirm}
    />
  );
}

function NewVariableDialog({ projectId, primary }: { projectId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const environments = useListEnvironments(projectId);
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState("");
  const [scope, setScope] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = useCreateSharedVariable({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListSharedVariablesQueryKey(projectId) });
        setOpen(false);
        toast.success(`${key} added — reference it as {{shared.${key}}}`);
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not add the shared variable"),
    },
  });

  const reset = () => {
    setKey("");
    setScope("");
    setValue("");
    setError(null);
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: projectId,
      data: { key: key.trim(), value, ...(scope === "" ? {} : { environment_id: scope }) },
    });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant={primary ? "primary" : "secondary"}>
          <Plus className="h-3.5 w-3.5" /> Add
        </Button>
      </DialogTrigger>
      <DialogContent size="form" title="Add a shared variable">
        <form onSubmit={submit} className="space-y-3">
          <Field
            label="Key"
            qualifier="· how apps refer to it"
            hint={key.trim() === "" ? "Letters, digits and underscores." : `Apps will write {{shared.${key.trim()}}}`}
          >
            {(id) => (
              <Input
                id={id}
                value={key}
                onChange={(e) => setKey(e.target.value.toUpperCase())}
                placeholder="SENTRY_DSN"
                className="mono"
                autoComplete="off"
                spellCheck={false}
                required
                autoFocus
              />
            )}
          </Field>

          <Field
            label="Scope"
            qualifier="· where it applies"
            hint="An environment-scoped value wins over a project-scoped one with the same key, so production can differ without any app being edited."
          >
            {(id) => (
              <Select id={id} value={scope} onChange={(e) => setScope(e.target.value)} className="font-sans">
                <option value="">Whole project</option>
                {(environments.data ?? []).map((env) => (
                  <option key={env.id} value={env.id}>
                    {env.name} only
                  </option>
                ))}
              </Select>
            )}
          </Field>

          <Field label="Value" qualifier="· sealed, write-only" hint="Stored encrypted. It is never shown again — replace it to change it.">
            {(id) => (
              <Input
                id={id}
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                className="mono"
                autoComplete="off"
                required
              />
            )}
          </Field>

          {error && (
            <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
              {error}
            </p>
          )}

          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost" type="button">
                Cancel
              </Button>
            </DialogClose>
            <Button variant="primary" type="submit" disabled={create.isPending || key.trim() === ""}>
              {create.isPending ? "Adding…" : "Add variable"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
