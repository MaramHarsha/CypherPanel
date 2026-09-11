// The strip of tabs a resource or a settings section hangs off, and the fold
// that keeps it on a 360px screen (canvas 14c): the first few tabs survive
// with short labels and everything else moves into "More" rather than off the
// edge of a strip nobody can tell is scrollable.
//
// One implementation, because a fold written three times is a fold that ends
// up meaning three things. The application layout still draws its own copy —
// its strip is also the `1`–`7` key vocabulary (lib/keys.ts) — and belongs
// here as soon as someone maps APP_TABS onto these props.
import { Link, useRouterState } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { Dropdown, DropdownContent, DropdownItem, DropdownTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/utils";

export interface Tab {
  /** Path segment under the strip's `base`; `""` is the section itself. */
  to: string;
  label: string;
  /** The 360px label; omitted where the full label already fits. */
  short?: string;
}

/** How many tabs survive on a phone — 14c's four, unless a strip's labels are
 *  wide enough that four of them do not fit. */
const NARROW = 4;

export function TabStrip({
  tabs,
  label,
  base = "",
  narrow = NARROW,
}: {
  tabs: readonly Tab[];
  /** Names the strip for assistive tech — "Settings", "Compose stack". */
  label: string;
  /** Absolute path the segments hang off; the strip's own route. */
  base?: string;
  narrow?: number;
}) {
  // Which tab is open, computed rather than left to the strip: below `sm` the
  // folded tabs are display:none, so their own `activeProps` underline is
  // painted on nothing and the strip marks no tab at all. Canvas 14c always
  // marks exactly one, so when the open tab has been folded away the trigger
  // wears its name instead of the word "More".
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const open = tabs.findIndex((t) => isOpen(pathname, base, t));
  const folded = tabs.slice(narrow);
  const foldedLabel = open >= narrow ? (tabs[open]?.short ?? tabs[open]?.label) : undefined;

  return (
    // The negative margin lives on the strip, never on a tab: a tab pulled 1px
    // below an `overflow-x-auto` parent overflows it vertically and the browser
    // paints a scrollbar inside the strip. The horizontal scroll is the safety
    // valve rather than the design — the fold is what makes the strip fit, and
    // this is only what happens when a folded tab with a long name is open.
    <nav
      className="-mb-px flex items-center gap-4 overflow-x-auto text-[12.5px] sm:gap-5 sm:text-[13px]"
      aria-label={label}
    >
      {tabs.map((t, i) => (
        <Link
          key={t.label}
          to={href(base, t)}
          activeOptions={{ exact: t.to === "" }}
          className={cn(
            "whitespace-nowrap border-b-2 border-transparent px-0.5 py-[9px] text-text-mid hover:text-text",
            i >= narrow && "hidden sm:block",
          )}
          activeProps={{ className: "border-border-strong font-semibold text-text" }}
        >
          {t.short && t.short !== t.label ? (
            <>
              {/* Only the displayed one is in the accessibility tree — the
                  other is display:none, not merely invisible. */}
              <span className="sm:hidden">{t.short}</span>
              <span className="hidden sm:inline">{t.label}</span>
            </>
          ) : (
            t.label
          )}
        </Link>
      ))}
      {folded.length > 0 && (
        <Dropdown>
          <DropdownTrigger
            className={cn(
              "inline-flex items-center gap-1 whitespace-nowrap border-b-2 border-transparent px-0.5 py-[9px] text-text-mid hover:text-text sm:hidden",
              foldedLabel && "border-border-strong font-semibold text-text",
            )}
          >
            {foldedLabel ?? "More"} <ChevronDown className="h-3 w-3" aria-hidden />
          </DropdownTrigger>
          <DropdownContent align="end">
            {folded.map((t) => (
              <DropdownItem key={t.label} asChild>
                {/* The open tab is marked in the menu too. Weight and a fill
                    rather than a colour: Radix passes the item's own classes
                    through the Slot, where `cn` cannot arbitrate a second text
                    colour against the first. */}
                <Link to={href(base, t)} activeProps={{ className: "bg-raised font-semibold" }}>
                  {t.label}
                </Link>
              </DropdownItem>
            ))}
          </DropdownContent>
        </Dropdown>
      )}
    </nav>
  );
}

function href(base: string, tab: Tab): string {
  return tab.to === "" ? base || "/" : `${base}/${tab.to}`;
}

/** The same rule `activeOptions` applies, in arithmetic: the section's own tab
 *  matches the section exactly, every other tab keeps its underline over the
 *  pages below it (`/settings/teams/t_1` is still Teams). */
function isOpen(pathname: string, base: string, tab: Tab): boolean {
  const path = pathname.length > 1 ? pathname.replace(/\/$/, "") : pathname;
  const to = href(base, tab);
  return tab.to === "" ? path === to : path === to || path.startsWith(`${to}/`);
}
