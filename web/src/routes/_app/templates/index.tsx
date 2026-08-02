// Templates: the catalog is a Phase 4 slice-5 feature with its own spec —
// until it lands, this is an honest empty state, not a stub grid.
import { createFileRoute } from "@tanstack/react-router";
import { EmptyState } from "@/components/empty-state";
import { PageBody, PageHeader } from "@/components/page-header";
import { useCrumbs } from "@/lib/crumbs";

export const Route = createFileRoute("/_app/templates/")({ component: TemplatesPage });

function TemplatesPage() {
  useCrumbs([{ label: "templates" }]);
  return (
    <>
      <PageHeader title="Templates" />
      <PageBody>
        <EmptyState
          title="The template catalog isn't here yet"
          hint="Templates will let you launch ready-made stacks (Plausible, Uptime Kuma, n8n …) with one click — generated secrets, domain, and backups included. It's on the roadmap for this phase."
        />
      </PageBody>
    </>
  );
}
