// AdvancedSection (web-ui-design.md §6): everything with a working default
// folds in here — create forms show only what a first-timer must answer.
import { ChevronRight } from "lucide-react";
import { useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export function AdvancedSection({
  children,
  label = "Advanced",
  /** Right-aligned reassurance that skipping this is fine (design 5w). */
  note,
}: {
  children: ReactNode;
  label?: string;
  note?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-lg border border-border bg-surface">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 px-3.5 py-3 text-[12.5px] text-text-mid hover:text-text"
      >
        <ChevronRight className={cn("h-3.5 w-3.5 transition-transform duration-150", open && "rotate-90")} aria-hidden />
        {label}
        {note && !open && <span className="ml-auto font-mono text-[10.5px] text-text-faint">{note}</span>}
      </button>
      {open && <div className="space-y-4 border-t border-border p-3.5">{children}</div>}
    </div>
  );
}
