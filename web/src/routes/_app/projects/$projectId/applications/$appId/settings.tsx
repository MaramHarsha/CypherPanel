// Application · Settings: sectioned form + danger zone (typed-name delete,
// ui-principles §2). Dirty state is explicit; nothing saves silently.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import {
  getGetApplicationQueryKey,
  getListApplicationsQueryKey,
  useDeleteApplication,
  useGetApplication,
  useListEnvVarKeys,
  useUpdateApplication,
} from "@/api/gen/applications/applications";
import { useListDeployKeys } from "@/api/gen/deploy-keys/deploy-keys";
import { useListDeployments } from "@/api/gen/deployments/deployments";
import type { Application } from "@/api/gen/model";
import { useListPreviews } from "@/api/gen/previews/previews";
import { useListScheduledTasks } from "@/api/gen/scheduled-tasks/scheduled-tasks";
import { AppAccessCard } from "@/components/app-access-card";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { DomainField } from "@/components/domain-field";
import { RouteStatus } from "@/components/route-status";
import { Input, Select } from "@/components/ui/input";
import { toastFailed, toastSuccess } from "@/lib/toast";

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
  const deployKeys = useListDeployKeys().data ?? [];
  const [name, setName] = useState(initial.name);
  const [repo, setRepo] = useState(initial.source.repo);
  const [branch, setBranch] = useState(initial.source.branch);
  // The deploy key an application clones a PRIVATE repository with. The API has
  // taken it on create and update since deploy-key-private-repos.md shipped and
  // nothing in the panel ever offered it, so the only way to attach one was the
  // API — which meant a private repository failed to clone with no way to fix
  // it from the screen that owns the source.
  const [deployKeyID, setDeployKeyID] = useState(initial.source.deploy_key_id ?? "");
  const [image, setImage] = useState(initial.source.image ?? "");
  // An image-source app has no repository, branch, or build step — showing
  // those fields would invite edits the server rejects.
  const isImageSource = initial.source.kind === "image";
  const [domain, setDomain] = useState(initial.route.domain ?? "");
  const [pathPrefix, setPathPrefix] = useState(initial.route.path_prefix);
  const [buildKind, setBuildKind] = useState(initial.build.kind ?? "dockerfile");
  const [dockerfile, setDockerfile] = useState(initial.build.dockerfile_path);
  const [context, setContext] = useState(initial.build.context);
  // Runtime and health were readable on the overview and editable NOWHERE. The
  // API has always accepted both blocks, so the only way to change the port an
  // application listens on was to call the API by hand — and the port is the
  // single most common thing that needs changing, because a framework picks its
  // own (Next.js 3000, Rails 3000, Vite 5173) and the panel defaults to 8080.
  const [port, setPort] = useState(String(initial.runtime.port));
  const [cpuLimit, setCPULimit] = useState(initial.runtime.cpu_limit == null ? "" : String(initial.runtime.cpu_limit));
  const [memLimit, setMemLimit] = useState(
    initial.runtime.memory_limit_mb == null ? "" : String(initial.runtime.memory_limit_mb),
  );
  const [healthPath, setHealthPath] = useState(initial.health.path);
  const [healthRetries, setHealthRetries] = useState(String(initial.health.retries));
  const [previewEnabled, setPreviewEnabled] = useState(initial.preview_enabled ?? false);
  const [previewDomain, setPreviewDomain] = useState(initial.preview_base_domain ?? "");
  const [previewTTL, setPreviewTTL] = useState(String(initial.preview_ttl_hours ?? 72));

  const dirty =
    name !== initial.name ||
    repo !== initial.source.repo ||
    branch !== initial.source.branch ||
    deployKeyID !== (initial.source.deploy_key_id ?? "") ||
    port !== String(initial.runtime.port) ||
    cpuLimit !== (initial.runtime.cpu_limit == null ? "" : String(initial.runtime.cpu_limit)) ||
    memLimit !== (initial.runtime.memory_limit_mb == null ? "" : String(initial.runtime.memory_limit_mb)) ||
    healthPath !== initial.health.path ||
    healthRetries !== String(initial.health.retries) ||
    image !== (initial.source.image ?? "") ||
    domain !== (initial.route.domain ?? "") ||
    normalizePrefix(pathPrefix) !== normalizePrefix(initial.route.path_prefix) ||
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
        toastSuccess("Saved — applies on the next deploy");
      },
      onError: (e: unknown, vars) => toastFailed("Could not save changes", e, { retry: () => update.mutate(vars) }),
    },
  });
  const saveState = useMutationActionState(update);

  const del = useDeleteApplication({
    mutation: {
      onSuccess: () => {
        // The project page we land on renders its applications from cache, so
        // the row we just deleted would still be sitting there. Drop the list
        // before navigating rather than after: by then this component is gone.
        void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(initial.environment_id) });
        toastSuccess(`Deleted ${initial.name}`);
        void navigate({ to: "/projects/$projectId", params: { projectId } });
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the application", e, { retry: () => del.mutate(vars) }),
    },
  });

  const [error, setError] = useState<string | null>(null);

  // 13af puts numbers in the blast radius — "2 preview environments · 214
  // deployments of history" — and every one of them is a list the app's own
  // tabs already fetch, so the confirm can be exact rather than "all of it".
  // Env values are write-only, so the count is the only thing said of them.
  const deployments = useListDeployments(appId);
  const previews = useListPreviews(appId);
  const envKeys = useListEnvVarKeys(appId);
  const tasks = useListScheduledTasks(appId);
  const volumes = initial.volumes ?? [];
  // Previews and deployments share one line, as the canvas sets them.
  const history = [
    ...(initial.preview_enabled || (previews.data?.length ?? 0) > 0
      ? counted(previews.data?.length, "preview environment", "every preview environment open for it")
      : []),
    ...counted(deployments.data?.length, "deployment", "its whole deployment history").map((s) =>
      deployments.data ? `${s} of history` : s,
    ),
  ];
  const blastRadius = [
    "this application + the container serving it",
    ...(initial.route.domain ? [`its route at ${initial.route.domain}`] : []),
    ...counted(envKeys.data?.keys.length, "env var", "every env var set on it"),
    ...(history.length > 0 ? [`${history.join(" · ")} — this cannot be undone`] : []),
    ...counted(tasks.data?.length, "scheduled task", "").map((s) => `${s} — nothing runs on their schedules again`),
    ...(volumes.length > 0
      ? [`${plural(volumes.length, "named volume")} — the data stays on the server; nothing reclaims it`]
      : []),
  ];

  const submit = (e: FormEvent) => {
    e.preventDefault();
    // The API rejects this combination too; catching it here keeps the user's
    // typing instead of bouncing them off a toast (ui-principles §1).
    if (previewEnabled && previewDomain.trim() === "") {
      setError("A base domain is required to turn previews on — each PR is published at pr-<number>.<domain>.");
      return;
    }
    const prefix = normalizePrefix(pathPrefix);
    if (prefix && !prefix.startsWith("/")) {
      setError("A path prefix starts with / — /api routes only that path and below; leave it empty for the whole host.");
      return;
    }
    setError(null);
    update.mutate({
      id: appId,
      data: {
        name,
        source: isImageSource
          ? { ...initial.source, image }
          : // Empty means "no key": a public repository needs none, and the API
            // reads null as exactly that.
            { ...initial.source, repo, branch, deploy_key_id: deployKeyID || null },
        build: { ...initial.build, kind: buildKind, dockerfile_path: dockerfile, context },
        runtime: {
          port: Number(port) || initial.runtime.port,
          // Blank means "no limit", which the API reads as null — not zero,
          // which would be a limit of nothing.
          cpu_limit: cpuLimit.trim() === "" ? null : Number(cpuLimit),
          memory_limit_mb: memLimit.trim() === "" ? null : Number(memLimit),
        },
        health: { ...initial.health, path: healthPath, retries: Number(healthRetries) || initial.health.retries },
        route: { ...initial.route, domain: domain || undefined, path_prefix: prefix },
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
            </div>
            <Field
              label="Deploy key"
              qualifier="· for a private repository"
              hint={
                deployKeys.length === 0
                  ? "No deploy keys yet. Create one in Settings → Deploy keys, then add its public half to the repository."
                  : "The panel clones over SSH with this key. Add its public half to the repository's own Deploy keys first."
              }
            >
              {(id, describedBy) => (
                <Select
                  id={id}
                  aria-describedby={describedBy}
                  value={deployKeyID}
                  onChange={(e) => setDeployKeyID(e.target.value)}
                >
                  <option value="">None — the repository is public</option>
                  {deployKeys.map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </>
        )}
        {/* No build stage runs for an image source — the agent pulls the
            reference and rolls it out, so build settings would be inert. */}
        {!isImageSource && (
          <>
            <Field
              label="How to build it"
              hint="Detect prefers a Dockerfile, then Nixpacks where it is installed on the builder, then a static site."
            >
              {(id) => (
                <Select id={id} value={buildKind} onChange={(e) => setBuildKind(e.target.value as typeof buildKind)}>
                  <option value="auto">Detect automatically</option>
                  <option value="dockerfile">Dockerfile</option>
                  <option value="static">Static site (HTML, CSS, JS)</option>
                  {/* Both packs are legal on the API and neither was offered
                      here, so a framework app could only be built by editing it
                      through the API. Chosen explicitly they are an assertion:
                      a builder without the binary fails loudly rather than
                      silently falling back (pack-builds.md §4). */}
                  <option value="nixpacks">Nixpacks (framework auto-build)</option>
                  <option value="railpack">Railpack (needs buildx)</option>
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
        <Eyebrow className="pt-4">Runtime</Eyebrow>
        <div className="grid gap-3 sm:grid-cols-3">
          <Field
            label="Port"
            hint="The port your app listens on inside its container — not a host port. A framework usually picks its own: Next.js and Rails use 3000, Vite 5173."
          >
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                inputMode="numeric"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                className="mono"
              />
            )}
          </Field>
          <Field label="CPU limit" qualifier="· cores" hint="Blank means no limit.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                inputMode="decimal"
                value={cpuLimit}
                onChange={(e) => setCPULimit(e.target.value)}
                placeholder="0.5"
                className="mono"
              />
            )}
          </Field>
          <Field label="Memory limit" qualifier="· MiB" hint="Blank means no limit.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                inputMode="numeric"
                value={memLimit}
                onChange={(e) => setMemLimit(e.target.value)}
                placeholder="512"
                className="mono"
              />
            )}
          </Field>
        </div>

        <Eyebrow className="pt-4">Health check</Eyebrow>
        <div className="grid gap-3 sm:grid-cols-[1fr_130px]">
          <Field
            label="Path"
            hint="Probed on the port above before a new container takes the route. A rollout that never passes this is discarded, and the old container keeps serving."
          >
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                value={healthPath}
                onChange={(e) => setHealthPath(e.target.value)}
                placeholder="/"
                className="mono"
              />
            )}
          </Field>
          <Field label="Retries" hint="Before the rollout is given up.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                inputMode="numeric"
                value={healthRetries}
                onChange={(e) => setHealthRetries(e.target.value)}
                className="mono"
              />
            )}
          </Field>
        </div>

        {/* Canvas 13c: the route is its own section — the domain, the path it
            answers on, and one row per hostname saying how it is served. The
            row reports the SAVED route (everything on it is fetched by id),
            so it does not chase the field while someone is typing. */}
        <Eyebrow className="pt-4">Route</Eyebrow>
        <div className="grid gap-3 sm:grid-cols-[1fr_130px]">
          <DomainField applicationId={initial.id} value={domain} onChange={setDomain} />
          <Field label="Path prefix" hint="Only this path and below; empty means the whole host.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                value={pathPrefix}
                onChange={(e) => setPathPrefix(e.target.value)}
                placeholder="/"
                className="mono"
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </Field>
        </div>
        <RouteStatus app={initial} />
        <p className="text-[12px] leading-relaxed text-text-faint">
          HTTP→HTTPS is automatic once issued. Wildcards, BYO certificates, and custom redirects are deliberately
          later (routing spec §10).
        </p>

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
          <ActionButton
            type="submit"
            variant="primary"
            state={saveState}
            busyLabel="Saving…"
            successLabel="Saved"
            disabledReason={dirty ? undefined : "Nothing has changed yet"}
          >
            Save changes
          </ActionButton>
          {dirty && <span className="text-xs text-text-faint">Unsaved changes</span>}
        </div>
      </form>

      <AppAccessCard appId={appId} />

      <section className="space-y-2">
        <Eyebrow className="text-danger">Danger zone</Eyebrow>
        <div className="flex items-center justify-between gap-3 rounded-lg border-[1.5px] border-status-error/40 bg-surface px-4 py-3.5">
          <div>
            <p className="text-[13.5px] font-semibold text-text">Delete this application</p>
            <p className="mt-[3px] text-xs text-text-mid">Stops the container, removes its route, and deletes its deploy history.</p>
          </div>
          <ConfirmDestructive
            trigger={<Button variant="danger">Delete</Button>}
            title={`Delete application ${initial.name}?`}
            // One entry per class of thing, each carrying its own consequence:
            // 13af's box is meant to be scanned, and a single sentence with a
            // red square in front of it is not.
            blastRadius={blastRadius}
            confirmName={initial.name}
            actionLabel="Delete forever"
            pendingLabel="Deleting…"
            pending={del.isPending}
            onConfirm={() => del.mutate({ id: appId })}
          />
        </div>
      </section>
    </div>
  );
}

/** `/api` stays `/api`; `/`, ` ` and `` all mean "the whole host", which the
 *  proxy spells as an empty prefix (the create form sends the same). */
function normalizePrefix(raw: string): string {
  const p = raw.trim();
  return p === "/" ? "" : p;
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/**
 * A counted entry once the list has arrived, the uncounted `fallback` while it
 * is still loading (never "0 …" for something not yet known), and nothing at
 * all when the count is zero — an empty class is not part of the blast.
 */
function counted(n: number | undefined, noun: string, fallback: string): string[] {
  if (n === undefined) return fallback ? [fallback] : [];
  if (n === 0) return [];
  return [plural(n, noun)];
}
