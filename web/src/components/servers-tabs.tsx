// The Servers tab strip. `Servers → Updates` is a TAB, not a fifth top-level
// nav item: ui-principles §4 fixes the top bar at four, and agent updates are
// something you do TO the fleet rather than a fifth place to be.
//
// It is a component rather than a route layout on purpose. Wrapping
// /servers in a layout route would put this strip on the server DETAIL page
// too, where it would compete with that page's own masthead — so the two pages
// that are fleet-wide render it and the one that is about a single host does
// not.
import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/utils";

const TABS = [
  { to: "/servers", label: "Fleet", exact: true },
  { to: "/servers/updates", label: "Updates", exact: false },
] as const;

export function ServersTabs() {
  return (
    <nav className="-mb-px flex items-center gap-5 text-[13px]" aria-label="Servers">
      {TABS.map((t) => (
        <Link
          key={t.to}
          to={t.to}
          activeOptions={{ exact: t.exact }}
          className="border-b-2 border-transparent pb-2.5 text-text-mid transition-colors hover:text-text"
          activeProps={{ className: cn("border-text text-text font-medium") }}
        >
          {t.label}
        </Link>
      ))}
    </nav>
  );
}
