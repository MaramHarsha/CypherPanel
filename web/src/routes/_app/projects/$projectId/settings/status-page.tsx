// Project settings · Status page (canvas 12c's "Status page" tab, now that it
// has an endpoint).
//
// This screen publishes things to people who are not signed in and never will
// be, and that — not the bars — is what its layout is about. Two decisions
// carry it:
//
// The PREVIEW is the disclosure control. A confirmation dialog saying "this
// will be public" is read by nobody; a rendered page with the customer's name
// on it is read by everybody, because it looks like the thing it is. So the
// preview is not an afterthought at the bottom — it sits directly under the
// switch, showing exactly what a visitor gets, before and after publishing.
//
// The LABEL is the only name that becomes public. Every component row says so
// by putting the label in an editable field beside a dimmed resource name: the
// operator can see the thing they picked and the thing the internet will read,
// and that they are not the same string. An agency whose projects are named
// after clients must not publish a customer list by flipping a switch.
import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { GripVertical, Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useListApplications } from "@/api/gen/applications/applications";
import { useListComposeStacks } from "@/api/gen/compose-stacks/compose-stacks";
import { useListDatabases } from "@/api/gen/databases/databases";
import {
  getGetStatusPageQueryKey,
  getListStatusPageComponentsQueryKey,
  getPreviewStatusPageQueryKey,
  useCheckStatusPageDomain,
  useDeleteStatusPage,
  useGetStatusPage,
  useListStatusPageComponents,
  usePreviewStatusPage,
  useSetStatusPage,
  useSetStatusPageComponents,
} from "@/api/gen/projects/projects";
import { useListEnvironments } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import type {
  PublicStatusPage,
  SetStatusPageComponentsRequestComponentsItem,
  StatusPage,
  StatusPageComponent,
} from "@/api/gen/model";
import { ConfirmDestructive } from "@/components/confirm-destructive";
import { CopyField } from "@/components/copy-field";
import { DomainCheckRow } from "@/components/domain-check-row";
import { EmptyState } from "@/components/empty-state";
import { Eyebrow } from "@/components/eyebrow";
import { PageState } from "@/components/page-state";
import { StatusPagePreview } from "@/components/status-page-preview";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed, toastSuccess } from "@/lib/toast";
import { cn } from "@/lib/utils";

export const Route = createFileRoute("/_app/projects/$projectId/settings/status-page")({
  component: StatusPageTab,
});

const SLUG = /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/;

type Draft = SetStatusPageComponentsRequestComponentsItem & { key: string; resourceName: string };

function StatusPageTab() {
  const { projectId } = Route.useParams();
  useCrumbs([{ label: "settings" }, { label: "status page" }]);
  const page = useGetStatusPage(projectId, { query: { retry: false } });

  // A 404 here is the normal state for most projects, not an error: this
  // feature is opt-in and the majority of projects will never want one.
  if (page.isPending) {
    return <PageState query={page} isEmpty={() => false} skeletonRows={3}>{() => null}</PageState>;
  }
  if (page.isError || !page.data) {
    return <NoPageYet projectId={projectId} />;
  }
  return <PageEditor projectId={projectId} page={page.data} />;
}

function NoPageYet({ projectId }: { projectId: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface">
      <EmptyState
        glyph="◎"
        title="No status page"
        hint="Publish one page saying whether this project's services are working — for customers, at your own domain. It reuses the health checks the panel already runs, so there is nothing to configure twice."
        action={<CreateDialog projectId={projectId} primary />}
      />
    </div>
  );
}

