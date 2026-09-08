// Compose stack detail layout: masthead + tabs (Overview · Logs · Variables ·
// Revisions · Settings), mirroring the application and database layouts so the
// third Resource type is learned once rather than taught again.
//
// SOURCING NOTE (see `create-compose-stack-dialog.tsx` for the long form): a
// stack has no card in the design canvas, so this is the existing resource
// layout applied to it — same `PageHeader size="sm"`, same StatusBadge, same
// tab strip, same ResourceGone. What differs is the badge's second line: a
// stack's history is its revision list rather than a deployment pipeline, so
// the masthead says which revision is serving.
import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useGetComposeStack } from "@/api/gen/compose-stacks/compose-stacks";
import { useGetProject } from "@/api/gen/projects/projects";
import { PageBody, PageHeader } from "@/components/page-header";
import { ResourceGone } from "@/components/resource-gone";
import { StatusBadge } from "@/components/status-badge";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/projects/$projectId/compose/$stackId")({
  component: ComposeStackLayout,
});

const TABS = [
  { to: "", label: "Overview" },
  { to: "logs", label: "Logs" },
  { to: "env", label: "Variables" },
  { to: "revisions", label: "Revisions" },
  { to: "settings", label: "Settings" },
] as const;

/** Short revision for display, the same 7 characters an application's card gets. */
function shortRevision(id: string | null | undefined): string {
  if (!id) return "";
  const tail = id.includes("_") ? id.slice(id.lastIndexOf("_") + 1) : id;
  return tail.slice(0, 7);
}

function ComposeStackLayout() {
  const { projectId, stackId } = Route.useParams();
  const project = useGetProject(projectId);
  const stack = useGetComposeStack(stackId);

  useCrumbs([
    { label: "projects", to: "/projects" },
    { label: project.data?.project.name ?? projectId, to: `/projects/${projectId}` },
    { label: stack.data?.name ?? stackId },
  ]);

  if (stack.isError) {
    return (
      <ResourceGone
        kind="compose stack"
        error={stack.error}
        backTo={`/projects/${projectId}`}
        backLabel="Back to the project"
      />
    );
  }

  const observed = shortRevision(stack.data?.observed_revision_id);
  const desired = shortRevision(stack.data?.desired_revision_id);

  return (
    <>
      <PageHeader
        size="sm"
        title={stack.data?.name ?? "…"}
        badge={
          <span className="flex flex-wrap items-center gap-2">
            <StatusBadge status={stack.data?.status} />
            {/* Desired and observed are separate facts (ADR-005) and the gap
                between them is the whole state of a converging stack, so the
                masthead shows both rather than picking one to believe. */}
            {desired && (
              <span className="ml-1.5 font-mono text-[12px] text-text-faint">
                rev {observed || "—"}
                {observed && observed !== desired ? ` → ${desired}` : ""}
              </span>
            )}
          </span>
        }
        below={
          <nav className="-mb-px flex gap-5 overflow-x-auto" aria-label="Compose stack">
            {TABS.map((t) => (
              <Link
                key={t.label}
                from={Route.fullPath}
                to={t.to === "" ? "." : t.to}
                activeOptions={{ exact: t.to === "" }}
                className="whitespace-nowrap border-b-2 border-transparent px-0.5 py-2.5 text-[13px] text-text-mid hover:text-text"
                activeProps={{ className: "border-border-strong font-semibold text-text" }}
              >
                {t.label}
              </Link>
            ))}
          </nav>
        }
      />
      <PageBody>
        <Outlet />
      </PageBody>
    </>
  );
}
