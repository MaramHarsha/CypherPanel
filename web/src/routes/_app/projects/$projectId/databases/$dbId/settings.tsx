// Database · Settings: the config, and the danger zone (typed-name delete,
// ui-principles §2). The backup count is stated in the blast radius so nobody
// deletes it blind.
//
// A config change is DESIRED STATE, not an instruction (ADR-005): the patch
// mints a new revision and the agent converges to it, which for a database
// means the container is recreated. That is a restart with a gap — a database
// has no second copy to health-gate behind, the way an application does — so
// the form says so before the click rather than letting an operator discover
// it as an outage.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { useListDatabaseBackups } from "@/api/gen/backups/backups";
import {
  getGetDatabaseQueryKey,
  getListDatabasesQueryKey,
  useDeleteDatabase,
  useGetDatabase,
  useUpdateDatabase,
} from "@/api/gen/databases/databases";
import type { Database } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/databases/$dbId/settings")({
  component: DatabaseSettings,
});

function DatabaseSettings() {
  const { projectId, dbId } = Route.useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const db = useGetDatabase(dbId);
  const schedules = useListDatabaseBackups(dbId);

  const del = useDeleteDatabase({
    mutation: {
      onSuccess: () => {
        // The project board we land on draws its rows from the cached
        // environment list, so a database deleted here would sit there looking
        // alive until something else asked for that list. The invalidation has
        // to happen before the navigation, not after it: the destination
        // renders from cache the moment it mounts.
        const envId = db.data?.environment_id;
        if (envId) void qc.invalidateQueries({ queryKey: getListDatabasesQueryKey(envId) });
        toastSuccess(`Deleted ${db.data?.name ?? "database"}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the database", e, { retry: () => del.mutate(vars) }),
    },
  });

  const scheduleCount = schedules.data?.length ?? 0;

  return (
    <PageState query={db} isEmpty={() => false}>
      {(d) => (
        <div className="max-w-xl space-y-6">
          <ConfigForm db={d} />
          <div className="space-y-2">
            <Eyebrow className="text-danger">Danger zone</Eyebrow>
            <div className="flex items-center justify-between gap-3 rounded-lg border border-danger/35 p-4">
              <div>
                <p className="text-[13px] font-medium text-text">Delete this database</p>
                <p className="text-xs text-text-mid">
                  Stops the container and deletes its data volume. Backup files in your S3 targets are kept.
                </p>
              </div>
              <ConfirmDestructive
                trigger={<Button variant="danger">Delete</Button>}
                title={`Delete ${d.name}?`}
                // One entry per class of thing, each carrying its own
                // consequence: a run-on sentence about a deletion is the one
                // paragraph an operator skims (canvas 13af).
                blastRadius={[
                  `the ${d.engine} container and its data volume — permanently (backup files already in S3 survive)`,
                  ...(scheduleCount > 0
                    ? [`${scheduleCount} backup schedule${scheduleCount > 1 ? "s" : ""} — nothing further is uploaded`]
                    : []),
                ]}
                confirmName={d.name}
                actionLabel="Delete database"
                pending={del.isPending}
                onConfirm={() => del.mutate({ id: dbId })}
              />
            </div>
          </div>
        </div>
      )}
    </PageState>
  );
}

/**
 * The database's own config. Everything here becomes a new revision when it
 * changes, and the agent recreates the container to reach it — so the fields
 * are grouped by whether that is a surprise:
 *
 *   · Name is a label and changes nothing about the container.
 *   · Version, limits and the exposed port change what runs, which means a
 *     recreate. The note says so once, under the group, rather than four times.
 *
 * The root credential is not here: it is reset from the Overview, once, and
 * shown once — a field that could be typed into would imply the panel can read
 * back what it sealed.
 */
function ConfigForm({ db }: { db: Database }) {
  const qc = useQueryClient();
  const [name, setName] = useState(db.name);
  const [version, setVersion] = useState(db.version);
  const [cpu, setCpu] = useState(db.cpu_limit ? String(db.cpu_limit) : "");
  const [memory, setMemory] = useState(db.memory_limit_mb ? String(db.memory_limit_mb) : "");
  const [port, setPort] = useState(db.expose_port ? String(db.expose_port) : "");
  const [error, setError] = useState<string | null>(null);

  const update = useUpdateDatabase({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetDatabaseQueryKey(db.id) });
        void qc.invalidateQueries({ queryKey: getListDatabasesQueryKey(db.environment_id) });
        setError(null);
        toastSuccess({
          title: "Saved",
          detail: "The agent converges the container to the new revision; it restarts to get there.",
        });
      },
      onError: (e: unknown) => {
        setError(e instanceof Error ? e.message : "Could not save the database");
        toastFailed("Could not save the database", e);
      },
    },
  });
  const state = useMutationActionState(update);

  const num = (v: string) => (v.trim() === "" ? 0 : Number(v));
  const dirty =
    name !== db.name ||
    version !== db.version ||
    num(cpu) !== (db.cpu_limit ?? 0) ||
    num(memory) !== (db.memory_limit_mb ?? 0) ||
    num(port) !== (db.expose_port ?? 0);
  // Only the fields that actually moved, so an untouched database is never
  // handed a revision it did not need.
  const recreates =
    version !== db.version ||
    num(cpu) !== (db.cpu_limit ?? 0) ||
    num(memory) !== (db.memory_limit_mb ?? 0) ||
    num(port) !== (db.expose_port ?? 0);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    update.mutate({
      id: db.id,
      data: {
        ...(name !== db.name ? { name: name.trim() } : {}),
        ...(version !== db.version ? { version: version.trim() } : {}),
        ...(num(cpu) !== (db.cpu_limit ?? 0) ? { cpu_limit: num(cpu) } : {}),
        ...(num(memory) !== (db.memory_limit_mb ?? 0) ? { memory_limit_mb: num(memory) } : {}),
        ...(num(port) !== (db.expose_port ?? 0) ? { expose_port: num(port) } : {}),
      },
    });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <Eyebrow>Configuration</Eyebrow>

      <Field label="Name" error={error ?? undefined}>
        {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} className="font-sans" />}
      </Field>

      <Field label="Version" qualifier={`· the ${db.engine} image tag`}>
        {(id) => <Input id={id} required value={version} onChange={(e) => setVersion(e.target.value)} />}
      </Field>

      <div className="grid grid-cols-2 gap-3">
        <Field label="CPU limit" qualifier="· cores, empty for none">
          {(id) => (
            <Input
              id={id}
              inputMode="decimal"
              value={cpu}
              onChange={(e) => setCpu(e.target.value.replace(/[^\d.]/g, ""))}
              placeholder="unlimited"
            />
          )}
        </Field>
        <Field label="Memory limit" qualifier="· MB, empty for none">
          {(id) => (
            <Input
              id={id}
              inputMode="numeric"
              value={memory}
              onChange={(e) => setMemory(e.target.value.replace(/\D/g, ""))}
              placeholder="unlimited"
            />
          )}
        </Field>
      </div>

      {/* Publishing a port is the one field with a security consequence, so it
          carries it rather than leaving it to be inferred from a number. */}
      <Field
        label="Exposed port"
        qualifier="· empty keeps it private"
        hint="A published port is reachable by anything that can reach the server, not only by this project's applications — which reach it over the internal network either way."
      >
        {(id, describedBy) => (
          <Input
            id={id}
            inputMode="numeric"
            aria-describedby={describedBy}
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))}
            placeholder="private"
          />
        )}
      </Field>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="min-w-0 flex-1 text-[12px] leading-[1.5] text-text-faint">
          {recreates
            ? "Saving mints a new revision and the container is recreated to reach it — a short outage, since a database has no second copy to health-gate behind. The data volume is untouched."
            : "A name change is a label — nothing on the server moves."}
        </p>
        <ActionButton
          type="submit"
          variant="primary"
          state={state}
          busyLabel="Saving…"
          successLabel="Saved"
          disabledReason={!dirty ? "Nothing has changed" : undefined}
        >
          Save
        </ActionButton>
      </div>
    </form>
  );
}
