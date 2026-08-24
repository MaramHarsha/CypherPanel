// Application · Settings: sectioned form + danger zone (typed-name delete,
// ui-principles §2). Dirty state is explicit; nothing saves silently.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import { toast } from "sonner";
import {
  getGetApplicationQueryKey,
  getListApplicationsQueryKey,
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
import { DomainField } from "@/components/domain-field";
import { DomainVerification } from "@/components/domain-verification";
import { Input, Select } from "@/components/ui/input";

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
  const qc = useQueryClient();
  const [name, setName] = useState(initial.name);
  const [repo, setRepo] = useState(initial.source.repo);
  const [branch, setBranch] = useState(initial.source.branch);
  const [image, setImage] = useState(initial.source.image ?? "");
  // An image-source app has no repository, branch, or build step — showing
  // those fields would invite edits the server rejects.
  const isImageSource = initial.source.kind === "image";
  const [domain, setDomain] = useState(initial.route.domain ?? "");
  const [buildKind, setBuildKind] = useState(initial.build.kind ?? "dockerfile");
  const [dockerfile, setDockerfile] = useState(initial.build.dockerfile_path);
  const [context, setContext] = useState(initial.build.context);
  const [previewEnabled, setPreviewEnabled] = useState(initial.preview_enabled ?? false);
  const [previewDomain, setPreviewDomain] = useState(initial.preview_base_domain ?? "");
  const [previewTTL, setPreviewTTL] = useState(String(initial.preview_ttl_hours ?? 72));

  const dirty =
    name !== initial.name ||
    repo !== initial.source.repo ||
    branch !== initial.source.branch ||
    image !== (initial.source.image ?? "") ||
    domain !== (initial.route.domain ?? "") ||
    buildKind !== (initial.build.kind ?? "dockerfile") ||
    dockerfile !== initial.build.dockerfile_path ||
    context !== initial.build.context ||
    previewEnabled !== (initial.preview_enabled ?? false) ||
    previewDomain !== (initial.preview_base_domain ?? "") ||
    previewTTL !== String(initial.preview_ttl_hours ?? 72);

  // Dirty forms warn before navigation discards them (ui-principles §6).
  useEffect(() => {
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);

  const update = useUpdateApplication({
    mutation: {
      onSuccess: () => {
        // This form is seeded from the detail query and the name it just wrote
        // is the one the environment lists it under, so both have to be told —
        // otherwise the page keeps rendering the values the operator replaced.
        void qc.invalidateQueries({ queryKey: getGetApplicationQueryKey(appId) });
        void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(initial.environment_id) });
        toast.success("Saved — applies on the next deploy");
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not save changes"),
    },
  });

  const del = useDeleteApplication({
    mutation: {
      onSuccess: () => {
        // The project page we land on renders its applications from cache, so
        // the row we just deleted would still be sitting there. Drop the list
        // before navigating rather than after: by then this component is gone.
        void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(initial.environment_id) });
        toast.success(`Deleted ${initial.name}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown) => toast.error(e instanceof Error ? e.message : "Could not delete the application"),
    },
  });

  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    // The API rejects this combination too; catching it here keeps the user's
    // typing instead of bouncing them off a toast (ui-principles §1).
    if (previewEnabled && previewDomain.trim() === "") {
      setError("A base domain is required to turn previews on — each PR is published at pr-<number>.<domain>.");
      return;
    }
    setError(null);
    update.mutate({
      id: appId,
      data: {
        name,
        source: isImageSource ? { ...initial.source, image } : { ...initial.source, repo, branch },
        build: { ...initial.build, kind: buildKind, dockerfile_path: dockerfile, context },
        route: { ...initial.route, domain: domain || undefined },
        preview_enabled: previewEnabled,
        preview_base_domain: previewDomain.trim(),
        preview_ttl_hours: Number(previewTTL) || 72,
      },
    });
  };

  return (
    <div className="max-w-xl space-y-8">
      <form onSubmit={submit} className="space-y-4">
        <Eyebrow>General</Eyebrow>
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        {isImageSource ? (
          <>
            <Field label="Image" hint="A moving tag is re-pulled on every deploy; a digest is pinned.">
              {(id) => <Input id={id} required value={image} onChange={(e) => setImage(e.target.value)} className="mono" />}
            </Field>
            <div>
              <DomainField applicationId={initial.id} value={domain} onChange={setDomain} />
              <DomainVerification applicationId={initial.id} />
            </div>
          </>
        ) : (
          <>
            <Field label="Repository">
              {(id) => <Input id={id} value={repo} onChange={(e) => setRepo(e.target.value)} className="mono" />}
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Branch">
                {(id) => <Input id={id} value={branch} onChange={(e) => setBranch(e.target.value)} className="mono" />}
              </Field>
              <div>
                <DomainField applicationId={initial.id} value={domain} onChange={setDomain} />
                <DomainVerification applicationId={initial.id} />
              </div>
            </div>
          </>
        )}
        {/* No build stage runs for an image source — the agent pulls the
            reference and rolls it out, so build settings would be inert. */}
        {!isImageSource && (
          <>
            <Field label="How to build it" hint="Detect picks a Dockerfile if the repository has one, otherwise serves it as a static site.">
              {(id) => (
                <Select id={id} value={buildKind} onChange={(e) => setBuildKind(e.target.value as typeof buildKind)}>
                  <option value="auto">Detect automatically</option>
                  <option value="dockerfile">Dockerfile</option>
                  <option value="static">Static site (HTML, CSS, JS)</option>
                </Select>
              )}
            </Field>
            <div className="grid grid-cols-2 gap-3">
              {buildKind !== "static" && (
                <Field label="Dockerfile path">
                  {(id) => <Input id={id} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />}
                </Field>
              )}
              <Field label="Build context">
                {(id) => <Input id={id} value={context} onChange={(e) => setContext(e.target.value)} />}
              </Field>
            </div>
          </>
        )}
        {/* Preview environments were reachable in the API but nowhere in the
            UI, while the Previews tab told the operator to enable them "in
            Settings" — a dead end (ui-principles §11).

            They are driven by pull_request webhooks matched against the app's
            branch, which an image source has none of, so the server refuses
            the combination — don't offer it. */}
        {!isImageSource && (
        <>
        <Eyebrow className="pt-4">Preview environments</Eyebrow>
        <p className="text-[12.5px] leading-relaxed text-text-mid">
          When on, opening a pull request against this repository builds a throwaway copy of the app at its own
          subdomain, and closing the PR tears it down.
        </p>
        <label className="flex items-start gap-2.5 text-[13px]">
          <input
            type="checkbox"
            checked={previewEnabled}
            onChange={(e) => setPreviewEnabled(e.target.checked)}
            className="mt-0.5 h-4 w-4 shrink-0 accent-[var(--accent)]"
          />
          <span>Create a preview environment for each pull request</span>
        </label>
        {previewEnabled && (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Base domain" hint="PR #12 becomes pr-12.<base domain>.">
              {(id) => (
                <Input
                  id={id}
                  value={previewDomain}
                  onChange={(e) => setPreviewDomain(e.target.value)}
                  placeholder="preview.example.com"
                />
              )}
            </Field>
            <Field label="Tear down after (hours)" hint="Backstop for a PR that never closes.">
              {(id) => (
                <Input
                  id={id}
                  inputMode="numeric"
                  value={previewTTL}
                  onChange={(e) => setPreviewTTL(e.target.value)}
                />
              )}
            </Field>
          </div>
        )}
        </>
        )}
        {error && (
          <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
            {error}
          </p>
        )}

        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" disabled={!dirty || update.isPending}>
            {update.isPending ? "Saving…" : "Save changes"}
          </Button>
          {dirty && <span className="text-xs text-text-faint">Unsaved changes</span>}
        </div>
      </form>

      <section className="space-y-2">
        <Eyebrow className="text-danger">Danger zone</Eyebrow>
        <div className="flex items-center justify-between gap-3 rounded-lg border-[1.5px] border-status-error/40 bg-surface px-4 py-3.5">
          <div>
            <p className="text-[13.5px] font-semibold text-text">Delete this application</p>
            <p className="mt-[3px] text-xs text-text-mid">Stops the container, removes its route, and deletes its deploy history.</p>
          </div>
          <ConfirmDestructive
            trigger={<Button variant="danger">Delete</Button>}
            title={`Delete ${initial.name}?`}
            // One entry per class of thing, each carrying its own consequence:
            // 13af's box is meant to be scanned, and a single sentence with a
            // red square in front of it is not.
            blastRadius={[
              "this application + the container serving it",
              initial.route.domain
                ? `its route at ${initial.route.domain} & the TLS cert`
                : "its domain route & TLS cert",
              ...(initial.preview_enabled ? ["every preview environment open for it"] : []),
              "its whole deployment history — this cannot be undone",
            ]}
            confirmName={initial.name}
            actionLabel="Delete forever"
            pending={del.isPending}
            onConfirm={() => del.mutate({ id: appId })}
          />
        </div>
      </section>
    </div>
  );
}
