// "+ New project" (canvas 9a/13y): a 400px card with two questions — what to
// call it, and which team it belongs to — because a project's team is not
// recoverable by renaming it later.
//
// The canvas also draws a mono "slug: atlas-crm · used in URLs and the CLI"
// line under the name. There is no slug anywhere in the Project schema or in
// core, so a client-derived preview would promise a URL the server never
// mints (CLAUDE.md rule 4). The line arrives with the backend field.
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useGetMe } from "@/api/gen/auth/auth";
import { getListProjectsQueryKey, useCreateProject } from "@/api/gen/projects/projects";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useTeamScope } from "@/lib/team";
import { toastFailed } from "@/lib/toast";

export function CreateProjectDialog() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const me = useGetMe();
  const { teamId } = useTeamScope();
  const teams = me.data?.teams ?? [];
  const [name, setName] = useState("");
  const [team, setTeam] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = useCreateProject({
    mutation: {
      // The list this dialog was opened from is server state TanStack Query
      // owns (web-ui-design.md §5), and no SSE stream carries projects — so it
      // stays as it was until we say otherwise. Mark it stale before leaving,
      // or coming back shows a list without the project just created.
      onSuccess: (res) => {
        void qc.invalidateQueries({ queryKey: getListProjectsQueryKey() });
        void navigate({ to: "/projects/$projectId", params: { projectId: res.project.id } });
      },
      // The pill turns to "✕ Retry" and the toast carries the why (10b/10c);
      // the inline line keeps the server's sentence beside the form.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the project");
        toastFailed("Could not create the project", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);

  // Which team a project lands in is not recoverable by renaming it later, so
  // the choice is on screen rather than inferred from the current scope; the
  // scope is only the default. A scope left over from a team the user has since
  // left is ignored, or the select would show one team and submit another.
  const scoped = teams.some((t) => t.id === teamId) ? teamId : undefined;
  const chosen = team || scoped || teams[0]?.id;

  // /auth/me lists every team a panel owner can see, so an empty list is not
  // "no memberships" but "no team to put a project in" — and the server will
  // refuse the create. The field stays on screen, disabled, with the way out
  // beside it, rather than vanishing and leaving the 400 to explain itself.
  const noTeam = me.isSuccess && teams.length === 0;
  const blocked =
    name.trim() === ""
      ? "Name the project first"
      : me.isPending
        ? "Loading your teams…"
        : noTeam
          ? "Join a team first — a project belongs to a team"
          : undefined;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({ data: { name: name.trim(), ...(chosen ? { team_id: chosen } : {}) } });
  };

  // Closing resets the fields and the mutation, so a reopened modal never
  // inherits the last attempt's "✕ Retry" pill or its error line.
  const onOpenChange = (open: boolean) => {
    if (open) return;
    setName("");
    setTeam("");
    setError(null);
    create.reset();
  };

  return (
    <Dialog onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="primary" size="lg">
          <Plus className="h-3.5 w-3.5" aria-hidden /> New project
        </Button>
      </DialogTrigger>
      <DialogContent
        className="max-w-[400px]"
        title="New project"
        description="A project groups apps and databases that ship together. Every project starts with a production environment."
      >
        <form onSubmit={submit} className="space-y-4">
          <fieldset disabled={create.isPending} className="min-w-0 space-y-4">
            {/* A project name is prose, not a machine value — the one place
                the mono default is wrong (input.tsx). */}
            <Field label="Name" error={error ?? undefined}>
              {(id, describedBy) => (
                <Input
                  id={id}
                  required
                  autoFocus
                  className="font-sans"
                  aria-describedby={describedBy}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="Atlas CRM"
                />
              )}
            </Field>
            <Field
              label="Team"
              hint={noTeam ? "You are not in a team yet — create one, or ask an admin to add you, under Settings → Teams." : undefined}
            >
              {(id, describedBy) =>
                me.isPending ? (
                  <Select id={id} disabled aria-busy className="font-sans">
                    <option>Loading teams…</option>
                  </Select>
                ) : noTeam ? (
                  <Select id={id} disabled aria-describedby={describedBy} className="font-sans">
                    <option>No teams yet</option>
                  </Select>
                ) : (
                  <Select id={id} className="font-sans" value={chosen ?? ""} onChange={(e) => setTeam(e.target.value)}>
                    {teams.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                  </Select>
                )
              }
            </Field>
          </fieldset>
          <div className="flex items-center justify-end gap-2.5 pt-1">
            <DialogClose asChild>
              <Button variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              type="submit"
              variant="accent"
              size="lg"
              state={state}
              busyLabel="Creating…"
              successLabel="Created"
              disabledReason={blocked}
            >
              Create project →
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
