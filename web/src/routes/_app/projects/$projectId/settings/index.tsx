// Project settings · General (canvas 12c): the project's own facts, its
// environments, and the one destructive act that belongs to the project rather
// than to anything in it.
//
// The board draws this tab as a form — Name, Slug, Team, Default environment,
// an ink Save — and it is that form now. It was read-only facts for as long as
// the API was GET and DELETE only; `PATCH /projects/{id}` exists, so the
// deferral is over.
//
// Three of the four fields behave differently and the form says so rather than
// presenting four identical boxes:
//
//   · Name is a rename, team admin.
//   · Slug is immutable by design — it is what a bookmark and a script hold, so
//     renaming must not move it. It is shown, and it is not a field.
//   · Team is a TRANSFER: it changes who can see everything inside, so it needs
//     ownership of both teams and an interactive session, and it is not folded
//     in with the rename.
//   · Default environment is where "open this project" lands.
//
// The danger zone is where the board and the backend disagree most, and the
// backend wins. 12c's card says the confirm will list "3 apps, 1 database, 2
// previews" — a cascade. handleDeleteProject refuses with 409 while any
// application or database remains, because a managed database is torn down by
// a two-phase flow keyed on its own row and cascading it away would leave the
// container and its data volume running with nothing that knows they exist.
// So the card counts what is inside, names it, links to it, and only offers
// the typed-name confirm once the project is empty.
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { ApiError } from "@/api/client";
import { getListApplicationsQueryOptions } from "@/api/gen/applications/applications";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListDatabasesQueryOptions } from "@/api/gen/databases/databases";
import {
  getGetProjectQueryKey,
  getListProjectsQueryKey,
  useCreateEnvironment,
  useDeleteEnvironment,
  useDeleteProject,
  useGetProject,
  usePatchEnvironment,
  usePatchProject,
} from "@/api/gen/projects/projects";
import type { Environment, ProjectDetail } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { Fact, FactCard } from "@/components/fact-card";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { Skeleton, SkeletonForm, useSkeletonDelay } from "@/components/ui/skeleton";
import { useCrumbs } from "@/lib/crumbs";
import { atLeast, type Role } from "@/lib/roles";
import { absoluteTime, relativeTime } from "@/lib/time";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/settings/")({ component: GeneralTab });

function GeneralTab() {
  const { projectId } = Route.useParams();
  const project = useGetProject(projectId);
  const me = useGetMe();

  // Canvas 12c datelines this page from the project, not from the projects
  // list: the trail names where you are inside the project, and PROJECTS is
  // already one click away in the top bar.
  useCrumbs([
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: "settings" },
  ]);

  // Deleting needs admin on the project's team; a panel owner has it
  // everywhere (teams.RoleForProject). Resolved here so the pill can say so
  // instead of letting the server's 403 explain it afterwards.
  const team = me.data?.teams.find((t) => t.id === project.data?.project.team_id);
  const role: Role | undefined = me.data?.role === "owner" ? "owner" : team?.role;

  // The single-resource skeleton (10e), behind the same 200 ms gate every list
  // gets — PageState paints a custom `loading` immediately, so the gate is
  // applied here.
  const showSkeleton = useSkeletonDelay(project.isPending);

  return (
    <div className="max-w-2xl">
      <PageState query={project} loading={showSkeleton ? <SkeletonForm fields={4} columns={1} /> : null}>
        {(detail) => (
          <>
            <ProjectForm
              detail={detail}
              teamName={team?.name}
              canEdit={atLeast(role, "admin")}
              canTransfer={role === "owner"}
              ownedTeams={(me.data?.teams ?? []).filter((t) => t.role === "owner")}
            />
            <Environments projectId={projectId} detail={detail} canEdit={atLeast(role, "admin")} />
            <DangerZone projectId={projectId} detail={detail} canDelete={atLeast(role, "admin")} />
          </>
        )}
      </PageState>
    </div>
  );
}

