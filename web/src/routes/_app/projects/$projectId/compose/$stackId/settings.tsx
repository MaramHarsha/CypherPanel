// Compose stack · Settings: the file, the name, the route — and the delete.
//
// Editing the file does not deploy it. That is the API's contract ("a changed
// file becomes a new revision, but is not deployed") and it is the right one —
// a half-finished paste should not reach a host — so the page says it rather
// than leaving the operator to discover that nothing happened, and offers the
// deploy as the next step from the same screen.
//
// The delete carries a second decision the other resources do not have: whether
// the volumes go too. Absence means remove for the services, never for their
// data, so removing the data has to be asked for explicitly — and once it is,
// the confirm says so in the blast radius rather than in a checkbox label
// nobody re-reads.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import {
  getGetComposeFileQueryKey,
  getGetComposeStackQueryKey,
  getListComposeStacksQueryKey,
  useDeleteComposeStack,
  useDeployComposeStack,
  useGetComposeFile,
  useGetComposeStack,
  useUpdateComposeStack,
} from "@/api/gen/compose-stacks/compose-stacks";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input, Textarea } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId/settings")({
  component: ComposeSettingsTab,
});

function ComposeSettingsTab() {
  const { projectId, stackId } = Route.useParams();
  const stack = useGetComposeStack(stackId);
  const file = useGetComposeFile(stackId);

  return (
    <PageState query={stack} skeletonRows={4}>
      {(s) => (
        <div className="max-w-2xl space-y-8">
          <PageState query={file} skeletonRows={6}>
            {(rev) => (
              <StackForm
                stackId={stackId}
                name={s.name}
                yaml={rev.compose_yaml}
                domain={s.route.domain}
                service={s.route.service}
                port={s.route.port}
                https={s.route.https}
              />
            )}
          </PageState>
          <DangerZone projectId={projectId} envId={s.environment_id} stackId={stackId} name={s.name} />
        </div>
      )}
    </PageState>
  );
}

