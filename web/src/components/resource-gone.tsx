// A resource layout whose own fetch failed cannot render a masthead — it has
// no name to show. Before this, both the application and database layouts fell
// back to "…" as the title and rendered their tab strip over it, so a deleted
// or forbidden resource looked like a page still loading forever, with the real
// error buried inside whichever tab happened to be open.
//
// It answers with the designed pages (canvas 8a–8d) in place of the masthead:
// a 404 is 8a with the id that was asked for — the "deleted by a teammate"
// case the canvas names — and the way back to the parent; everything else
// goes through the same routing PageState uses.
//
// ui-principles §1: an error says what failed in glossary terms and offers the
// most likely remedy. §11: no dead ends — there is always a way back.
import { useParams } from "@tanstack/react-router";
import { ApiError } from "@/api/client";
import { NotFoundPage } from "@/components/error-page";
import { QueryError } from "@/components/page-state";

export function ResourceGone({
  kind,
  error,
  backTo,
  backLabel,
  name,
}: {
  /** Glossary noun, lower case: "application", "managed database", "server". */
  kind: string;
  error: unknown;
  backTo: string;
  backLabel: string;
  /** What was asked for. Defaults to the id in the URL for this kind of resource. */
  name?: string;
}) {
  const params = useParams({ strict: false });
  const requested =
    name ??
    (kind.includes("application")
      ? params.appId
      : kind.includes("database")
        ? params.dbId
        : kind.includes("server")
          ? params.serverId
          : undefined);

  // A non-member is answered 404 rather than 403 so the panel never confirms a
  // resource exists to someone who cannot see it — so 404 has two meanings,
  // and 8a's copy ("or it did…") already covers both without asserting either.
  if (error instanceof ApiError && error.status === 404) {
    return <NotFoundPage embedded resource={requested} backTo={backTo} backLabel={backLabel} />;
  }
  return <QueryError error={error} />;
}
