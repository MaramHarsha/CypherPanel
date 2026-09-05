import { useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Database, Package, Search, X } from "lucide-react";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { getListApplicationsQueryKey } from "@/api/gen/applications/applications";
import { getListDatabasesQueryKey } from "@/api/gen/databases/databases";
import type { FirstLogin, Template } from "@/api/gen/model";
import { useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import { useInstallTemplate, useListTemplates } from "@/api/gen/templates/templates";
import { AdvancedSection } from "@/components/advanced-section";
import { EmptyState } from "@/components/empty-state";
import { FirstLoginNotice } from "@/components/first-login-notice";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { ActionButton, useMutationActionState } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";
import { toastFailed } from "@/lib/toast";

export const Route = createFileRoute("/_app/templates/")({ component: TemplatesPage });

/** "1 app" / "2 apps" — counts read as prose, never "1 database(s)". */
function count(n: number, singular: string, plural = `${singular}s`) {
  return `${n} ${n === 1 ? singular : plural}`;
}

const ALL = "all";

/**
 * Mirrors the server's Template.needsDomain: a routed app, or any value that
 * interpolates {{domain}}, makes the domain mandatory. Both the form and the
 * "what you get" summary answer to it, so it lives outside them.
 */
function needsDomain(template: Template) {
  return template.resources.applications.some(
    // Whitespace is legal inside a placeholder ({{ domain }}), so match the
    // grammar rather than a literal — the server does the same.
    (app) => app.route || Object.values(app.env ?? {}).some((v) => /\{\{\s*domain\s*\}\}/.test(v)),
  );
}

function TemplatesPage() {
  useCrumbs([{ label: "templates" }]);
  const navigate = useNavigate();
  const templates = useListTemplates();
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState<string>(ALL);

  // The catalog is bundled static content that arrives in one response, so
  // narrowing it happens here rather than server-side (ui-principles §7's
  // pagination rule is about live tables, not a shipped list).
  const categories = useMemo(() => {
    const counts = new Map<string, number>();
    for (const t of templates.data ?? []) counts.set(t.category, (counts.get(t.category) ?? 0) + 1);
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [templates.data]);

  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return (templates.data ?? []).filter(
      (t) =>
        (category === ALL || t.category === category) &&
        (!needle || `${t.name} ${t.slug} ${t.description} ${t.category}`.toLowerCase().includes(needle)),
    );
  }, [search, category, templates.data]);

  return (
    <>
      <PageHeader title="Templates" />
      <PageBody>
        <div className="relative mb-4 max-w-md">
          <Search className="absolute left-3 top-2.5 h-4 w-4 text-text-faint" aria-hidden />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search templates…"
            aria-label="Search templates"
            className="pl-9"
          />
        </div>
        {categories.length > 1 && (
          <div className="mb-5 flex flex-wrap gap-1.5" role="group" aria-label="Filter by category">
            <CategoryChip label="All" count={(templates.data ?? []).length} active={category === ALL} onSelect={() => setCategory(ALL)} />
            {categories.map(([name, n]) => (
              <CategoryChip key={name} label={name} count={n} active={category === name} onSelect={() => setCategory(name)} />
            ))}
          </div>
        )}
        <PageState
          query={templates}
          empty={<EmptyState title="No templates in this release" hint="The catalog is bundled with the control plane." />}
        >
          {(all) => filtered.length === 0 ? (
            <SearchMiss
              query={search.trim()}
              category={category}
              catalogSize={all.length}
              onClearCategory={() => setCategory(ALL)}
              onDeployFromRepo={() => void navigate({ to: "/projects" })}
            />
          ) : (
            <ul className="grid gap-3.5 md:grid-cols-2 xl:grid-cols-3">
              {filtered.map((template) => <TemplateCard key={template.slug} template={template} />)}
            </ul>
          )}
        </PageState>
      </PageBody>
    </>
  );
}