function CreateDialog({ projectId, primary }: { projectId: string; primary?: boolean }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = useSetStatusPage({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetStatusPageQueryKey(projectId) });
        setOpen(false);
        toastSuccess({
          title: "Status page created",
          detail: "Nothing is public yet — add components, look at the preview, then publish.",
        });
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not create the page"),
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const s = slug.trim().toLowerCase();
    if (!SLUG.test(s)) {
      setError("The address is 3–40 lowercase letters, digits and dashes.");
      return;
    }
    setError(null);
    // Created switched OFF, always. A page that publishes the moment it exists
    // would disclose before anyone has seen what it says.
    save.mutate({ id: projectId, data: { title: title.trim(), slug: s, enabled: false, https: true } });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> Create a status page
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Create a status page"
        description="It starts switched off. Nothing is published until you turn it on, and the preview shows exactly what a visitor would see first."
      >
        <form onSubmit={submit} className="space-y-4">
          <Field label="Page title" hint="Shown at the top of the public page. Customers read this.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                autoFocus
                value={title}
                onChange={(e) => {
                  setTitle(e.target.value);
                  if (!slug) setSlug(e.target.value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""));
                }}
                placeholder="Acme"
              />
            )}
          </Field>
          <Field label="Address" qualifier="· public handle" hint="Lowercase letters, digits and dashes. It appears in the page's URL and is unique across this panel.">
            {(id, describedBy) => (
              <Input
                id={id}
                aria-describedby={describedBy}
                required
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                className="mono"
                placeholder="acme"
                spellCheck={false}
              />
            )}
          </Field>
          {error && (
            <p role="alert" className="text-[13px] text-danger">
              {error}
            </p>
          )}
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
              state={save.isPending ? "busy" : "idle"}
              busyLabel="Creating…"
              disabledReason={!title.trim() ? "Give the page a title" : undefined}
            >
              Create
            </ActionButton>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function PageEditor({ projectId, page }: { projectId: string; page: StatusPage }) {
  return (
    <div className="max-w-3xl space-y-6">
      <PublishSwitch projectId={projectId} page={page} />
      <Addresses projectId={projectId} page={page} />
      <Components projectId={projectId} page={page} />
      <Preview page={page} />
      <DangerZone projectId={projectId} page={page} />
    </div>
  );
}

function PublishSwitch({ projectId, page }: { projectId: string; page: StatusPage }) {
  const qc = useQueryClient();
  const preview = usePreviewStatusPage(page.id);
  const componentCount = preview.data?.components.length ?? 0;

  const save = useSetStatusPage({
    mutation: {
      onSuccess: (p) => {
        void qc.invalidateQueries({ queryKey: getGetStatusPageQueryKey(projectId) });
        toastSuccess(
          p.enabled
            ? { title: "Status page published", detail: `Anyone with the link can read it now.` }
            : { title: "Status page unpublished", detail: "Both public addresses answer 404 within a few seconds." },
        );
      },
      onError: (e: unknown, vars) => toastFailed("Could not change the page", e, { retry: () => save.mutate(vars) }),
    },
  });

  const toggle = (enabled: boolean) =>
    save.mutate({
      id: projectId,
      data: {
        slug: page.slug,
        title: page.title,
        enabled,
        domain: page.domain,
        https: page.https,
        route_server_id: page.route_server_id,
      },
    });

  return (
    <section
      className={cn(
        "rounded-lg border px-4 py-3.5",
        page.enabled ? "border-status-running/40 bg-status-running/5" : "border-border bg-surface",
      )}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-[14px] font-semibold text-text">
            {page.enabled ? "This page is public" : "This page is not public"}
          </p>
          <p className="mt-0.5 max-w-prose text-[12.5px] leading-[1.5] text-text-mid">
            {page.enabled ? (
              <>
                Anyone with the address reads it — no sign-in. Only the labels below are published; resource names,
                servers, domains and revisions never leave the panel.
              </>
            ) : (
              <>Both public addresses answer 404. Look at the preview below before you turn it on.</>
            )}
          </p>
        </div>
        {page.enabled ? (
          <ActionButton
            variant="secondary"
            size="sm"
            state={save.isPending ? "busy" : "idle"}
            busyLabel="Unpublishing…"
            onClick={() => toggle(false)}
          >
            Unpublish
          </ActionButton>
        ) : (
          <PublishConfirm
            page={page}
            count={componentCount}
            pending={save.isPending}
            onConfirm={() => toggle(true)}
          />
        )}
      </div>
    </section>
  );
}

