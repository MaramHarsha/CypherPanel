// Right-side drawer (radix dialog): the canonical detail surface — the list
// stays visible behind it (ui-principles §4: drawers over modals).
//
// `tone="ink"` is the live-deploy drawer (canvas 1c): an ink panel in BOTH
// themes, because what it frames is a log pane and log panes are already ink
// (4e token table). A deploy in progress should feel like looking into the
// machine. Its head is not a toolbar — the revision line sits inside the
// panel's own padding and the stage rail's top margin does the separating,
// so no hairline cuts across the top of the pane.
//
// Below `sm` it becomes a bottom sheet (canvas 14c): rounded at the top, led by
// a grab handle, with the list still visible above it. A full-viewport cover
// would answer "what is deploying?" by hiding what you were looking at.
//
// The sheet rises and the column slides in — one short ease, and under
// `prefers-reduced-motion` the same keyframes collapse to a snap (canvas 14g:
// "bottom sheets and drawers snap instead of slide"). Radix traps focus
// inside, closes on esc and returns focus to the opener.
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
  /**
   * One line under the title saying what the panel is for. Also the panel's
   * `aria-describedby`; without one the description is deliberately absent
   * rather than a repeat of the title.
   */
  description?: string;
  children: ReactNode;
  wide?: boolean;
  tone?: "paper" | "ink";
}

export function Drawer({
  open,
  onOpenChange,
  title,
  label,
  description,
  children,
  wide,
  tone = "paper",
}: DrawerProps) {
  const ink = tone === "ink";
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-[rgba(22,19,14,0.5)] dark:bg-black/60" />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-x-0 bottom-0 z-50 flex max-h-[85dvh] flex-col rounded-t-2xl shadow-sheet focus:outline-none",
            "animate-sheet-up",
            // From `sm` up it is the right-hand column the canvas draws.
            "sm:inset-y-0 sm:left-auto sm:right-0 sm:max-h-none sm:animate-drawer-in sm:rounded-none sm:border-l sm:shadow-modal",
            // A pane sizes itself against its parent, so the sheet needs a
            // definite height or the log viewer inside it never scrolls — it
            // just grows the sheet until the whole thing scrolls instead.
            ink && "h-[85dvh] sm:h-auto",
            ink
              ? "bg-toast text-toast-text sm:border-l-[1.5px] sm:border-border-strong"
              : "border-border bg-surface",
            wide ? "sm:w-full sm:max-w-2xl" : "sm:w-full sm:max-w-lg",
          )}
          // With a description Radix wires aria-describedby itself; without
          // one the attribute is set to nothing on purpose, which is how Radix
          // is told the omission is deliberate rather than an oversight.
          {...(description ? {} : { "aria-describedby": undefined })}
        >
          {/* The grab handle only exists on the sheet; above `sm` the panel is
              a column and there is nothing to drag. */}
          <div
            aria-hidden
            className={cn(
              "mx-auto mt-2.5 h-1 w-9 shrink-0 rounded-full sm:hidden",
              ink ? "bg-pane-border" : "bg-border",
            )}
          />
          <div
            className={cn(
              ink ? "px-6 pb-0 pt-4 sm:pt-[22px]" : "border-b px-5 py-3.5",
              !ink && "border-border",
            )}
          >
            <div className="flex items-center justify-between">
              <DialogPrimitive.Title
                className={cn("min-w-0 text-[15px] font-semibold", ink ? "text-toast-text" : "text-text")}
              >
                <span className="sr-only">{label}</span>
                <span aria-hidden>{title}</span>
              </DialogPrimitive.Title>
              <DialogPrimitive.Close
                aria-label="Close"
                className={cn(
                  "rounded p-1",
                  ink
                    ? "text-toast-dismiss hover:text-toast-text"
                    : "text-text-faint hover:bg-raised hover:text-text",
                )}
              >
                <X className="h-4 w-4" />
              </DialogPrimitive.Close>
            </div>
            {description && (
              <DialogPrimitive.Description
                className={cn("mt-1 text-[12.5px] leading-relaxed", ink ? "text-toast-faint" : "text-text-mid")}
              >
                {description}
              </DialogPrimitive.Description>
            )}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