// Two different misses, and confusing them is expensive. With a category chip
// on, the query almost certainly missed *because* of the chip — WordPress is in
// the catalog, just filed under cms — so that case takes 15a's filter-miss
// shape and points back at the chip, and never offers to file a request for a
// template that already exists. Only once nothing is filtered out is "this
// doesn't exist yet" a true statement, and only then is a request worth making.
//
// Neither shape offers to clear the search: the query is still in the box
// above, so undoing that is already one click away in place.
//
// The catalog always has at least one template per chip and PageState owns the
// empty catalog, so `query` is never blank by the time this renders.
function SearchMiss({
  query,
  category,
  catalogSize,
  onClearCategory,
  onDeployFromRepo,
}: {
  query: string;
  category: string;
  catalogSize: number;
  onClearCategory: () => void;
  onDeployFromRepo: () => void;
}) {
  const requestUrl = `https://github.com/MaramHarsha/CypherPanel/issues/new?${new URLSearchParams({
    title: `Template request: ${query}`,
    body: `What it is:\n\nUpstream image or compose file:\n`,
  }).toString()}`;
  if (category !== ALL) {
    return (
      <EmptyState
        glyph="≡"
        title={`No ${category} template matches "${query}"`}
        hint={`The rest of the catalog is filtered out — ${count(catalogSize, "template")} is the bar, and most of them are not ${category}.`}
        action={
          <>
            <Button variant="secondary" size="sm" onClick={onClearCategory}>
              Search all templates
            </Button>
            <Button variant="primary" size="sm" onClick={onDeployFromRepo}>
              Deploy from a repo instead
            </Button>
          </>
        }
      />
    );
  }
  return (
    <EmptyState
      glyph="▢?"
      title={`No template called "${query}" yet`}
      hint={`${count(catalogSize, "template")} is the bar and the catalog is community-driven.`}
      action={
        <>
          <Button variant="secondary" size="sm" onClick={() => window.open(requestUrl, "_blank", "noopener,noreferrer")}>
            Request it ↗
          </Button>
          <Button variant="primary" size="sm" onClick={onDeployFromRepo}>
            Deploy from a repo instead
          </Button>
        </>
      }
    />
  );
}

function CategoryChip({ label, count, active, onSelect }: { label: string; count: number; active: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onSelect}
      // Selection is ink, never accent: the orange on this page belongs to the
      // install pill and the top-bar nav, and a filter is not either of those.
      className={`rounded-full px-3 py-1 font-mono text-[11px] uppercase tracking-wide transition-colors ${
        active
          ? "border-[1.5px] border-border-strong bg-surface font-medium text-text"
          : "border border-border bg-surface text-text-mid hover:border-text-faint"
      }`}
    >
      {label} <span className="text-text-faint">{count}</span>
    </button>
  );
}

function TemplateCard({ template }: { template: Template }) {
  return (
    <li className="flex flex-col rounded-lg border border-border bg-surface p-5">
      <div className="flex items-start gap-3">
        <span className="rounded-md bg-raised p-2 text-text-faint"><Package className="h-4 w-4" /></span>
        <div className="min-w-0">
          <p className="text-[17px] font-semibold">{template.name}</p>
          {/* Version is optional in the schema: an imported template whose
              upstream image ships only a moving tag is pinned by digest and
              has no version to name. Show the category alone rather than a
              dangling separator. */}
          <p className="font-mono text-[10px] uppercase tracking-wide text-text-faint">
            {template.category}
            {template.version ? ` · ${template.version}` : ""}
          </p>
        </div>
      </div>
      <p className="mt-3 flex-1 text-[13px] leading-relaxed text-text-mid">{template.description}</p>
      <p className="mt-4 flex items-center gap-1.5 font-mono text-[11px] text-text-faint">
        <Package className="h-3 w-3" /> {count(template.resources.applications.length, "app")}
        <span className="mx-1">·</span>
        <Database className="h-3 w-3" /> {count(template.resources.databases.length, "database", "databases")}
      </p>
      <div className="mt-4"><InstallDialog template={template} /></div>
    </li>
  );
}