function ProjectForm({
  detail,
  teamName,
  canEdit,
  canTransfer,
  ownedTeams,
}: {
  detail: ProjectDetail;
  teamName: string | undefined;
  canEdit: boolean;
  /** A transfer needs owner of BOTH teams, so the destination list is the
   *  teams this account owns — a member of the destination cannot receive one. */
  canTransfer: boolean;
  ownedTeams: { id: string; name: string; role?: string }[];
}) {
  const qc = useQueryClient();
  const p = detail.project;
  const envs = detail.environments;
  const [name, setName] = useState(p.name);
  const [defaultEnv, setDefaultEnv] = useState(p.default_environment_id ?? "");
  const [error, setError] = useState<string | null>(null);

  const patch = usePatchProject({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(p.id) });
        void qc.invalidateQueries({ queryKey: getListProjectsQueryKey() });
        setError(null);
        toastSuccess("Project updated");
      },
      onError: (e: unknown) => {
        setError(e instanceof Error ? e.message : "Could not update the project");
        toastFailed("Could not update the project", e);
      },
    },
  });
  const state = useMutationActionState(patch);

  const dirty = name.trim() !== p.name || defaultEnv !== (p.default_environment_id ?? "");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    patch.mutate({
      id: p.id,
      data: {
        ...(name.trim() !== p.name ? { name: name.trim() } : {}),
        ...(defaultEnv !== (p.default_environment_id ?? "") ? { default_environment_id: defaultEnv } : {}),
      },
    });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <Eyebrow>Project</Eyebrow>
      <fieldset disabled={!canEdit || patch.isPending} className="space-y-4">
        <Field label="Name" error={error ?? undefined}>
          {(id) => <Input id={id} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />}
        </Field>
        <Field
          label="Default environment"
          qualifier="· where “open this project” lands"
        >
          {(id) => (
            <Select id={id} value={defaultEnv} onChange={(e) => setDefaultEnv(e.target.value)}>
              <option value="">first environment</option>
              {envs.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <div className="flex justify-end">
          <ActionButton
            type="submit"
            variant="primary"
            state={state}
            busyLabel="Saving…"
            successLabel="Saved"
            disabledReason={
              !canEdit ? "Editing a project needs admin on its team" : !dirty ? "Nothing has changed" : undefined
            }
          >
            Save
          </ActionButton>
        </div>
      </fieldset>

      {/* Read-only, and the reason is the point: a slug is what a bookmark and
          a script hold, so a rename must not move it. Showing it as a disabled
          field would say "not yet"; showing it as a fact says "never". */}
      <FactCard title="Fixed">
        <Fact label="Slug">{p.slug ?? "—"}</Fact>
        <Fact label="Team">{teamName ?? p.team_id}</Fact>
        <Fact label="Created">
          <span title={absoluteTime(p.created_at)}>{relativeTime(p.created_at)}</span>
        </Fact>
        <Fact label="ID">{p.id}</Fact>
      </FactCard>
      <p className="text-xs leading-relaxed text-text-faint">
        The slug is chosen once, from the name, so URLs and scripts keep working after a rename.
      </p>

      <TransferProject
        projectId={p.id}
        projectName={p.name}
        currentTeamId={p.team_id}
        canTransfer={canTransfer}
        ownedTeams={ownedTeams}
      />
    </form>
  );
}

/**
 * A transfer is not a rename with a different field. It changes who can see
 * everything inside the project, so the API asks for ownership of BOTH teams
 * and an interactive session — a leaked API token must not be able to hand a
 * project to a team the attacker controls. The dialog states that rather than
 * letting a 403 explain it afterwards, and it is a separate gesture from Save
 * for the same reason a delete is.
 */
function TransferProject({
  projectId,
  projectName,
  currentTeamId,
  canTransfer,
  ownedTeams,
}: {
  projectId: string;
  projectName: string;
  currentTeamId: string;
  canTransfer: boolean;
  ownedTeams: { id: string; name: string }[];
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const targets = ownedTeams.filter((t) => t.id !== currentTeamId);
  const [teamId, setTeamId] = useState("");
  const [error, setError] = useState<string | null>(null);

  const patch = usePatchProject({
    mutation: {
      onSuccess: () => {
        // Everything scoped by team is now scoped elsewhere — including the
        // sidebar's current team and the project list this page came from.
        void qc.invalidateQueries();
        toastSuccess(`${projectName} moved`);
        setOpen(false);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not transfer the project"),
    },
  });

  const reason = !canTransfer
    ? "Transferring needs you to own both teams"
    : targets.length === 0
      ? "You own no other team to move it to"
      : undefined;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setError(null);
      }}
    >
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-surface px-4 py-3.5">
        <div className="min-w-0">
          <p className="text-[13px] font-semibold text-text">Transfer to another team</p>
          <p className="mt-0.5 text-[12px] leading-[1.5] text-text-mid">
            Everyone on the new team can see everything inside; everyone on this one stops being able to.
          </p>
        </div>
        <DialogTrigger asChild>
          <Button type="button" variant="secondary" disabledReason={reason}>
            Transfer
          </Button>
        </DialogTrigger>
      </div>
      <DialogContent
        title={`Transfer ${projectName}?`}
        description="The project, its environments and everything in them move with it. Its servers do not — a resource whose server belongs to the old team keeps running, and its next deploy is refused."
      >
        <div className="space-y-4">
          <Field label="Destination team" error={error ?? undefined}>
            {(id) => (
              <Select id={id} value={teamId} onChange={(e) => setTeamId(e.target.value)}>
                <option value="">choose a team…</option>
                {targets.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              variant="danger"
              size="lg"
              state={patch.isPending ? "busy" : "idle"}
              busyLabel="Transferring…"
              disabledReason={teamId === "" ? "Choose a team first" : undefined}
              onClick={() => {
                setError(null);
                patch.mutate({ id: projectId, data: { team_id: teamId } });
              }}
            >
              Transfer project
            </ActionButton>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * The environments, as a list you can add to and take from. They were tabs on
 * the board and nothing else, which meant a project was stuck with whatever it
 * was created with.
 *
 * A PREVIEW environment is in the list and is not editable: it belongs to its
 * pull request, and renaming or deleting one by hand desynchronises it from the
 * PR that made it. Saying that on the row is better than a 409 after the click.
 */
function Environments({
  projectId,
  detail,
  canEdit,
}: {
  projectId: string;
  detail: ProjectDetail;
  canEdit: boolean;
}) {
  const envs = detail.environments;
  const standing = envs.filter((e) => e.kind !== "preview");
  return (
    <section className="mt-6 space-y-2.5">
      <div className="flex items-center justify-between gap-3">
        <Eyebrow>Environments — {envs.length}</Eyebrow>
        <NewEnvironmentDialog projectId={projectId} canEdit={canEdit} />
      </div>
      <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
        {envs.map((e) => (
          <EnvironmentRow
            key={e.id}
            projectId={projectId}
            env={e}
            canEdit={canEdit}
            // The last standing environment cannot go: a project with none has
            // nowhere to put anything.
            isLastStanding={e.kind !== "preview" && standing.length === 1}
          />
        ))}
      </ul>
    </section>
  );
}

function EnvironmentRow({
  projectId,
  env,
  canEdit,
  isLastStanding,
}: {
  projectId: string;
  env: Environment;
  canEdit: boolean;
  isLastStanding: boolean;
}) {
  const qc = useQueryClient();
  const preview = env.kind === "preview";
  const [renaming, setRenaming] = useState(false);
  const [name, setName] = useState(env.name);
  const [blocked, setBlocked] = useState<string | null>(null);

  const refresh = () => void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(projectId) });

  const rename = usePatchEnvironment({
    mutation: {
      onSuccess: () => {
        refresh();
        setRenaming(false);
        toastSuccess("Environment renamed");
      },
      onError: (e: unknown) => toastFailed("Could not rename the environment", e),
    },
  });
  const del = useDeleteEnvironment({
    mutation: {
      onSuccess: () => {
        refresh();
        toastSuccess(`Deleted ${env.name}`);
      },
      // The refusal is an instruction — "delete what is inside first" — so it
      // stays on the row rather than flying past in a toast.
      onError: (e: unknown) => {
        if (e instanceof ApiError && e.status === 409) {
          setBlocked(e.message);
          return;
        }
        toastFailed("Could not delete the environment", e);
      },
    },
  });

  const deleteReason = !canEdit
    ? "Deleting an environment needs admin on the team"
    : preview
      ? "A preview belongs to its pull request"
      : isLastStanding
        ? "A project needs at least one environment"
        : undefined;

  return (
    <li className="px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        {renaming ? (
          <>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
              autoFocus
              aria-label={`New name for ${env.name}`}
              className="h-8 max-w-[220px]"
            />
            <Button
              size="sm"
              variant="primary"
              disabled={rename.isPending || name.trim() === ""}
              onClick={() => rename.mutate({ id: env.id, data: { name: name.trim() } })}
            >
              Save
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setRenaming(false);
                setName(env.name);
              }}
            >
              Cancel
            </Button>
          </>
        ) : (
          <>
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px] font-semibold text-text">{env.name}</span>
              <span className="mono mt-0.5 block truncate text-[11.5px] text-text-faint">
                {preview ? "preview · owned by its pull request" : "standing"}
                {env.is_default ? " · default" : ""}
                {" · created "}
                {relativeTime(env.created_at)}
              </span>
            </span>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setRenaming(true)}
              disabledReason={
                !canEdit
                  ? "Renaming an environment needs admin on the team"
                  : preview
                    ? "A preview belongs to its pull request"
                    : undefined
              }
            >
              Rename
            </Button>
            <ConfirmDestructive
              trigger={
                <Button size="sm" variant="ghost" className="text-danger" disabledReason={deleteReason}>
                  Delete
                </Button>
              }
              title={`Delete ${env.name}?`}
              blastRadius={[
                "The environment and its shared configuration.",
                "Refused while any application or database is still inside — delete those first, so their containers and volumes are torn down properly.",
              ]}
              confirmName={env.name}
              actionLabel="Delete environment"
              pending={del.isPending}
              pendingLabel="Deleting…"
              onConfirm={() => {
                setBlocked(null);
                del.mutate({ id: env.id });
              }}
            />
          </>
        )}
      </div>
      {blocked && (
        <p role="alert" className="mt-2 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[12.5px] text-danger">
          {blocked}
        </p>
      )}
    </li>
  );
}

