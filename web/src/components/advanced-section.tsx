// AdvancedSection (web-ui-design.md §6): everything with a working default
// folds in here — create forms show only what a first-timer must answer.
import { ChevronRight } from "lucide-react";
import { useState, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export function AdvancedSection({ children, label = "Advanced" }: { children: ReactNode; label?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-md border border-border">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-1.5 px-3 py-2 text-[13px] text-text-mid hover:text-text"
      >
        <ChevronRight className={cn("h-3.5 w-3.5 transition-transform duration-150", open && "rotate-90")} aria-hidden />
        {label}
      </button>
      {open && <div className="space-y-4 border-t border-border p-3">{children}</div>}
    </div>
  );
}