// Nothing installs blind (ui-principles: show what will happen). Canvas 12b
// heads the box with an eyebrow and lists what the operator ends up with as
// three outcome-shaped lines — the containers, the volumes, and the things
// nobody has to type. The mechanism behind them (images, ports, every variable
// the template sets) is still here, one click deeper, for whoever wants it
// (ui-principles §11: outcomes in the headline, mechanism in the expander).
//
// The closing promise is assembled from what this template actually does,
// because "TLS + subdomain" is a lie on a template that publishes nothing.
function TemplateContents({ template }: { template: Template }) {
  const apps = template.resources.applications;
  const dbs = template.resources.databases;
  const handled = [
    needsDomain(template) && "TLS + subdomain",
    dbs.length > 0 && "generated secrets",
  ].filter(Boolean) as string[];

  // "app container (n8n) + postgresql 16 sidecar"
  const brings = [
    apps.length > 0 && `app container (${apps.map((app) => app.name).join(", ")})`,
    ...dbs.map((db) => `${db.engine}${db.version ? ` ${db.version}` : ""} sidecar`),
  ].filter(Boolean) as string[];
  const lines = [
    brings.length > 0 && brings.join(" + "),
    ...apps.flatMap((app) => (app.volumes ?? []).map((v) => `volume ${v.name} → ${v.path}`)),
    handled.length > 0 && `${handled.join(" + ")} — nothing to fill in`,
  ].filter(Boolean) as string[];

  return (
    <div className="rounded-lg border border-border bg-surface px-3.5 py-[11px] text-[12px] leading-[1.8] text-text-dim">
      <div className="eyebrow mb-1">The template brings</div>
      <ul>
        {lines.map((line) => (
          <li key={line} className="flex gap-2">
            <span aria-hidden>●</span>
            <span className="min-w-0 break-words">{line}</span>
          </li>
        ))}
      </ul>
      <details className="group mt-1">
        <summary className="cursor-pointer list-none font-mono text-[10.5px] text-text-faint hover:text-text-mid [&::-webkit-details-marker]:hidden">
          exactly what it creates <span className="group-open:hidden">▾</span>
          <span className="hidden group-open:inline">▴</span>
        </summary>
        <ul className="mt-1 space-y-0.5 font-mono text-[11px] text-text-faint">
          {dbs.map((db) => (
            <li key={db.name} className="truncate">
              {db.name} · {db.engine}
              {db.version ? ` ${db.version}` : ""} · generated password
            </li>
          ))}
          {apps.map((app) => {
            const envKeys = Object.keys(app.env ?? {});
            return (
              <li key={app.name} className="min-w-0">
                <span className="block truncate">
                  {app.name} · {app.image} · port {app.port}
                  {app.route ? " · routed" : ""}
                </span>
                {envKeys.length > 0 && (
                  <span className="block truncate">
                    sets {count(envKeys.length, "variable")}: {envKeys.join(", ")}
                  </span>
                )}
              </li>
            );
          })}
        </ul>
      </details>
    </div>
  );
}

/**
 * One select over one list query, with the four states the list can be in
 * (ui-principles §1) drawn in the control's own footprint: a disabled,
 * `aria-busy` select while the list loads; the failure with its retry beside
 * it; an empty list with the remedy under it — "Join a server first" — rather
 * than a placeholder that looks like a choice nobody made; and the options.
 */
