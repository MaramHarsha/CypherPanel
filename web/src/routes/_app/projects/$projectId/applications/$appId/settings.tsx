// Application · Settings: sectioned form + danger zone (typed-name delete,
// ui-principles §2). Dirty state is explicit; nothing saves silently.
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  useDeleteApplication,
  useGetApplication,
  useUpdateApplication,
} from "@/api/gen/applications/applications";
import type { Application } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/_app/projects/$projectId/applications/$appId/settings")({
  component: SettingsTab,
});

function SettingsTab() {
  const { projectId, appId } = Route.useParams();
  const app = useGetApplication(appId);

  return (
    <PageState query={app} isEmpty={() => false}>
      {(a) => <SettingsForm key={a.id} appId={appId} projectId={projectId} initial={a} />}
    </PageState>
  );
}

function SettingsForm({
  appId,
  projectId,
  initial,
}: {
  appId: string;
  projectId: string;
  initial: Application;
}) {
  const navigate = useNavigate();
  const [name, setName] = useState(initial.name);
  const [repo, setRepo] = useState(initial.source.repo);
  const [branch, setBranch] = useState(initial.source.branch);
  const [domain, setDomain] = useState(initial.route.domain ?? "");
  const [dockerfile, setDockerfile] = useState(initial.build.dockerfile_path);
  const [context, setContext] = useState(initial.build.context);

  const dirty =
    name !== initial.name ||
    repo !== initial.source.repo ||
    branch !== initial.source.branch ||
    domain !== (initial.route.domain ?? "") ||
    dockerfile !== initial.build.dockerfile_path ||
    context !== initial.build.context;

  // Dirty forms warn before navigation discards them (ui-principles §6).
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);

  const update = useUpdateApplication({
    mutation: {
      onSuccess: () => toast.success("Saved — applies on the next deploy"),
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not save changes"),
    },
  });

  const del = useDeleteApplication({
    mutation: {
      onSuccess: () => {
        toast.success(`Deleted ${initial.name}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the application"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    update.mutate({
      id: appId,
      data: {
        name,
        source: { ...initial.source, repo, branch },
        build: { ...initial.build, dockerfile_path: dockerfile, context },
        route: { ...initial.route, domain: domain || undefined },
      },
    });
  };

  return (
    <div className="max-w-xl space-y-8">
      <form onSubmit={submit} className="space-y-4">
        <Eyebrow>General</Eyebrow>
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Repository">
          {(id) => <Input id={id} value={repo} onChange={(e) => setRepo(e.target.value)} className="mono" />}
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Branch">
            {(id) => <Input id={id} value={branch} onChange={(e) => setBranch(e.target.value)} className="mono" />}
          </Field>
          <Field label="Domain" hint="Where the app is reachable.">
            {(id) => <Input id={id} value={domain} onChange={(e) => setDomain(e.target.value)} className="mono" />}
          </Field>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Dockerfile path">
            {(id) => <Input id={id} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} className="mono" />}
          </Field>
          <Field label="Build context">
            {(id) => <Input id={id} value={context} onChange={(e) => setContext(e.target.value)} className="mono" />}
          </Field>
        </div>
        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" disabled={!dirty || update.isPending}>
            {update.isPending ? "Saving…" : "Save changes"}
          </Button>
          {dirty && <span className="text-xs text-text-faint">Unsaved changes</span>}
        </div>
      </form>

      <section className="space-y-2">
        <Eyebrow className="text-danger">Danger zone</Eyebrow>
        <div className="flex items-center justify-between gap-3 rounded-md border border-danger/30 p-4">
          <div>
            <p className="text-[13px] font-medium text-text">Delete this application</p>
            <p className="text-xs text-text-mid">Stops the container, removes its route, and deletes its deploy history.</p>
          </div>
          <ConfirmDestructive
            trigger={<Button variant="danger">Delete</Button>}
            title={`Delete ${initial.name}?`}
            blastRadius="Deletes this application, stops its running container, removes its domain route, and erases its deployments. This cannot be undone."
            confirmName={initial.name}
            actionLabel="Delete application"
            pending={del.isPending}
            onConfirm={() => del.mutate({ id: appId })}
          />
        </div>
      </section>
    </div>
  );
}
