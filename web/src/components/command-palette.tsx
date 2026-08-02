// ⌘K command palette (web-ui-design.md §3): the fast lane for power users —
// jump to any project or server, or a top-level destination. Fed only by what
// the caller can already see; invisible to beginners until summoned. Never a
// requirement for any flow.
import { useNavigate } from "@tanstack/react-router";
import { Boxes, CornerDownLeft, LayoutTemplate, Search, Server, Settings } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
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

  useEffect(() => setActive(0), [query, open]);

  const run = (item: Item | undefined) => {
    if (!item) return;
    setOpen(false);
    setQuery("");
    item.go();
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent title="Command palette" className="top-[14%] max-w-lg translate-y-0 p-0">
        <div className="flex items-center gap-2.5 border-b-[1.5px] border-border-strong px-4">
          <Search className="h-4 w-4 shrink-0 text-text-faint" aria-hidden />
          <input
            autoFocus
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
                run(filtered[active]);
              }
            }}
            placeholder="Jump to a project, server, or page…"
            className="h-12 w-full bg-transparent font-mono text-sm text-text placeholder:text-text-faint focus:outline-none"
            aria-label="Search"
          />
        </div>
        <ul ref={listRef} className="max-h-80 overflow-y-auto p-2" role="listbox">
          {filtered.length === 0 ? (
            <li className="px-3 py-8 text-center text-[13px] text-text-faint">
              No matches — try a project or server name
            </li>
          ) : (
            filtered.map((item, i) => {
              // Section eyebrows are derived, not hand-placed: the first item
              // of each run of a kind labels the run.
              const heading = item.hint !== filtered[i - 1]?.hint ? SECTION[item.hint] : null;
              return (
                <li key={item.id}>
                  {heading && <div className="eyebrow px-2.5 pb-1 pt-2">{heading}</div>}
                  <button
                    type="button"
                    role="option"
                    aria-selected={i === active}
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
            })
          )}
        </ul>
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
