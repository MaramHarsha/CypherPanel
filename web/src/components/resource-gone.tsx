// A resource layout whose own fetch failed cannot render a masthead — it has
// no name to show. Before this, both the application and database layouts fell
// back to "…" as the title and rendered their tab strip over it, so a deleted
// or forbidden resource looked like a page still loading forever, with the real
// error buried inside whichever tab happened to be open.
//
// ui-principles §1: an error says what failed in glossary terms and offers the
// most likely remedy. §11: no dead ends — there is always a way back.
import { Link } from "@tanstack/react-router";
import { ApiError } from "@/api/client";
import { PageBody, PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";

export function ResourceGone({
  kind,
  error,
  backTo,
  backLabel,
}: {
  /** Glossary noun, lower case: "application", "managed database". */
  kind: string;
  error: unknown;
  backTo: string;
  backLabel: string;
}) {
  const status = error instanceof ApiError ? error.status : 0;
  // A non-member is answered 404 rather than 403 so the panel never confirms a
  // resource exists to someone who cannot see it — so 404 has two meanings and
  // the copy has to cover both without asserting either.
  const gone = status === 404;
  const title = gone ? `This ${kind} isn't here` : `Could not load this ${kind}`;
  const hint = gone
    ? `It may have been deleted, or it belongs to a team you're not a member of.`
    : error instanceof ApiError
      ? error.message
      : `The panel could not be reached. Check that cypherd is running.`;

  return (
    <>
      <PageHeader title={title} />
      <PageBody>
        <div className="max-w-lg rounded-lg border border-border bg-surface p-5">
          <p className="text-[13px] leading-relaxed text-text-mid">{hint}</p>
          <Link to={backTo} className="mt-4 inline-block">
            <Button variant="primary">{backLabel}</Button>
          </Link>
        </div>
      </PageBody>
    </>
  );
}
