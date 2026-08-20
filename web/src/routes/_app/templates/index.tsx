import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Database, Package, Search, ShieldCheck, X } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { Template } from "@/api/gen/model";
import { useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import { useInstallTemplate, useListTemplates } from "@/api/gen/templates/templates";
import { EmptyState } from "@/components/empty-state";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
import { ActionButton } from "@/components/ui/action-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input, Select } from "@/components/ui/input";
import { useCrumbs } from "@/lib/crumbs";

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
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search templates…" className="pl-9" />
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

// Nothing installs blind (ui-principles: show what will happen). Every
// resource, mount, and configuration key this template creates is listed
// before the operator commits — the data is already in the catalog response.
//
// Canvas 12b heads the box with an eyebrow and closes it with the promise the
// whole modal rests on: the things nobody has to type. That closing line is
// assembled from what this template actually does, because "TLS + subdomain"
// is a lie on a template that publishes nothing.
function TemplateContents({ template }: { template: Template }) {
  const handled = [
    needsDomain(template) && "TLS + subdomain",
    template.resources.databases.length > 0 && "generated secrets",
  ].filter(Boolean) as string[];
  return (
    <div className="rounded-lg border border-border bg-surface px-3.5 py-[11px] text-[12px] leading-[1.8] text-text-dim">
      <div className="eyebrow mb-1">The template brings</div>
      {template.resources.databases.map((db) => (
        <div key={db.name} className="flex items-baseline gap-2">
          <Database className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
          <span className="font-medium">{db.name}</span>
          <span className="font-mono text-[11px] text-text-faint">
            {db.engine}
            {db.version ? ` ${db.version}` : ""} · generated password
          </span>
        </div>
      ))}
      {template.resources.applications.map((app) => {
        const envKeys = Object.keys(app.env ?? {});
        return (
          <div key={app.name}>
            <div className="flex items-baseline gap-2">
              <Package className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
              <span className="font-medium">{app.name}</span>
              <span className="truncate font-mono text-[11px] text-text-faint">
                {app.image} · port {app.port}
                {app.route ? " · routed" : ""}
              </span>
            </div>
            {(app.volumes ?? []).map((v) => (
              <p key={v.path} className="pl-5 font-mono text-[11px] text-text-faint">
                volume {v.name} → {v.path}
              </p>
            ))}
            {envKeys.length > 0 && (
              <p className="pl-5 font-mono text-[11px] text-text-faint">
                sets {count(envKeys.length, "variable")}: {envKeys.slice(0, 4).join(", ")}
                {envKeys.length > 4 ? `, +${envKeys.length - 4} more` : ""}
              </p>
            )}
          </div>
        );
      })}
      {handled.length > 0 && (
        <div className="flex items-baseline gap-2">
          <ShieldCheck className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
          <span>{handled.join(" + ")} — nothing to fill in</span>
        </div>
      )}
    </div>
  );
}

function InstallDialog({ template }: { template: Template }) {
  const navigate = useNavigate();
  const projects = useListProjects();
  const servers = useListServers();
  const [projectID, setProjectID] = useState("");
  const [environmentID, setEnvironmentID] = useState("");
  const [serverID, setServerID] = useState("");
  const [name, setName] = useState("");
  const [domain, setDomain] = useState("");
  const [error, setError] = useState<string | null>(null);
  const environments = useListEnvironments(projectID, { query: { enabled: projectID !== "" } });
  const install = useInstallTemplate({ mutation: {
    onSuccess: (result) => {
      const appID = result.applications[0];
      if (appID) void navigate({ to: "/projects/$projectId/applications/$appId", params: { projectId: projectID, appId: appID } });
    },
    onError: (e: unknown) => setError(e instanceof Error ? e.message : "Could not install template"),
  }});
  const routed = needsDomain(template);

  // A gated button that does not say what is missing is a dead end (10b), and
  // the answers here are always one specific empty select.
  const blocked =
    !projectID ? "Pick a project first"
    : !environmentID ? "Pick an environment"
    : !serverID ? "Pick a server"
    : routed && domain.trim() === "" ? "This template publishes a public URL, so it needs a domain"
    : undefined;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    install.mutate({ slug: template.slug, data: { environment_id: environmentID, server_id: serverID, name: name || undefined, domain: domain || undefined } });
  };

  // `automation · postgresql · v1.94` — 12b's identity line. Every segment is
  // optional in the catalog schema, so each is dropped rather than emitting a
  // dangling separator, exactly as the card does with the version.
  const meta = [
    template.category,
    [...new Set(template.resources.databases.map((db) => db.engine))].join(" + "),
    template.version && `v${template.version}`,
  ].filter(Boolean).join(" · ");

  return (
    <Dialog>
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
            {/* Two columns at the canvas's 430px, one on a phone: paired
                selects in a 360px-wide sheet are narrower than their own
                option labels. */}
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Project">{(id) => <Select id={id} required value={projectID} onChange={(e) => { setProjectID(e.target.value); setEnvironmentID(""); }} className="font-sans"><option value="">Select a project</option>{(projects.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select>}</Field>
              <Field label="Server">{(id) => <Select id={id} required value={serverID} onChange={(e) => setServerID(e.target.value)} className="font-sans"><option value="">Select a server</option>{(servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder").map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select>}</Field>
              <Field label="Environment">{(id) => <Select id={id} required disabled={!projectID} value={environmentID} onChange={(e) => setEnvironmentID(e.target.value)} className="font-sans"><option value="">Select an environment</option>{(environments.data ?? []).map((env) => <option key={env.id} value={env.id}>{env.name}</option>)}</Select>}</Field>
              <Field label="Name" hint={`Defaults to ${template.slug}`}>{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder={template.slug} />}</Field>
            </div>
            {/* Every routed template needs a domain — the server refuses without
                one, because resolving {{domain}} to "" would write settings like
                https:/// into the container. Ask for it as required rather than
                letting the form advertise a default that always fails. */}
            {routed && (
              <Field label="Domain" hint="Required — this template publishes a public URL. TLS is automatic.">
                {(id) => (
                  <Input
                    id={id}
                    required
                    value={domain}
                    onChange={(e) => setDomain(e.target.value)}
                    placeholder={`${template.slug}.example.com`}
                  />
                )}
              </Field>
            )}
            {/* The summary sits last, immediately above the commit: it is what
                the operator re-reads with a finger on the button, not preamble. */}
            <TemplateContents template={template} />
            {error && <p className="text-sm text-danger">{error}</p>}
            <div className="flex justify-end gap-2.5 pt-1">
              <DialogClose asChild><Button variant="ghost" size="lg">Cancel</Button></DialogClose>
              <ActionButton
                type="submit"
                variant="accent"
                size="lg"
                state={install.isPending ? "busy" : "idle"}
                busyLabel="Installing…"
                disabledReason={blocked}
              >
                Install →
              </ActionButton>
            </div>
          </form>
        </div>
      </DialogContent>
    </Dialog>
  );
}