function ListSelect<T extends { id: string; name: string }, E>({
  id,
  describedBy,
  query,
  items,
  value,
  onChange,
  noun,
  remedy,
}: {
  id: string;
  describedBy: string | undefined;
  query: UseQueryResult<T[], E>;
  /** The options — the query's rows, possibly narrowed by the caller. */
  items: T[];
  value: string;
  onChange: (id: string) => void;
  noun: string;
  /** What to do about an empty list — a Link to where the first one is made. */
  remedy: ReactNode;
}) {
  if (query.isPending) {
    return (
      <Select id={id} disabled aria-busy aria-describedby={describedBy} className="font-sans">
        <option>Loading {noun}s…</option>
      </Select>
    );
  }
  if (query.isError) {
    return (
      <div
        role="alert"
        className="flex h-9 items-center justify-between gap-2 rounded-md border border-danger/35 bg-danger/[0.06] pl-3 pr-1 text-[12px] text-danger"
      >
        <span className="truncate">{capitalize(noun)}s could not be loaded</span>
        <ActionButton
          size="sm"
          variant="ghost"
          state={query.isFetching ? "busy" : "idle"}
          busyLabel="Retrying…"
          onClick={() => void query.refetch()}
        >
          Retry
        </ActionButton>
      </div>
    );
  }
  if (items.length === 0) {
    return (
      <>
        <Select id={id} disabled aria-describedby={describedBy} className="font-sans">
          <option>No {noun}s yet</option>
        </Select>
        <p className="text-xs leading-relaxed text-text-faint">{remedy}</p>
      </>
    );
  }
  return (
    <Select id={id} required value={value} onChange={(e) => onChange(e.target.value)} aria-describedby={describedBy} className="font-sans">
      <option value="">Select a {noun}</option>
      {items.map((item) => (
        <option key={item.id} value={item.id}>
          {item.name}
        </option>
      ))}
    </Select>
  );
}