// Publishing shows the labels one last time, because those are the strings the
// internet gets. It is not a "are you sure" — it is the list itself.
function PublishConfirm({
  page,
  count,
  pending,
  onConfirm,
}: {
  page: StatusPage;
  count: number;
  pending: boolean;
  onConfirm: () => void;
}) {
  const [open, setOpen] = useState(false);
  const preview = usePreviewStatusPage(page.id, { query: { enabled: open } });
  const where = page.domain ? (page.https ? `https://${page.domain}` : `http://${page.domain}`) : page.panel_url;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          variant="primary"
          size="sm"
          disabledReason={count === 0 ? "Add at least one component first" : undefined}
        >
          Publish
        </Button>
      </DialogTrigger>
      <DialogContent
        title="This page will be public"
        description={`Anyone who knows the address reads it, with no sign-in. It will be at ${where}.`}
      >
        <div className="space-y-4">
          <div>
            <Eyebrow>What the internet will read</Eyebrow>
            <ul className="mt-2 divide-y divide-border-subtle overflow-hidden rounded-md border border-border">
              <li className="bg-pane px-3 py-2 text-[13px] font-semibold text-pane-text">{page.title}</li>
              {(preview.data?.components ?? []).map((c) => (
                <li key={c.label} className="px-3 py-2 text-[13px] text-text">
                  {c.label}
                </li>
              ))}
            </ul>
          </div>
          <p className="text-[12px] leading-[1.5] text-text-faint">
            Nothing else is published. Not the resources' own names, not which server anything runs on, not a route
            domain, not a revision, and never the detail text the agent writes when something fails.
          </p>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" size="lg">
                Cancel
              </Button>
            </DialogClose>
            <ActionButton
              variant="primary"
              size="lg"
              state={pending ? "busy" : "idle"}
              busyLabel="Publishing…"
              onClick={() => {
                onConfirm();
                setOpen(false);
              }}
            >
              Publish this page
            </ActionButton>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Addresses({ projectId, page }: { projectId: string; page: StatusPage }) {
  const qc = useQueryClient();
  const servers = useListServers();
  const [title, setTitle] = useState(page.title);
  const [slug, setSlug] = useState(page.slug);
  const [domainValue, setDomainValue] = useState(page.domain);
  const [https, setHttps] = useState(page.https);
  const [serverId, setServerId] = useState(page.route_server_id);
  const [error, setError] = useState<string | null>(null);

  const check = useCheckStatusPageDomain(page.id, { query: { enabled: page.domain !== "" } });

  const save = useSetStatusPage({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetStatusPageQueryKey(projectId) });
        setError(null);
        toastSuccess("Saved");
      },
      onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not save"),
    },
  });

  const eligible = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const dirty =
    title !== page.title || slug !== page.slug || domainValue !== page.domain || https !== page.https || serverId !== page.route_server_id;

  return (
    <section className="space-y-2.5">
      <Eyebrow>Where it is served</Eyebrow>
      <div className="space-y-4 rounded-lg border border-border bg-surface px-4 py-4">
        <div className="space-y-1.5">
          <p className="text-[12.5px] text-text-mid">
            The panel's own address always works — no DNS, no certificate, nothing to set up.
          </p>
          <CopyField value={page.panel_url} />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Page title">
            {(id) => <Input id={id} value={title} onChange={(e) => setTitle(e.target.value)} />}
          </Field>
          <Field label="Address" qualifier="· public handle">
            {(id) => <Input id={id} value={slug} onChange={(e) => setSlug(e.target.value)} className="mono" spellCheck={false} />}
          </Field>
        </div>

        <Field
          label="Custom domain"
          qualifier="· optional"
          hint="Point a CNAME or A record at the server below, and the Proxy there serves this page with a certificate from the same account your applications use."
        >
          {(id, describedBy) => (
            <Input
              id={id}
              aria-describedby={describedBy}
              value={domainValue}
              onChange={(e) => setDomainValue(e.target.value)}
              className="mono"
              placeholder="status.example.com"
              spellCheck={false}
            />
          )}
        </Field>

        {domainValue !== "" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Served by" hint="The node whose Proxy answers for this domain. Point DNS at this server.">
              {(id) => (
                <select
                  id={id}
                  value={serverId}
                  onChange={(e) => setServerId(e.target.value)}
                  className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
                >
                  <option value="">Pick a server…</option>
                  {eligible.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <label className="flex items-end gap-2.5 pb-2.5 text-[13px] text-text">
              <input
                type="checkbox"
                checked={https}
                onChange={(e) => setHttps(e.currentTarget.checked)}
                className="size-3.5 accent-accent"
              />
              Serve over HTTPS
            </label>
          </div>
        )}

        {page.domain !== "" && check.data && <DomainCheckRow check={check.data} />}

        {error && (
          <p role="alert" className="text-[13px] text-danger">
            {error}
          </p>
        )}
        <div className="flex justify-end">
          <ActionButton
            variant="secondary"
            size="sm"
            state={save.isPending ? "busy" : "idle"}
            busyLabel="Saving…"
            disabledReason={
              !dirty
                ? "Nothing has changed"
                : domainValue !== "" && !serverId
                  ? "Pick the server that serves this domain"
                  : undefined
            }
            onClick={() =>
              save.mutate({
                id: projectId,
                data: {
                  title: title.trim(),
                  slug: slug.trim().toLowerCase(),
                  enabled: page.enabled,
                  domain: domainValue.trim().toLowerCase(),
                  https,
                  route_server_id: serverId,
                },
              })
            }
          >
            Save
          </ActionButton>
        </div>
      </div>
    </section>
  );
}

function Components({ projectId, page }: { projectId: string; page: StatusPage }) {
  const qc = useQueryClient();
  const saved = useListStatusPageComponents(page.id);
  const [rows, setRows] = useState<Draft[] | null>(null);

  // The server's list is the truth until the operator touches something; after
  // that the local draft is, so a background refetch cannot eat their typing.
  useEffect(() => {
    if (rows === null && saved.data) {
      setRows(
        saved.data.map((c: StatusPageComponent) => ({
          key: c.id,
          resource_kind: c.resource_kind,
          resource_id: c.resource_id,
          label: c.label,
          resourceName: "",
        })),
      );
    }
  }, [saved.data, rows]);

  const save = useSetStatusPageComponents({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListStatusPageComponentsQueryKey(page.id) });
        void qc.invalidateQueries({ queryKey: getPreviewStatusPageQueryKey(page.id) });
        toastSuccess("Components saved");
      },
      onError: (e: unknown, vars) => toastFailed("Could not save the components", e, { retry: () => save.mutate(vars) }),
    },
  });

  const list = rows ?? [];
  const dirty = useMemo(() => {
    const orig = (saved.data ?? []).map((c) => `${c.resource_kind}/${c.resource_id}/${c.label}`).join("|");
    const now = list.map((c) => `${c.resource_kind}/${c.resource_id}/${c.label}`).join("|");
    return orig !== now;
  }, [saved.data, list]);

  const move = (i: number, by: number) => {
    const j = i + by;
    if (j < 0 || j >= list.length) return;
    const a = list[i];
    const b = list[j];
    if (!a || !b) return;
    const next = [...list];
    next[i] = b;
    next[j] = a;
    setRows(next);
  };

  return (
    <section className="space-y-2.5">
      <div className="flex items-center gap-3">
        <Eyebrow>Components</Eyebrow>
        <span className="ml-auto">
          <AddComponentDialog
            projectId={projectId}
            taken={list.map((r) => `${r.resource_kind}/${r.resource_id}`)}
            onAdd={(d) => setRows([...list, d])}
          />
        </span>
      </div>

      <PageState
        query={saved}
        empty={
          <div className="rounded-lg border border-border bg-surface">
            <EmptyState
              glyph={null}
              className="py-6"
              title="Nothing on the page yet"
              hint="Pick the applications, stacks and databases customers care about. Each one gets a label you type — that label is the only name the public sees."
              action={
                <AddComponentDialog
                  projectId={projectId}
                  taken={[]}
                  primary
                  onAdd={(d) => setRows([...list, d])}
                />
              }
            />
          </div>
        }
        isEmpty={() => list.length === 0}
      >
        {() => (
          <ul className="divide-y divide-border-subtle overflow-hidden rounded-lg border border-border bg-surface">
            {list.map((row, i) => (
              <li key={row.key} className="flex items-center gap-2.5 px-3 py-2.5">
                <button
                  type="button"
                  aria-label={`Move ${row.label} up`}
                  onClick={() => move(i, -1)}
                  disabled={i === 0}
                  className="shrink-0 text-text-faint transition-colors hover:text-text disabled:opacity-30"
                >
                  <GripVertical className="h-3.5 w-3.5" aria-hidden />
                </button>
                <div className="min-w-0 flex-1">
                  <Input
                    value={row.label}
                    aria-label={`Public label for ${row.resourceName || row.resource_id}`}
                    onChange={(e) => {
                      const next = [...list];
                      next[i] = { ...row, label: e.target.value };
                      setRows(next);
                    }}
                    className="h-8"
                  />
                </div>
                {/* Dimmed, and never published — it is here so the operator can
                    see the thing they picked and the thing the internet reads,
                    and that they are not the same string. */}
                <span className="mono hidden shrink-0 text-[11px] text-text-faint sm:block" title="Never published">
                  {row.resource_kind.replace("_", " ")} · {row.resourceName || row.resource_id}
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove ${row.label}`}
                  className="shrink-0 px-2 text-danger"
                  onClick={() => setRows(list.filter((_, j) => j !== i))}
                >
                  <Trash2 className="h-3.5 w-3.5" aria-hidden />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </PageState>

      <div className="flex justify-end">
        <ActionButton
          variant="secondary"
          size="sm"
          state={save.isPending ? "busy" : "idle"}
          busyLabel="Saving…"
          disabledReason={
            !dirty ? "Nothing has changed" : list.some((r) => !r.label.trim()) ? "Every component needs a label" : undefined
          }
          onClick={() =>
            save.mutate({
              id: page.id,
              data: {
                components: list.map((r) => ({
                  resource_kind: r.resource_kind,
                  resource_id: r.resource_id,
                  label: r.label.trim(),
                })),
              },
            })
          }
        >
          Save components
        </ActionButton>
      </div>
    </section>
  );
}

function AddComponentDialog({
  projectId,
  taken,
  primary,
  onAdd,
}: {
  projectId: string;
  taken: string[];
  primary?: boolean;
  onAdd: (d: Draft) => void;
}) {
  const [open, setOpen] = useState(false);
  const envs = useListEnvironments(projectId);
  // Preview environments are refused by the API — they carry branch names and
  // pull request titles written by people outside the team — so they are not
  // offered here either.
  const standing = (envs.data ?? []).filter((e) => e.kind !== "preview");
  const [envId, setEnvId] = useState("");
  const active = envId || standing[0]?.id || "";

  const apps = useListApplications(active, { query: { enabled: open && active !== "" } });
  const stacks = useListComposeStacks(active, { query: { enabled: open && active !== "" } });
  const dbs = useListDatabases(active, { query: { enabled: open && active !== "" } });

  const options = [
    ...(apps.data ?? []).map((a) => ({ kind: "application", id: a.id, name: a.name })),
    ...(stacks.data ?? []).map((s) => ({ kind: "compose_stack", id: s.id, name: s.name })),
    ...(dbs.data ?? []).map((d) => ({ kind: "database", id: d.id, name: d.name })),
  ].filter((o) => !taken.includes(`${o.kind}/${o.id}`));

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant={primary ? "primary" : "secondary"} size={primary ? "lg" : "sm"}>
          <Plus className="h-3.5 w-3.5" aria-hidden /> Add component
        </Button>
      </DialogTrigger>
      <DialogContent
        title="Add a component"
        description="Pick what to report on. You give it a public label next — the resource's own name is never published."
      >
        <div className="space-y-3">
          {standing.length > 1 && (
            <Field label="Environment">
              {(id) => (
                <select
                  id={id}
                  value={active}
                  onChange={(e) => setEnvId(e.target.value)}
                  className="w-full rounded-md border border-border-input bg-surface px-3 py-2 text-[13px] text-text focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong focus-visible:outline-none"
                >
                  {standing.map((e) => (
                    <option key={e.id} value={e.id}>
                      {e.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          )}
          {options.length === 0 ? (
            <p className="py-4 text-[13px] text-text-mid">
              Nothing left to add in this environment.
            </p>
          ) : (
            <ul className="max-h-72 divide-y divide-border-subtle overflow-y-auto rounded-md border border-border">
              {options.map((o) => (
                <li key={`${o.kind}/${o.id}`}>
                  <button
                    type="button"
                    className="flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left transition-colors hover:bg-surface-hover"
                    onClick={() => {
                      onAdd({
                        key: `${o.kind}/${o.id}`,
                        resource_kind: o.kind as Draft["resource_kind"],
                        resource_id: o.id,
                        // Seeded from the resource name because a blank field
                        // is a save that fails. It is editable right after, and
                        // the row says the resource's own name is not published.
                        label: o.name,
                        resourceName: o.name,
                      });
                      setOpen(false);
                    }}
                  >
                    <span className="text-[13px] text-text">{o.name}</span>
                    <span className="mono text-[11px] text-text-faint">{o.kind.replace("_", " ")}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Preview({ page }: { page: StatusPage }) {
  const preview = usePreviewStatusPage(page.id);
  return (
    <section className="space-y-2.5">
      <Eyebrow>Preview</Eyebrow>
      <p className="text-[12.5px] leading-[1.5] text-text-mid">
        Exactly what a visitor gets — the same code renders both. {page.enabled ? "This is live right now." : "Nothing here is published yet."}
      </p>
      <PageState query={preview} isEmpty={() => false} skeletonRows={3}>
        {(payload: PublicStatusPage) => <StatusPagePreview page={payload} />}
      </PageState>
    </section>
  );
}

function DangerZone({ projectId, page }: { projectId: string; page: StatusPage }) {
  const qc = useQueryClient();
  const del = useDeleteStatusPage({
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getGetStatusPageQueryKey(projectId) });
        toastSuccess("Status page deleted");
      },
      onError: (e: unknown, vars) => toastFailed("Could not delete the page", e, { retry: () => del.mutate(vars) }),
    },
  });

  return (
    <section className="space-y-2.5">
      <Eyebrow>Danger zone</Eyebrow>
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-danger/30 bg-surface px-4 py-3.5">
        <div className="min-w-0">
          <p className="text-[13px] font-semibold text-text">Delete this status page</p>
          <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
            The page stops answering and its uptime history is deleted with it.
          </p>
        </div>
        <ConfirmDestructive
          trigger={
            <Button size="sm" variant="secondary" className="text-danger">
              Delete page
            </Button>
          }
          title={`Delete the ${page.title} status page?`}
          lead="Deleting this page:"
          blastRadius={[
            "both public addresses stop answering within a few seconds",
            "the thirty days of recorded uptime are deleted and cannot be recovered",
            "the Proxy fragment on the serving node is removed at its next reconcile",
          ]}
          confirmName={page.slug}
          actionLabel="Delete status page"
          pendingLabel="Deleting…"
          pending={del.isPending}
          onConfirm={() => del.mutate({ id: projectId })}
        />
      </div>
    </section>
  );
}
