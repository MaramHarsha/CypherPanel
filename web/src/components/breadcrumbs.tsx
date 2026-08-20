// Breadcrumbs: team / project / environment / resource — always visible
// (ui-principles §4). In Mission Control the trail is set as the page's mono
// eyebrow, uppercase and widely tracked, with the current resource in the
// accent. It reads as a dateline above a headline.
import { Link } from "@tanstack/react-router";
import { Fragment } from "react";
import { cn } from "@/lib/utils";

export interface Crumb {
  label: string;
  to?: string;
}

export function Breadcrumbs({ crumbs, className }: { crumbs: Crumb[]; className?: string }) {
  if (crumbs.length === 0) return null;
  return (
    <nav aria-label="Breadcrumb" className={cn("min-w-0", className)}>
      {/* The canvas sets the trail as one text run — `MERIDIAN-STUDIO /
          ATLAS-CRM` — so the gap either side of the slash is a tracked mono
          space, ~8px at this size, not a layout gutter. */}
      <ol className="eyebrow flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        {crumbs.map((c, i) => {
          const last = i === crumbs.length - 1;
          return (
            <Fragment key={`${c.label}-${i}`}>
              {i > 0 && <li aria-hidden>/</li>}
              <li className="min-w-0 truncate">
                {c.to && !last ? (
                  <Link to={c.to} className="transition-colors hover:text-text">
                    {c.label}
                  </Link>
                ) : (
                  <span aria-current={last ? "page" : undefined} className={last ? "text-accent" : undefined}>
                    {c.label}
                  </span>
                )}
              </li>
            </Fragment>
          );
        })}
      </ol>
    </nav>
  );
}