function capitalize(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The link that closes an empty list's dead end (ui-principles §11). */
function RemedyLink({ to, children }: { to: "/projects" | "/servers"; children: ReactNode }) {
  return (
    <Link to={to} className="font-medium text-text-mid underline-offset-2 hover:text-text hover:underline">
      {children} →
    </Link>
  );
}

function InstallDialog({ template }: { template: Template }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const projects = useListProjects();
  const servers = useListServers();
  const [open, setOpen] = useState(false);
  const [projectID, setProjectID] = useState("");
  const [environmentID, setEnvironmentID] = useState("");
  const [serverID, setServerID] = useState("");
  const [name, setName] = useState("");
  const [domain, setDomain] = useState("");
  const [error, setError] = useState<string | null>(null);
  // What the 202 handed back, held until the first-login notice is dismissed.
  const [installed, setInstalled] = useState<{ first: FirstLogin; appID: string | undefined } | null>(null);

  // 12b asks only where: Project and Server. With exactly one project there
  // was never a choice to make, and the first enrolled server is the working
  // default every create form shows filled in (ui-principles §6). A builder-
  // only agent rejects rollout work, so it is never offered.
  const projectList = projects.data ?? [];
  const chosenProjectID = projectID || (projectList.length === 1 ? (projectList[0]?.id ?? "") : "");
  const enrolled = (servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder");
  const chosenServerID = serverID || enrolled[0]?.id || "";

  // The environment defaults to production — the one every project starts
  // with — else the first; it is a real choice only on a project that has
  // grown a staging, which is why it lives under Advanced.
  const environments = useListEnvironments(chosenProjectID, { query: { enabled: chosenProjectID !== "" } });
  const envList = environments.data ?? [];
  const defaultEnv = envList.find((env) => env.name === "production") ?? envList[0];
  const chosenEnvID = environmentID || defaultEnv?.id || "";
  const chosenEnv = envList.find((env) => env.id === chosenEnvID);

  const goTo = (appID: string | undefined) => {
    if (appID) void navigate({ to: "/projects/$projectId/applications/$appId", params: { projectId: chosenProjectID, appId: appID } });
  };

  const install = useInstallTemplate({ mutation: {
    onSuccess: (result, vars) => {
      // An install drops whole resources into the environment, and the project
      // page renders those lists from cache — so they are marked stale before
      // the navigation that lands on them, not after.
      void qc.invalidateQueries({ queryKey: getListApplicationsQueryKey(vars.data.environment_id) });
      void qc.invalidateQueries({ queryKey: getListDatabasesQueryKey(vars.data.environment_id) });
      const appID = result.applications[0];
      // A generated password appears in this response and nowhere else ever, so
      // navigating straight past it would destroy it. Hold the navigation until
      // the notice is dismissed; with nothing to say, go as before.
      if (result.first_login) {
        setInstalled({ first: result.first_login, appID });
        return;
      }
      goTo(appID);
    },
    // The pill turns to "✕ Retry" and the toast carries the why (10b/10c); the
    // inline line keeps the server's sentence beside the form it is about.
    onError: (e: unknown, vars) => {
      setError(e instanceof Error ? e.message : "Could not install the template");
      toastFailed(`Could not install ${template.name}`, e, { retry: () => install.mutate(vars) });
    },
  }});
  const state = useMutationActionState(install);

  const routed = needsDomain(template);

  // A gated button that does not say what is missing is a dead end (10b), and
  // the answer here is always one specific control above it.
  const blocked =
    projects.isPending ? "Loading projects…"
    : projects.isError ? "Projects could not be loaded — retry above"
    : projectList.length === 0 ? "Create a project first"
    : !chosenProjectID ? "Pick a project first"
    : servers.isPending ? "Loading servers…"
    : servers.isError ? "Servers could not be loaded — retry above"
    : enrolled.length === 0 ? "Join a server first"
    : !chosenServerID ? "Pick a server"
    : environments.isPending ? "Loading environments…"
    : environments.isError ? "Environments could not be loaded — retry under Advanced"
    : !chosenEnvID ? "This project has no environment yet"
    : routed && domain.trim() === "" ? "This template publishes a public URL, so it needs a domain"
    : undefined;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    install.mutate({
      slug: template.slug,
      data: { environment_id: chosenEnvID, server_id: chosenServerID, name: name.trim() || undefined, domain: domain.trim() || undefined },
    });
  };

  // Opening resets the mutation as well as the fields, so a reopened modal
  // never inherits the last attempt's "✕ Retry" pill or its error line.
  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) {
      install.reset();
      return;
    }
    setProjectID("");
    setEnvironmentID("");
    setServerID("");
    setName("");
    setDomain("");
    setError(null);
  };

  // `automation · postgresql · v1.94` — 12b's identity line. Every segment is
  // optional in the catalog schema, so each is dropped rather than emitting a
  // dangling separator, exactly as the card does with the version. (The canvas
  // names the runtime — "node + postgres" — which the schema does not carry.)
  const meta = [
    template.category,
    [...new Set(template.resources.databases.map((db) => db.engine))].join(" + "),
    template.version && `v${template.version}`,
  ].filter(Boolean).join(" · ");

  return (
    <>
      {/* The first-login notice is its own dialog, so this one yields to it
          rather than stacking underneath (ui-principles §4: modal depth is 1).
          The form's state survives the swap because this component does. */}
      <Dialog open={open && !installed} onOpenChange={onOpenChange}>
        <DialogTrigger asChild><Button variant="primary" className="w-full">Install {template.name}</Button></DialogTrigger>
        {/* 12b/13au opens on the template's own identity rather than on a form:
            the monogram tile and the mono meta line are what make this read as
            "installing n8n". The shared masthead is zeroed and rebuilt here for
            that — the accessible name still comes from `title`. */}
        <DialogContent title={`Install ${template.name}`} hideTitle size="alert" className="[&>div:first-child]:p-0">
          <div className="pt-6">
            <div className="mb-3.5 flex items-center gap-3">
              <span className="flex size-[38px] flex-none items-center justify-center rounded-[9px] bg-accent font-mono text-[15px] font-normal text-accent-fg" aria-hidden>
                {template.slug.slice(0, 2)}
              </span>
              <div className="min-w-0">
                <div className="text-[17px] font-bold tracking-[-0.02em] text-text">Install {template.name}</div>
                <div className="truncate font-mono text-[11px] text-text-faint">{meta}</div>
              </div>
              <DialogClose aria-label="Close" className="ml-auto shrink-0 rounded p-0.5 text-text-faint hover:text-text">
                <X className="h-4 w-4" />
              </DialogClose>
            </div>

            <form onSubmit={submit} className="space-y-3">
              {/* Locked while the install is in flight: a second submit would
                  install a second copy (10a). */}
              <fieldset disabled={install.isPending} className="min-w-0 space-y-3">
                {/* Two columns at the canvas's 430px, one on a phone: paired
                    selects in a 360px-wide sheet are narrower than their own
                    option labels. */}
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Project">
                    {(id, describedBy) => (
                      <ListSelect
                        id={id}
                        describedBy={describedBy}
                        query={projects}
                        items={projectList}
                        value={chosenProjectID}
                        onChange={(next) => { setProjectID(next); setEnvironmentID(""); }}
                        noun="project"
                        remedy={<RemedyLink to="/projects">Create a project first</RemedyLink>}
                      />
                    )}
                  </Field>
                  <Field label="Server">
                    {(id, describedBy) => (
                      <ListSelect
                        id={id}
                        describedBy={describedBy}
                        query={servers}
                        items={enrolled}
                        value={chosenServerID}
                        onChange={setServerID}
                        noun="server"
                        remedy={<RemedyLink to="/servers">Join a server first</RemedyLink>}
                      />
                    )}
                  </Field>
                </div>
                {/* Every routed template needs a domain — the server refuses without
                    one, because resolving {{domain}} to "" would write settings like
                    https:/// into the container. Ask for it as required rather than
                    letting the form advertise a default that always fails. */}
                {routed && (
                  <Field label="Domain" hint="Required — this template publishes a public URL. TLS is automatic.">
                    {(id, describedBy) => (
                      <Input
                        id={id}
                        required
                        aria-describedby={describedBy}
                        value={domain}
                        onChange={(e) => setDomain(e.target.value)}
                        placeholder={`${template.slug}.example.com`}
                      />
                    )}
                  </Field>
                )}
                {/* Both have a working default — production, and the template's
                    own slug — so they fold (ui-principles §6); the note says what
                    the fold will do if left alone. */}
                <AdvancedSection note={`${chosenEnv?.name ?? "…"} · ${name.trim() || template.slug}`}>
                  <Field label="Environment" hint="Defaults to production.">
                    {(id, describedBy) =>
                      chosenProjectID === "" ? (
                        <Select id={id} disabled aria-describedby={describedBy} className="font-sans">
                          <option>Pick a project first</option>
                        </Select>
                      ) : (
                        <ListSelect
                          id={id}
                          describedBy={describedBy}
                          query={environments}
                          items={envList}
                          value={chosenEnvID}
                          onChange={setEnvironmentID}
                          noun="environment"
                          remedy="Every project starts with one — this one has none. Open the project to add an environment."
                        />
                      )
                    }
                  </Field>
                  <Field label="Name" hint={`Defaults to ${template.slug}`}>
                    {(id, describedBy) => (
                      <Input id={id} aria-describedby={describedBy} value={name} onChange={(e) => setName(e.target.value)} placeholder={template.slug} />
                    )}
                  </Field>
                </AdvancedSection>
                {/* The summary sits last, immediately above the commit: it is what
                    the operator re-reads with a finger on the button, not preamble. */}
                <TemplateContents template={template} />
              </fieldset>
              {error && <p className="text-sm text-danger" aria-live="polite">{error}</p>}
              <div className="flex justify-end gap-2.5 pt-1">
                <DialogClose asChild><Button variant="ghost" size="lg">Cancel</Button></DialogClose>
                <ActionButton
                  type="submit"
                  variant="accent"
                  size="lg"
                  state={state}
                  busyLabel="Installing…"
                  successLabel="Installed"
                  disabledReason={blocked}
                >
                  Install →
                </ActionButton>
              </div>
            </form>
          </div>
        </DialogContent>
      </Dialog>
      {installed && (
        <FirstLoginNotice
          first={installed.first}
          templateName={template.name}
          onContinue={() => {
            const { appID } = installed;
            setInstalled(null);
            setOpen(false);
            goTo(appID);
          }}
        />
      )}
    </>
  );
}
