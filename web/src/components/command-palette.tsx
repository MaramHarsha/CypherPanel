// ⌘K command palette (web-ui-design.md §3): the fast lane for power users — jump
// to any project or server, or a top-level page. Fed only by what the caller can
// already see; invisible to beginners until summoned. Never a requirement for
// any flow.
//
// 15a's miss copy names a wider index than this one — applications and
// databases too — which needs a control-plane search endpoint: both live under
// an environment, so covering them from here would mean fanning out over every
// project on every keystroke. Until that endpoint exists the miss says what is
// actually searched rather than what the canvas promises.
import { useNavigate } from "@tanstack/react-router";
import { Boxes, CornerDownLeft, LayoutTemplate, Search, Server, Settings } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useListProjects } from "@/api/gen/projects/projects";
import { useListServers } from "@/api/gen/servers/servers";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

interface Item {
  id: string;
  label: string;
  hint: string;
  icon: ReactNode;
  go: () => void;
}

const SECTION: Record<string, string> = {
  project: "Projects",
  server: "Servers",
  "go to": "Go to",
};

/** The top bar's "Jump to anything" pill opens the same surface as ⌘K. */
const OPEN_EVENT = "cypher:open-palette";
export function openCommandPalette(): void {
  window.dispatchEvent(new Event(OPEN_EVENT));
}