function NewEnvironmentDialog({ projectId, canEdit }: { projectId: string; canEdit: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const create = useCreateEnvironment({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(projectId) });
        toastSuccess(`Created ${name.trim()}`);
        setOpen(false);
        setName("");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the environment"),
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setError(null);
      }}
    >
      <DialogTrigger asChild>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabledReason={canEdit ? undefined : "Adding an environment needs admin on the team"}
        >
          New environment
        </Button>
      </DialogTrigger>
      <DialogContent
        title="New environment"
        description="An environment is a copy of this project's world — its own applications, databases, stacks and variables, on whichever servers you place them."
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            create.mutate({ id: projectId, data: { name: name.trim() } });
          }}
          className="space-y-4"
        >
          <Field label="Name" qualifier="· unique in this project" error={error ?? undefined}>
            {(id) => (
              <Input
                id={id}
                required
                autoFocus
                maxLength={100}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="staging"
              />
            )}
          </Field>
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
              state={create.isPending ? "busy" : "idle"}
              busyLabel="Creating…"
              disabledReason={name.trim() === "" ? "Name it first" : undefined}
            >
              Create environment
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** "3 apps", "1 database" — the counts the card and the pill both speak in. */
function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function DangerZone({
  projectId,
  detail,
  canDelete,
}: {
  projectId: string;
  detail: ProjectDetail;
  canDelete: boolean;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [blocked, setBlocked] = useState<string | null>(null);
  const name = detail.project.name;
  const envs = detail.environments;
  const envName = (id: string) => envs.find((e) => e.id === id)?.name ?? "";

  // The same per-environment fan-out the projects list uses to count a row's
  // resources — the API has no project-level "what is inside" call.
  const appQueries = useQueries({ queries: envs.map((e) => getListApplicationsQueryOptions(e.id)) });
  const dbQueries = useQueries({ queries: envs.map((e) => getListDatabasesQueryOptions(e.id)) });
  const all = [...appQueries, ...dbQueries];
  const apps = appQueries.flatMap((q) => q.data ?? []);
  const dbs = dbQueries.flatMap((q) => q.data ?? []);
  const counting = all.some((q) => q.isPending);
  const countFailed = !counting && all.some((q) => q.isError);
  const showCounting = useSkeletonDelay(counting);
  const inUse = apps.length + dbs.length > 0;

  const del = useDeleteProject({
    mutation: {
      onSuccess: () => {
        // /projects renders from cache, and nothing streams project changes
        // in — so the row we just deleted would still be sitting there when we
        // land. Drop both the list and this project's own entry first, then
        // navigate, so the page we arrive at is one we can vouch for.
        void qc.invalidateQueries({ queryKey: getListProjectsQueryKey() });
        void qc.invalidateQueries({ queryKey: getGetProjectQueryKey(projectId) });
        toastSuccess(`Deleted ${name}`);
        void navigate({ to: "/projects" });
      },
      onError: (e: unknown) => {
        // The refusal is an instruction, not a notification: it stays on the
        // card. It only reaches here when the counts above were stale — a
        // teammate added something between the count and the click.
        if (e instanceof ApiError && e.status === 409) {
          setBlocked(e.message);
          all.forEach((q) => void q.refetch());
          return;
        }
        toastFailed("Could not delete the project", e);
      },
    },
  });

  const reason = !canDelete
    ? "Deleting a project needs admin on its team — ask an owner"
    : inUse
      ? "Delete the applications and databases inside first"
      : undefined;

  const inside = [apps.length > 0 && count(apps.length, "app", "apps"), dbs.length > 0 && count(dbs.length, "database", "databases")]
    .filter(Boolean)
    .join(" and ");

  return (
    // 12c draws this as one plain white card behind a heavy red rule — no
    // eyebrow: "Delete this project" already says which zone this is, and a
    // second heading only pushes the sentence that matters further down.
    <section className="mt-6 rounded-lg border-[1.5px] border-status-error/40 bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3.5">
        <div className="min-w-0 flex-1">
          <p className="text-[13.5px] font-semibold text-text">Delete this project</p>
          <div className="mt-[3px] text-xs leading-relaxed text-text-mid" aria-live="polite">
            {counting ? (
              showCounting ? (
                <Skeleton className="h-3 w-56 max-w-full" />
              ) : null
            ) : countFailed ? (
              <span className="flex flex-wrap items-center gap-2">
                Couldn't check what's inside this project.
                <ActionButton
                  size="sm"
                  variant="ghost"
                  state={all.some((q) => q.isFetching) ? "busy" : "idle"}
                  busyLabel="Checking…"
                  onClick={() => all.forEach((q) => void q.refetch())}
                >
                  Retry
                </ActionButton>
              </span>
            ) : inUse ? (
              <>
                {inside} {apps.length + dbs.length === 1 ? "is" : "are"} still inside — delete them first, so their containers
                and data volumes are torn down properly.
              </>
            ) : envs.length === 0 ? (
              <>Removes the project. It has no environments yet, so nothing else goes with it.</>
            ) : (
              <>
                Removes the project and its {count(envs.length, "environment", "environments")}. No applications or databases
                are left inside.
              </>
            )}
          </div>
        </div>
        <ConfirmDestructive
          trigger={
            <Button variant="danger" disabledReason={reason}>
              Delete
            </Button>
          }
          title={`Delete ${name}?`}
          blastRadius={[
            envs.length === 0
              ? "This project (it has no environments)"
              : `This project and its ${count(envs.length, "environment", "environments")} (${envs.map((e) => e.name).join(", ")})`,
            "Its notifiers, webhook endpoints and their delivery history, shared variables, and inbox items",
            "Cannot be undone",
          ]}
          confirmName={name}
          actionLabel="Delete project"
          pending={del.isPending}
          pendingLabel="Deleting…"
          onConfirm={() => del.mutate({ id: projectId })}
        />
      </div>

      {/* What stands in the way, each a link to the page it is deleted from.
          The list is the next verb — a count alone would send the operator
          hunting through every environment for the one database left behind. */}
      {inUse && (
        <ul className="mt-3 divide-y divide-border-subtle overflow-hidden rounded-md border border-border">
          {apps.map((a) => (
            <li key={a.id}>
              <Link
                to="/projects/$projectId/applications/$appId"
                params={{ projectId, appId: a.id }}
                className="flex items-center justify-between gap-3 px-3 py-2 hover:bg-raised"
              >
                <span className="mono min-w-0 truncate text-[12.5px] text-text">{a.name}</span>
                <span className="mono shrink-0 text-[11px] text-text-faint">application · {envName(a.environment_id)}</span>
              </Link>
            </li>
          ))}
          {dbs.map((d) => (
            <li key={d.id}>
              <Link
                to="/projects/$projectId/databases/$dbId"
                params={{ projectId, dbId: d.id }}
                className="flex items-center justify-between gap-3 px-3 py-2 hover:bg-raised"
              >
                <span className="mono min-w-0 truncate text-[12.5px] text-text">{d.name}</span>
                <span className="mono shrink-0 text-[11px] text-text-faint">
                  {d.engine} database · {envName(d.environment_id)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}

      {blocked && (
        <p role="alert" className="mt-3 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
          {blocked}
        </p>
      )}
    </section>
  );
}
