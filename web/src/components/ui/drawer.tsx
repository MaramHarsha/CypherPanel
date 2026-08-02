// Right-side drawer (radix dialog): the canonical detail surface — the list
// stays visible behind it (ui-principles §4: drawers over modals).
//
// `tone="ink"` is the live-deploy drawer: an ink panel in BOTH themes, because
// what it frames is a log pane and log panes are already ink (4e token table).
// A deploy in progress should feel like looking into the machine.
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

interface DrawerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  /** Accessible name when title is a node. */
  label: string;
  children: ReactNode;
  wide?: boolean;
  tone?: "paper" | "ink";
}

export function Drawer({ open, onOpenChange, title, label, children, wide, tone = "paper" }: DrawerProps) {
  const ink = tone === "ink";
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l shadow-2xl focus:outline-none",
            ink ? "border-pane-border bg-[#16130e] text-[#e9e5dc]" : "border-border bg-surface",
            wide ? "sm:max-w-2xl" : "sm:max-w-lg",
          )}
          aria-describedby={undefined}
        >
          <div
            className={cn(
              "flex items-center justify-between border-b px-5 py-3.5",
              ink ? "border-pane-border" : "border-border",
            )}
          >
            <DialogPrimitive.Title
              className={cn("min-w-0 text-[15px] font-semibold", ink ? "text-[#f0ece3]" : "text-text")}
            >
              <span className="sr-only">{label}</span>
              <span aria-hidden>{title}</span>
            </DialogPrimitive.Title>
            <DialogPrimitive.Close
              aria-label="Close"
              className={cn(
                "rounded p-1",
                ink
                  ? "text-pane-faint hover:bg-white/10 hover:text-[#f0ece3]"
                  : "text-text-faint hover:bg-raised hover:text-text",
              )}
            >
              <X className="h-4 w-4" />
            </DialogPrimitive.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
