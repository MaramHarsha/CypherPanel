// Settings · GitHub App (github-app.md §7).
//
// One panel-level credential on the DNS provider screen's shape, deliberately:
// an operator who has connected Cloudflare has already met this page. What
// differs is the warning, because the credential differs — a GitHub App's
// private key can mint a token for every repository the App is installed on.
//
// The key is WRITE-ONLY. It goes in, it is sealed, and no route returns it —
// not even redacted (ui-principles §6). So the field is empty on every visit
// and saving means replacing, which the copy says out loud rather than leaving
// an operator to discover.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import {
  getGetGitHubAppQueryKey,
  useDeleteGitHubApp,
  useGetGitHubApp,
  useRefreshGitHubInstallations,
  useSetGitHubApp,
} from "@/api/gen/panel/panel";
import type { GitHubApp } from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyField } from "@/components/copy-field";
import { Eyebrow } from "@/components/eyebrow";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/settings/github")({ component: GitHubAppPage });

function GitHubAppPage() {
  useCrumbs([{ label: "settings", to: "/settings" }, { label: "github" }]);
  const app = useGetGitHubApp();
  return (
    <>
      <PageHeader size="sm" title="GitHub App" />
      <PageBody className="space-y-6 pt-5 pb-9">
        <PageState query={app} isEmpty={() => false} skeletonRows={3}>
          {(data) => <Body data={data} />}
        </PageState>
      </PageBody>
    </>
  );
}