export function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const navigate = useNavigate();
  const listRef = useRef<HTMLUListElement>(null);
  // The input keeps DOM focus while ↑↓ move `active`, so the selection is
  // carried to a screen reader by aria-activedescendant rather than by focus —
  // which means every option needs an id the input can name.
  const listId = useId();
  const optionId = (item: Item) => `${listId}-${item.id}`;

  const projects = useListProjects({ query: { enabled: open } });
  const servers = useListServers({ query: { enabled: open } });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    const onOpen = () => setOpen(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(OPEN_EVENT, onOpen);
    };
  }, []);

  const items = useMemo<Item[]>(() => {
    const nav: Item[] = [
      { id: "nav-projects", label: "Projects", hint: "go to", icon: <Boxes className="h-4 w-4" />, go: () => navigate({ to: "/projects" }) },
      { id: "nav-servers", label: "Servers", hint: "go to", icon: <Server className="h-4 w-4" />, go: () => navigate({ to: "/servers" }) },
      { id: "nav-templates", label: "Templates", hint: "go to", icon: <LayoutTemplate className="h-4 w-4" />, go: () => navigate({ to: "/templates" }) },
      { id: "nav-settings", label: "Settings", hint: "go to", icon: <Settings className="h-4 w-4" />, go: () => navigate({ to: "/settings" }) },
    ];
    const proj: Item[] = (projects.data ?? []).map((p) => ({
      id: `project-${p.id}`,
      label: p.name,
      hint: "project",
      icon: <Boxes className="h-4 w-4" />,
      go: () => navigate({ to: "/projects/$projectId", params: { projectId: p.id } }),
    }));
    const srv: Item[] = (servers.data ?? []).map((s) => ({
      id: `server-${s.id}`,
      label: s.name,
      hint: "server",
      icon: <Server className="h-4 w-4" />,
      go: () => navigate({ to: "/servers/$serverId", params: { serverId: s.id } }),
    }));
    return [...proj, ...srv, ...nav];
  }, [projects.data, servers.data, navigate]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") return items;
    return items.filter((i) => i.label.toLowerCase().includes(q) || i.hint.includes(q));
  }, [items, query]);

  const activeItem = filtered[active];

  useEffect(() => setActive(0), [query, open]);

  const run = (item: Item | undefined) => {
    if (!item) return;
    setOpen(false);
    setQuery("");
    item.go();
  };

  // The miss state's only verb, reachable both by pointer and by ↵ — the
  // footer promises ↵ opens something, and on a miss this is the something.
  const createProject = () => {
    setOpen(false);
    setQuery("");
    void navigate({ to: "/projects" });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {/* 15a opens the palette straight onto its search row: no heading, no ✕,
          esc closes — which is what the footer already tells the operator. The
          child selector zeroes the shared card's masthead padding, which the
          card keeps even when its title is only an accessible name. The panel
          stays white while the modals around it went to paper: a palette is a
          field, not a sheet of stationery. */}
      <DialogContent
        title="Command palette"
        hideTitle
        hideClose
        className="top-[14%] max-w-lg translate-y-0 bg-surface [&>div]:p-0"
      >
        {/* The row is the field, so the row carries the focus indicator: the
            ink rule under it turns to the focus orange while the input has
            keyboard focus (canvas 14g: ":focus-visible only"), in place of an
            outline that would box a borderless input inside a bordered row. */}
        <div className="flex items-center gap-2.5 border-b-[1.5px] border-border-strong px-[15px] py-3 transition-colors has-[:focus-visible]:border-focus">
          <Search className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
          <input
            autoFocus
            role="combobox"
            aria-expanded={filtered.length > 0}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={activeItem ? optionId(activeItem) : undefined}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(a + 1, filtered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(a - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                if (filtered.length === 0) createProject();
                else run(filtered[active]);
              }
            }}
            placeholder="Jump to a project, server, or page…"
            className="w-full bg-transparent font-mono text-[13.5px] leading-6 text-text caret-accent placeholder:text-text-faint focus-visible:outline-none"
            aria-label="Search"
          />
        </div>
        {/* A listbox with no options is not a listbox: the miss is a sentence
            and a button, and neither is an `option`, so screen readers that
            keep only options would announce the miss as silence. It lives
            outside the list and speaks for itself instead. */}
        {filtered.length === 0 ? (
          <div role="status">
            <NoMatches query={query.trim()} onCreateProject={createProject} />
          </div>
        ) : (
          <ul ref={listRef} id={listId} className="max-h-80 overflow-y-auto p-2" role="listbox" aria-label="Results">
            {filtered.map((item, i) => {
              // Section eyebrows are derived, not hand-placed: the first item
              // of each run of a kind labels the run.
              const heading = item.hint !== filtered[i - 1]?.hint ? SECTION[item.hint] : null;
              return (
                // The li is presentational: a listbox owns options, and a
                // listitem between the two is what would stop the input's
                // aria-activedescendant resolving to anything announceable.
                <li key={item.id} role="presentation">
                  {heading && <div className="eyebrow px-2.5 pb-1 pt-2">{heading}</div>}
                  {/* tabIndex -1: the list is driven from the input by ↑↓, so
                      an option that were also a Tab stop would make the palette
                      take one Tab per result to leave. */}
                  <button
                    type="button"
                    role="option"
                    id={optionId(item)}
                    aria-selected={i === active}
                    tabIndex={-1}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => run(item)}
                    className={cn(
                      "flex w-full items-center justify-between gap-2 rounded-md px-2.5 py-2 text-left text-[13px]",
                      i === active ? "bg-primary font-medium text-primary-fg" : "text-text-mid",
                    )}
                  >
                    <span className="flex min-w-0 items-center gap-2.5">
                      <span className={cn("shrink-0", i === active ? "text-accent" : "text-text-faint")}>
                        {item.icon}
                      </span>
                      <span className="truncate">{item.label}</span>
                    </span>
                    {i === active && <CornerDownLeft className="h-3 w-3 shrink-0" aria-hidden />}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        <div className="flex gap-4 border-t border-border px-4 py-2 font-mono text-[10.5px] text-text-faint">
          <span>↑↓ navigate</span>
          <span>↵ open</span>
          <span>esc close</span>
          <span className="ml-auto hidden sm:inline">scoped to what you can see</span>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * 15a, the card the whole feedback layer is named after: never a dead end,
 * always the next verb. A miss echoes what was typed — so the operator can see
 * the typo — says what was searched, and offers the one thing that can still be
 * done from here.
 *
 * 15a's own verb is "+ Create application", which the palette cannot honour:
 * an application is created inside an environment, and the palette has no way
 * to say which. Projects is where every creation in this product starts, so
 * that is where the pill goes.
 */
function NoMatches({ query, onCreateProject }: { query: string; onCreateProject: () => void }) {
  return (
    <div className="px-[15px] py-[22px] text-center">
      <p className="text-[13px] text-text-dim">{`Nothing matches "${query}"`}</p>
      <p className="mt-1 text-[12px] text-text-faint">Not a project, server, or page you can see.</p>
      <button
        type="button"
        onClick={onCreateProject}
        className="mt-3 rounded-full bg-primary px-[15px] py-[7px] text-[12px] font-semibold text-primary-fg hover:bg-primary-hover"
      >
        + New project
      </button>
    </div>
  );
}
