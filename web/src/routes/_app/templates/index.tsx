import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Database, Package, Search } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import type { Template } from "@/api/gen/model";
import { useListEnvironments, useListProjects } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import { useInstallTemplate, useListTemplates } from "@/api/gen/templates/templates";
import { EmptyState } from "@/components/empty-state";
import { PageBody, PageHeader } from "@/components/page-header";
import { PageState } from "@/components/page-state";
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

function TemplatesPage() {
  useCrumbs([{ label: "templates" }]);
  const templates = useListTemplates();
  const [search, setSearch] = useState("");
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return (templates.data ?? []).filter(
      (t) => !needle || `${t.name} ${t.description} ${t.category}`.toLowerCase().includes(needle),
    );
  }, [search, templates.data]);

  return (
    <>
      <PageHeader title="Templates" />
      <PageBody>
        <div className="relative mb-5 max-w-md">
          <Search className="absolute left-3 top-2.5 h-4 w-4 text-text-faint" aria-hidden />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search templates…" className="pl-9" />
        </div>
        <PageState
          query={templates}
          empty={<EmptyState title="No templates in this release" hint="The catalog is bundled with the control plane." />}
        >
          {() => filtered.length === 0 ? (
            <EmptyState title="No matching templates" hint="Try a name, category, or a broader search." />
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

function TemplateCard({ template }: { template: Template }) {
  return (
    <li className="flex flex-col rounded-lg border border-border bg-surface p-5">
      <div className="flex items-start gap-3">
        <span className="rounded-md bg-raised p-2 text-accent"><Package className="h-4 w-4" /></span>
        <div className="min-w-0">
          <p className="text-[17px] font-semibold">{template.name}</p>
          <p className="font-mono text-[10px] uppercase tracking-wide text-text-faint">{template.category} · {template.version}</p>
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
function TemplateContents({ template }: { template: Template }) {
  return (
    <div className="mb-4 space-y-2.5 rounded-md border border-border bg-raised/60 p-3.5">
      {template.resources.databases.map((db) => (
        <div key={db.name} className="flex items-baseline gap-2 text-[12px]">
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
          <div key={app.name} className="space-y-1">
            <div className="flex items-baseline gap-2 text-[12px]">
              <Package className="h-3 w-3 shrink-0 text-text-faint" aria-hidden />
              <span className="font-medium">{app.name}</span>
              <span className="truncate font-mono text-[11px] text-text-faint">
                {app.image} · port {app.port}
                {app.route ? " · routed" : ""}
              </span>
            </div>
            {(app.volumes ?? []).length > 0 && (
              <p className="pl-5 font-mono text-[11px] text-text-faint">
                volumes: {(app.volumes ?? []).map((v) => v.path).join(", ")}
              </p>
            )}
            {envKeys.length > 0 && (
              <p className="pl-5 font-mono text-[11px] text-text-faint">
                sets {count(envKeys.length, "variable")}: {envKeys.slice(0, 4).join(", ")}
                {envKeys.length > 4 ? `, +${envKeys.length - 4} more` : ""}
              </p>
            )}
          </div>
        );
      })}
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
  // Mirrors the server's Template.needsDomain: a routed app, or any value that
  // interpolates {{domain}}, makes the domain mandatory.
  const needsDomain = template.resources.applications.some(
    // Whitespace is legal inside a placeholder ({{ domain }}), so match the
    // grammar rather than a literal — the server does the same.
    (app) => app.route || Object.values(app.env ?? {}).some((v) => /\{\{\s*domain\s*\}\}/.test(v)),
  );

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    install.mutate({ slug: template.slug, data: { environment_id: environmentID, server_id: serverID, name: name || undefined, domain: domain || undefined } });
  };

  return (
    <Dialog>
      <DialogTrigger asChild><Button variant="primary" className="w-full">Install {template.name}</Button></DialogTrigger>
      <DialogContent
        title={`Install ${template.name}`}
        description={`Creates ${count(template.resources.applications.length, "application")} and ${count(template.resources.databases.length, "database", "databases")}, then deploys them.`}
        size="form"
      >
        <TemplateContents template={template} />
        <form onSubmit={submit} className="space-y-4">
          <Field label="Project">{(id) => <Select id={id} required value={projectID} onChange={(e) => { setProjectID(e.target.value); setEnvironmentID(""); }}><option value="">Select a project</option>{(projects.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select>}</Field>
          <Field label="Environment">{(id) => <Select id={id} required disabled={!projectID} value={environmentID} onChange={(e) => setEnvironmentID(e.target.value)}><option value="">Select an environment</option>{(environments.data ?? []).map((env) => <option key={env.id} value={env.id}>{env.name}</option>)}</Select>}</Field>
          <Field label="Server">{(id) => <Select id={id} required value={serverID} onChange={(e) => setServerID(e.target.value)}><option value="">Select a server</option>{(servers.data ?? []).filter((s) => s.enrolled && s.role !== "builder").map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select>}</Field>
          <Field label="Name" hint={`Defaults to ${template.slug}`}>{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder={template.slug} />}</Field>
          {/* Every routed template needs a domain — the server refuses without
              one, because resolving {{domain}} to "" would write settings like
              https:/// into the container. Ask for it as required rather than
              letting the form advertise a default that always fails. */}
          {needsDomain && (
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
          {error && <p className="text-sm text-danger">{error}</p>}
          <div className="flex justify-end gap-2 pt-2"><DialogClose asChild><Button variant="ghost">Cancel</Button></DialogClose><Button type="submit" variant="primary" disabled={install.isPending || !environmentID || !serverID || (needsDomain && domain.trim() === "")}>{install.isPending ? "Installing…" : "Install and deploy"}</Button></div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