function Body({ data }: { data: GitHubApp }) {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: getGetGitHubAppQueryKey() });

  const [appId, setAppId] = useState(String(data.app_id ?? ""));
  const [slug, setSlug] = useState(data.slug ?? "");
  const [key, setKey] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = useSetGitHubApp({
    mutation: {
      onSuccess: () => {
        setError(null);
        setKey("");
        setSecret("");
        refresh();
        toastSuccess({ title: "GitHub App connected", detail: "Its installations are listed below." });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not connect the App"),
    },
  });
  const reload = useRefreshGitHubInstallations({
    mutation: { onSuccess: refresh, onError: (e: unknown) => toastFailed("Could not reach GitHub", e) },
  });
  const disconnect = useDeleteGitHubApp({
    mutation: {
      onSuccess: () => {
        refresh();
        toastSuccess("GitHub App disconnected");
      },
      onError: (e: unknown) => toastFailed("Could not disconnect the App", e),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    save.mutate({
      data: {
        app_id: Number.parseInt(appId, 10),
        slug: slug.trim(),
        private_key_pem: key,
        webhook_secret: secret,
      },
    });
  };

  return (
    <>
      {data.configured && <WebhookSetup />}
      <section className="space-y-2.5">
        <Eyebrow>Connection</Eyebrow>
        <div className="rounded-lg border border-border bg-surface p-4">
          {data.configured ? (
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="text-[13px] font-semibold text-text">
                  Connected{data.slug ? ` · ${data.slug}` : ""}
                </p>
                <p className="mono mt-0.5 text-[11.5px] text-text-faint">app {data.app_id}</p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {data.install_url && (
                  <a
                    href={data.install_url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="rounded-full border border-border-input bg-surface px-4 py-1.5 text-[12.5px] font-semibold hover:border-border-strong"
                  >
                    Install on GitHub ↗
                  </a>
                )}
                <ActionButton
                  variant="secondary"
                  size="sm"
                  state={reload.isPending ? "busy" : "idle"}
                  busyLabel="Refreshing…"
                  onClick={() => reload.mutate()}
                >
                  Refresh installations
                </ActionButton>
                <ConfirmDestructive
                  title="Disconnect the GitHub App?"
                  lead="The stored credentials are forgotten:"
                  blastRadius={[
                    "Applications that reach their repository through this App fail their next deploy, with a reason — they do not silently fall back to an anonymous clone.",
                    "Deliveries to /webhooks/github/app stop verifying, so pushes stop deploying through the App.",
                    "Nothing on GitHub changes — the App and its installations stay, and reconnecting restores this.",
                  ]}
                  actionLabel="Disconnect"
                  pending={disconnect.isPending}
                  onConfirm={() => disconnect.mutate()}
                  trigger={
                    <Button variant="ghost" size="sm">
                      Disconnect
                    </Button>
                  }
                />
              </div>
            </div>
          ) : (
            <p className="text-[12.5px] leading-[1.5] text-text-mid">
              Not connected. Applications clone with a deploy key or anonymously, which keeps working — an App adds
              repository picking and replaces the long-lived key with a token minted for each build.
            </p>
          )}
        </div>
      </section>

      <section className="space-y-2.5">
        <Eyebrow>{data.configured ? "Replace the credentials" : "Connect an App"}</Eyebrow>
        <form onSubmit={submit} className="space-y-4 rounded-lg border border-border bg-surface p-4">
          <p className="text-[12.5px] leading-[1.6] text-text-mid">
            Create a GitHub App under your organisation's settings with{" "}
            <b className="text-text">Contents: read</b> and <b className="text-text">Metadata: read</b>, generate a
            private key, and paste both here.
          </p>
          {/* The blast radius, before the paste rather than after it. */}
          <p className="rounded-md border border-status-degraded/35 bg-status-degraded/[0.07] px-3 py-2 text-[12.5px] leading-[1.5] text-status-degraded-text">
            The private key can read every repository the App is installed on. It is sealed with the panel's master key
            and never shown again — not even redacted.
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="App ID" qualifier="· the number on the App's page" error={error ?? undefined}>
              {(id) => (
                <Input id={id} required inputMode="numeric" value={appId} onChange={(e) => setAppId(e.target.value)} className="mono" />
              )}
            </Field>
            <Field label="Slug" qualifier="· optional, for the install link">
              {(id) => <Input id={id} value={slug} onChange={(e) => setSlug(e.target.value)} className="mono" placeholder="my-panel" />}
            </Field>
          </div>
          <Field
            label="Private key"
            qualifier="· PEM"
            hint={data.configured ? "Leave blank only if you are not changing it — this form replaces what is stored." : undefined}
          >
            {(id, describedBy) => (
              <textarea
                id={id}
                aria-describedby={describedBy}
                required
                value={key}
                onChange={(e) => setKey(e.target.value)}
                rows={5}
                spellCheck={false}
                placeholder="-----BEGIN RSA PRIVATE KEY-----"
                className={cn(
                  "w-full rounded-md border border-border-input bg-surface px-3 py-2 font-mono text-[12px] text-text",
                  "transition-colors focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none",
                )}
              />
            )}
          </Field>
          <Field label="Webhook secret" qualifier="· optional" hint="Verifies deliveries to /webhooks/github/app. Without it that endpoint refuses everything.">
            {(id, describedBy) => (
              <Input id={id} aria-describedby={describedBy} value={secret} onChange={(e) => setSecret(e.target.value)} className="mono" />
            )}
          </Field>
          <div className="flex justify-end">
            <ActionButton
              type="submit"
              variant="primary"
              size="lg"
              state={save.isPending ? "busy" : "idle"}
              busyLabel="Checking with GitHub…"
              disabledReason={!appId.trim() || !key.trim() ? "The App ID and private key are both needed" : undefined}
            >
              {data.configured ? "Replace" : "Connect"}
            </ActionButton>
          </div>
        </form>
      </section>

      {data.configured && (
        <section className="space-y-2.5">
          <Eyebrow>Installations</Eyebrow>
          <div className="overflow-hidden rounded-lg border border-border bg-surface">
            {data.installations.length === 0 ? (
              <p className="px-4 py-3.5 text-[12.5px] leading-[1.5] text-text-mid">
                The App is connected but not installed anywhere yet. Install it on an organisation or account, then
                refresh — this list is GitHub's answer, not ours.
              </p>
            ) : (
              <ul className="divide-y divide-border-subtle">
                {data.installations.map((i) => (
                  <li key={i.installation_id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                    <span className="text-[13px] font-medium text-text">{i.account_login}</span>
                    <span className="mono text-[11px] text-text-faint">{i.account_type}</span>
                    <span className="mono ml-auto text-[11px] text-text-faint">
                      {i.repo_selection === "all" ? "all repositories" : "selected repositories"}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </section>
      )}
    </>
  );
}

/**
 * What to paste into the App's own settings on GitHub.
 *
 * "Connected" says the panel holds credentials GitHub accepted. It says nothing
 * about whether a push will ever arrive — that needs a webhook URL, a secret,
 * and the `push` event selected on the App, none of which the panel can do for
 * the operator and none of which it used to name. The one screen that mentioned
 * the endpoint at all mentioned it as a bare path inside a disconnect warning.
 *
 * This is the same shape the per-application PR webhook already uses (the
 * previews screen): the full URL, ready to copy, built from the origin the
 * operator is looking at.
 */
function WebhookSetup() {
  const url = new URL("/webhooks/github/app", window.location.origin).toString();
  const insecure = url.startsWith("http://");
  return (
    <section className="space-y-2.5">
      <Eyebrow>Push deploys</Eyebrow>
      <div className="space-y-3 rounded-lg border border-border bg-surface p-4">
        <p className="text-[12.5px] leading-[1.5] text-text-mid">
          Connecting the App lets the panel read repositories and mint clone tokens. Deploying on a push needs one
          more thing, and it is set on GitHub rather than here: the App&rsquo;s own webhook.
        </p>
        <div className="space-y-1.5">
          <p className="text-[12px] font-semibold text-text">1 · Webhook URL</p>
          <CopyField value={url} />
          {insecure && (
            <p className="text-[11.5px] leading-[1.5] text-status-degraded-text">
              This is the address you are browsing, and it is plain HTTP. GitHub will deliver to it, but the payload
              crosses the network in the clear — put the panel behind HTTPS before relying on it.
            </p>
          )}
        </div>
        <div className="space-y-1">
          <p className="text-[12px] font-semibold text-text">2 · Secret</p>
          <p className="text-[12px] leading-[1.5] text-text-mid">
            The same value as the webhook secret below. Without one this endpoint refuses every delivery — that is
            deliberate: it is unauthenticated by design, and a signature is the only thing standing in front of it.
          </p>
        </div>
        <div className="space-y-1">
          <p className="text-[12px] font-semibold text-text">3 · Events</p>
          <p className="text-[12px] leading-[1.5] text-text-mid">
            Subscribe to <span className="mono">push</span> and nothing else is required. Everything else is
            acknowledged and dropped, so extra events cost a request and change nothing.
          </p>
        </div>
        <p className="text-[11.5px] leading-[1.5] text-text-faint">
          GitHub shows the result under the App&rsquo;s Advanced &rarr; Recent Deliveries. A green tick means the
          signature verified; open the delivery and read the response body to see how many deployments it started —{" "}
          <span className="mono">{"{\"deployments\": 0}"}</span> means it verified and matched no application, which
          is usually a branch that no application builds.
        </p>
      </div>
    </section>
  );
}
