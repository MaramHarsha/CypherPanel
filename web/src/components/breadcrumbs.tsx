// Breadcrumbs: team / project / environment / resource — always visible
// (ui-principles §4), set in mono because the trail is machine context.
import { Link } from "@tanstack/react-router";
import { Fragment } from "react";

export interface Crumb {
  label: string;
  to?: string;
}

export function Breadcrumbs({ crumbs }: { crumbs: Crumb[] }) {
  if (crumbs.length === 0) return null;
  return (
    <nav aria-label="Breadcrumb" className="min-w-0">
      <ol className="mono flex min-w-0 items-center gap-1.5 text-xs text-text-faint">
        {crumbs.map((c, i) => {
          const last = i === crumbs.length - 1;
          return (
            <Fragment key={`${c.label}-${i}`}>
              {i > 0 && <li aria-hidden>/</li>}
              <li className="min-w-0 truncate">
                {c.to && !last ? (
                  <Link to={c.to} className="hover:text-text">
                    {c.label}
                  </Link>
                ) : (
                  <span aria-current={last ? "page" : undefined} className={last ? "text-text-mid" : undefined}>
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
