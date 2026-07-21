// Right-side drawer (radix dialog): the canonical detail surface — the list
// stays visible behind it (ui-principles §4: drawers over modals).
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
}

export function Drawer({ open, onOpenChange, title, label, children, wide }: DrawerProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-border bg-surface shadow-2xl focus:outline-none",
            wide ? "sm:max-w-2xl" : "sm:max-w-lg",
          )}
          aria-describedby={undefined}
        >
          <div className="flex items-center justify-between border-b border-border px-4 py-3">
            <DialogPrimitive.Title className="min-w-0 text-sm font-semibold text-text">
              <span className="sr-only">{label}</span>
              <span aria-hidden>{title}</span>
            </DialogPrimitive.Title>
            <DialogPrimitive.Close
              aria-label="Close"
              className="rounded p-1 text-text-faint hover:bg-raised hover:text-text"
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
