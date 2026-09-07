// "+ New application" (canvas 3h, build step 6b/13b). Simple by default, power
// underneath (ui-principles §6): the form asks only what a first-timer must
// answer — where the code is, what to call it — and every working default is
// visible. The build strategy is its own section because auto-detect is the
// one default a first-timer has to know exists; the Dockerfile path and build
// context it reads stay under Advanced.
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { getListApplicationsQueryKey, useCreateApplication } from "@/api/gen/applications/applications";
import { AppBuildKind } from "@/api/gen/model";
import { useDeployApplication } from "@/api/gen/deployments/deployments";
import { useListDeployKeys } from "@/api/gen/deploy-keys/deploy-keys";
import { useListGitHubRepositories } from "@/api/gen/panel/panel";
import { useListServers } from "@/api/gen/servers/servers";
import { DomainField } from "@/components/domain-field";
import { AdvancedSection } from "@/components/advanced-section";
import { BuildStrategyField } from "@/components/build-strategy-field";
import { JoinServerFirstDialog } from "@/components/join-server-first-dialog";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { toastFailed } from "@/lib/toast";

export function NewAppDialog({
  envId,
  projectId,
  projectName,
  envName,
  primary,
}: {
  envId: string;
  projectId: string;
  projectName: string;
  envName: string;
  primary?: boolean;
}) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const servers = useListServers();
  const deployKeys = useListDeployKeys().data ?? [];
  // Empty on every panel with no App connected — the API answers 200 with []
  // rather than an error for exactly that case, so this needs no gate of its
  // own beyond "is there anything to pick".
  const repos = useListGitHubRepositories().data ?? [];
  const [name, setName] = useState("");
  const [sourceKind, setSourceKind] = useState<"github" | "image">("github");
  const [repo, setRepo] = useState("");
  const [image, setImage] = useState("");
  const [branch, setBranch] = useState("main");
  // Offered HERE rather than only after creation: the hint below has always
  // said "private with a deploy key", and Settings → Deploy keys only CREATES
  // one — so a private repository was a dead end at the first screen, and the
  // application failed its first clone with no way to fix it.
  const [deployKeyID, setDeployKeyID] = useState("");
  // Which App installation mints this repository's clone token, or null for
  // "not through the App" — a public repository, or a private one with a deploy
  // key. Set by picking a repository, cleared by typing a URL by hand.
  const [installationID, setInstallationID] = useState<number | null>(null);
  const [domain, setDomain] = useState("");
  const [serverId, setServerId] = useState("");
  const [port, setPort] = useState("8080");
  const [buildKind, setBuildKind] = useState<AppBuildKind>(AppBuildKind.auto);
  const [dockerfile, setDockerfile] = useState("./Dockerfile");
  const [context, setContext] = useState(".");
  const [error, setError] = useState<string | null>(null);

  // A builder-only agent is built without a workload driver and rejects rollout
  // work, so offering it would create the resource and then fail every deploy.
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServer = serverId || enrolled[0]?.id || "";

  // Declared before `create` so its onSuccess can reach it.
  const deploy = useDeployApplication();
  const create = useCreateApplication({
    mutation: {
      onSuccess: (res) => {
        // Coming back from the application will render the board from cache,
        // so the list is refreshed on the way out rather than leaving the
        // operator to wonder where their new application went.
        void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(envId) });
        // The button says "Deploy", so it deploys. Creating without deploying
        // left an operator on an application page with a Stopped badge,
        // wondering what they had just done — "when I create application it was
        // not deploying, I need to click on deploy button manually". Failing to
        // start is not failing to create: the application exists either way and
        // its own page has the button, so this reports and moves on.
        deploy.mutate(
          { id: res.application.id, data: {} },
          {
            onError: (e: unknown) =>
              toastFailed("Created, but the first deploy did not start", e, {
                retry: () => deploy.mutate({ id: res.application.id, data: {} }),
              }),
          },
        );
        void navigate({
          to: "/projects/$projectId/applications/$appId",
          params: { projectId, appId: res.application.id },
        });
      },
      // The pill offers the retry; the toast carries the why (10b/10c). The
      // inline line keeps the server's sentence beside the form it is about.
      onError: (e: unknown, vars) => {
        setError(e instanceof Error ? e.message : "Could not create the application");
        toastFailed("Could not create the application", e, { retry: () => create.mutate(vars) });
      },
    },
  });
  const state = useMutationActionState(create);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    create.mutate({
      id: envId,
      data: {
        name,
        source:
          sourceKind === "image"
            ? { kind: "image", image }
            : {
                kind: "github",
                repo,
                branch,
                // The two credentials are alternatives, and picking a
                // repository through the App clears the other: whichever the
                // operator chose last is the one they meant. Sending both would
                // store a configuration whose behaviour is decided by the
                // builder's precedence rather than by the person.
                deploy_key_id: installationID === null ? deployKeyID || null : null,
                github_installation_id: installationID,
              },
        build: { kind: buildKind, dockerfile_path: dockerfile, context },
        runtime: { server_id: chosenServer, port: Number(port), replicas: 1 },
        route: { domain: domain || undefined, https: true, path_prefix: "" },
      },
    });
  };

  const trigger = (
    <Button variant="primary" size={primary ? "lg" : "md"}>
      <Plus className="h-3.5 w-3.5" /> New application
    </Button>
  );

  if (!servers.isPending && enrolled.length === 0) {
    // Golden path: no servers yet → the next step is joining one, in context.
    return <JoinServerFirstDialog trigger={trigger} resource="application" />;
  }

  // 10b: a pill that cannot be pressed names its reason, in a tooltip.
  const missing =
    sourceKind === "github"
      ? repo.trim() === ""
        ? "Enter a repository first"
        : undefined
      : image.trim() === ""
        ? "Enter an image first"
        : undefined;

  return (
    <Dialog>
      <DialogTrigger asChild>{trigger}</DialogTrigger>
      <DialogContent
        size="form"
        // The last segment — where this dialog is acting — in accent (13z).
        eyebrow={
          <>
            {projectName} / {envName} / <span className="text-accent">new application</span>
          </>
        }
        title="Deploy an application"
        description="CypherPanel builds it (or pulls the image) and keeps it running at your domain."
      >
        <form onSubmit={submit} className="space-y-4">
          {/* Locked while the create is in flight: a second submit would
              create a second application (10a). */}
          <fieldset disabled={create.isPending} className="min-w-0 space-y-4">
            {/* Source first: it is the one thing only the operator knows.
                Everything with a working default folds into Advanced below
                (ui-principles §6, design 5w). */}
            <Field label="Deploy from">
              {(id) => (
                <Select id={id} value={sourceKind} onChange={(e) => setSourceKind(e.target.value as typeof sourceKind)}>
                  <option value="github">Git repository — built from source</option>
                  <option value="image">Container image — prebuilt, no build step</option>
                </Select>
              )}
            </Field>
            {sourceKind === "github" ? (
              <>
                {/* Repository picking, github-app.md §5: "pick one" instead of
                    "type a URL and hope". It appears only when the App is
                    connected AND can see something — an empty list is the
                    ordinary state of every panel that has not set one up, and a
                    control offering nothing is worse than no control.

                    It sets the CLONE URL, not `full_name`: the API needs a
                    remote git can reach, and `acme/web` is refused for the same
                    reason the placeholder below stopped suggesting it. */}
                {repos.length > 0 && (
                  <Field
                    label="Repository from GitHub"
                    qualifier="· optional"
                    hint="Cloned with a token minted for each build, not a stored key. Pick one, or type a URL below."
                  >
                    {(id, describedBy) => (
                      <Select
                        id={id}
                        aria-describedby={describedBy}
                        value={installationID === null ? "" : repo}
                        onChange={(e) => {
                          const picked = repos.find((r) => r.clone_url === e.target.value);
                          if (!picked) {
                            setInstallationID(null);
                            return;
                          }
                          setRepo(picked.clone_url);
                          setBranch(picked.default_branch || "main");
                          setInstallationID(picked.installation_id);
                        }}
                      >
                        <option value="">Type a repository URL instead…</option>
                        {repos.map((r) => (
                          <option key={r.clone_url} value={r.clone_url}>
                            {r.full_name}
                            {r.private ? " · private" : ""}
                          </option>
                        ))}
                      </Select>
                    )}
                  </Field>
                )}
                {/* The placeholder is a full URL because git needs one: it
                    reads a schemeless string as a LOCAL directory on the
                    builder, so the old `github.com/acme/web` suggestion
                    produced an application that saved and then failed to clone
                    with `exit status 128`. The API normalises that shorthand
                    now; the field stops teaching it. */}
                <Field
                  label="Repository"
                  hint="An https:// URL, or git@host:owner/repo.git. Public, or private with a deploy key below."
                >
                  {(id, describedBy) => (
                    <Input
                      id={id}
                      aria-describedby={describedBy}
                      required
                      autoFocus
                      className="mono"
                      spellCheck={false}
                      value={repo}
                      onChange={(e) => {
                        setRepo(e.target.value);
                        // A hand-typed URL is not the repository that was
                        // picked, so the installation that went with it no
                        // longer applies.
                        setInstallationID(null);
                      }}
                      placeholder="https://github.com/acme/web"
                    />
                  )}
                </Field>
                <Field
                  label="Deploy key"
                  qualifier="· for a private repository"
                  hint={
                    deployKeys.length === 0
                      ? "None yet — create one in Settings → Deploy keys, then add its public half to the repository."
                      : "Add the key's public half to the repository's own Deploy keys first, or the clone will be refused."
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
            ) : (
              <Field label="Image" hint="Any public registry reference; the server pulls it directly.">
                {(id) => (
                  <Input
                    id={id}
                    required
                    autoFocus
                    value={image}
                    onChange={(e) => setImage(e.target.value)}
                    placeholder="ghcr.io/acme/web:1.2"
                  />
                )}
              </Field>
            )}

            {/* No build stage runs for an image source — the agent pulls the
                reference and rolls it out, so a strategy would be inert. */}
            {sourceKind === "github" && (
              <BuildStrategyField
                value={buildKind}
                onChange={setBuildKind}
                dockerfilePath={dockerfile}
                context={context}
                eyebrow={
                  <>
                    new application / <span className="text-accent">build</span>
                  </>
                }
              />
            )}

            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Name">
                {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />}
              </Field>
              {/* Placement is always stated — a one-server fleet is still an
                  answer to "where does this run", in a field that reads as a
                  field rather than a greyed-out one. */}
              <Field label="Server">
                {(id) => (
                  <Select id={id} value={chosenServer} onChange={(e) => setServerId(e.target.value)}>
                    {enrolled.map((sv) => (
                      <option key={sv.id} value={sv.id}>
                        {sv.name}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            </div>

            {/* Pick from the zones the panel already manages, plus a box for
                what goes in front. Typing a hostname the panel knows was always
                busywork, and nothing warned when it was already taken. */}
            <DomainField value={domain} onChange={setDomain} serverId={chosenServer} />


            <AdvancedSection note="defaults work">
              <div className="grid gap-3 sm:grid-cols-2">
                {sourceKind === "github" && (
                  <Field label="Branch">
                    {(id) => <Input id={id} required value={branch} onChange={(e) => setBranch(e.target.value)} />}
                  </Field>
                )}
                <Field label="Port" hint="The port your app listens on.">
                  {(id) => <Input id={id} required inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} />}
                </Field>
              </div>
              {sourceKind === "github" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  {buildKind !== "static" && (
                    <Field label="Dockerfile path" hint="Relative to the build context.">
                      {(id) => <Input id={id} value={dockerfile} onChange={(e) => setDockerfile(e.target.value)} />}
                    </Field>
                  )}
                  <Field label="Build context" hint="The directory to build from.">
                    {(id) => <Input id={id} value={context} onChange={(e) => setContext(e.target.value)} />}
                  </Field>
                </div>
              )}
            </AdvancedSection>
          </fieldset>

          {error && (
            <p role="alert" className="rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[13px] text-danger">
              {error}
            </p>
          )}
          <div className="flex items-center gap-2 pt-1">
            <ActionButton
              type="submit"
              variant="accent"
              size="lg"
              state={state}
              busyLabel="Deploying…"
              disabledReason={missing}
            >
              Deploy →
            </ActionButton>
            <DialogClose asChild>
              <Button variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