function StackForm({
  stackId,
  name: initialName,
  yaml: initialYaml,
  domain: initialDomain,
  service: initialService,
  port: initialPort,
  https,
}: {
  stackId: string;
  name: string;
  yaml: string;
  domain: string;
  service: string;
  port: number;
  https: boolean;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(initialName);
  const [yaml, setYaml] = useState(initialYaml);
  const [domain, setDomain] = useState(initialDomain);
  const [service, setService] = useState(initialService);
  const [port, setPort] = useState(initialPort ? String(initialPort) : "");
  const [error, setError] = useState<string | null>(null);

  // A rollback on another tab replaces the stored file underneath this form.
  // Re-seeding from the server's copy when it changes keeps the box showing
  // what is actually stored rather than a draft of something that is gone.
  useEffect(() => setYaml(initialYaml), [initialYaml]);

  const deploy = useDeployComposeStack({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetComposeStackQueryKey(stackId) });
        toastSuccess({ title: "Deploying", detail: "The agent converges the stack to the saved file." });
      },
      onError: (e: unknown, vars) => toastFailed("Could not deploy the stack", e, { retry: () => deploy.mutate(vars) }),
    },
  });

  const update = useUpdateComposeStack({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetComposeStackQueryKey(stackId) });
        void qc.invalidateQueries({ queryKey: getGetComposeFileQueryKey(stackId) });
        setError(null);
        // Saving mints a revision and changes nothing on the host, so the
        // toast carries the verb that makes it real (canvas 10c).
        toastSuccess({
          title: "Saved as a new revision",
          detail: "Nothing on the server has changed yet",
          actions: [{ label: "Deploy now", onClick: () => deploy.mutate({ id: stackId }) }],
        });
      },
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not save the stack");
        toastFailed("Could not save the stack", e, { retry: () => update.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(update);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    update.mutate({
      id: stackId,
      data: {
        name: name.trim(),
        compose_yaml: yaml,
        route: { domain: domain.trim(), service: service.trim(), port: Number(port) || 0, https },
      },
    });
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <Eyebrow>Stack</Eyebrow>

      <Field label="Name">
        {(id) => (
          <Input id={id} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        )}
      </Field>

      <Field
        label="Compose file"
        qualifier="· this is the desired state"
        hint="Saving mints a revision; deploying is what ships it. `build:` and `container_name:` are refused. Secrets belong in the stack's variables, not in the file — this text is kept verbatim in the revision history."
        error={error ?? undefined}
      >
        {(id) => (
          <Textarea
            id={id}
            required
            value={yaml}
            onChange={(e) => setYaml(e.target.value)}
            spellCheck={false}
            className="min-h-72 text-[12px]"
          />
        )}
      </Field>

      <Field
        label="Domain"
        qualifier="· optional"
        hint="Empty publishes nothing through the Proxy. The file's own Traefik labels cannot work — the managed Proxy runs the file provider only — so the stack names which service answers and the panel emits the route itself."
      >
        {(id) => <Input id={id} value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="grafana.example.com" />}
      </Field>

      {domain.trim() !== "" && (
        <div className="grid grid-cols-2 gap-3">
          <Field label="Service" qualifier="· which one answers">
            {(id) => <Input id={id} required value={service} onChange={(e) => setService(e.target.value)} />}
          </Field>
          <Field label="Port" qualifier="· inside its container">
            {(id) => (
              <Input
                id={id}
                required
                inputMode="numeric"
                value={port}
                onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))}
              />
            )}
          </Field>
        </div>
      )}

      <div className="flex items-center justify-end gap-2.5">
        <Button
          type="button"
          variant="secondary"
          size="lg"
          disabled={deploy.isPending}
          onClick={() => deploy.mutate({ id: stackId })}
        >
          Deploy the saved file
        </Button>
        <ActionButton type="submit" variant="primary" size="lg" state={state} busyLabel="Saving…" successLabel="Saved">
          Save revision
        </ActionButton>
      </div>
    </form>
  );
}

function DangerZone({
  projectId,
  envId,
  stackId,
  name,
}: {
  projectId: string;
  envId: string;
  stackId: string;
  name: string;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [deleteVolumes, setDeleteVolumes] = useState(false);

  const del = useDeleteComposeStack({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListComposeStacksQueryKey(envId) });
        toastSuccess(`Deleted ${name}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the stack", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <section className="space-y-3 border-t border-border pt-6">
      <Eyebrow>Danger zone</Eyebrow>
      <label className="flex max-w-xl items-start gap-2.5">
        <input
          type="checkbox"
          checked={deleteVolumes}
          onChange={(e) => setDeleteVolumes(e.currentTarget.checked)}
          className="mt-0.5 size-3.5 accent-accent"
        />
        <span className="text-[12.5px] leading-[1.45] text-text-mid">
          Destroy the volumes as well
          <span className="block text-[11.5px] text-text-faint">
            Off by default. Bringing a stack down never removes its data — the same rule a managed database follows —
            so anything the services wrote survives unless you ask for this.
          </span>
        </span>
      </label>
      <ConfirmDestructive
        trigger={
          <Button variant="danger" size="lg">
            Delete stack
          </Button>
        }
        title={`Delete ${name}?`}
        confirmName={name}
        blastRadius={[
          "Every container this stack runs is brought down and removed.",
          "Its route is withdrawn from the Proxy; the domain stops answering.",
          "Its sealed variables are destroyed and cannot be recovered.",
          deleteVolumes
            ? "Its volumes are destroyed — everything the services wrote is gone. This cannot be undone."
            : "Its volumes are kept, so the data survives. Re-deploying a stack with the same file finds them again.",
        ]}
        actionLabel="Delete stack"
        pending={del.isPending}
        pendingLabel="Deleting…"
        onConfirm={() => del.mutate({ id: stackId, params: deleteVolumes ? { delete_volumes: true } : undefined })}
      />
    </section>
  );
}
