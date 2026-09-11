// The modal card of canvas 9a/9b/9c/9g/13af: paper (not white — white is what a
// modal *frames*, e.g. 9g's comparison table), 10px corners, a single 20/50/.35
// drop shadow and no border at all, over a warm ink scrim. The ✕ rides the
// title's own baseline rather than floating in the corner.
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { type ComponentPropsWithoutRef, type ReactNode } from "react";
import { cn } from "@/lib/utils";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

interface DialogContentProps extends ComponentPropsWithoutRef<typeof DialogPrimitive.Content> {
  title: string;
  description?: string;
  /** Mono breadcrumb above the title — where this dialog is acting. */
  eyebrow?: ReactNode;
  /**
   * `form` is the 560px create surface from the design; `md` is the 420px card
   * every ordinary modal uses (9a, 9d, 9e, 13ac); `alert` is the 430px
   * confirm/progress card (9g/9h/10d). All three share the 18px/700 title the
   * canvas gives every modal.
   */
  size?: "md" | "form" | "alert";
  /**
   * Drops the ✕. For operations that genuinely cannot be dismissed into a
   * sensible state (canvas 10d) — a close control that does not stop the work
   * is a lie, and a lie is worse than no control.
   */
  hideClose?: boolean;
  /**
   * Keeps the accessible name but paints nothing. The ⌘K palette (15a) opens
   * straight onto its search row — no heading, no ✕; esc closes it.
   */
  hideTitle?: boolean;
  children: ReactNode;
}

export function DialogContent({
  title,
  description,
  eyebrow,
  size = "md",
  hideClose,
  hideTitle,
  children,
  className,
  ...props
}: DialogContentProps) {
  const pad = size === "form" ? "px-7" : "px-[26px]";
  return (
    <DialogPrimitive.Portal>
      {/* Warm ink at 50%, not black: the paper behind a modal should darken,
          not turn grey. Dark deepens to black/60, as the dark cards do. */}
      <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-[rgba(22,19,14,0.5)] dark:bg-black/60" />
      {/* A dialog taller than the window used to be simply unreachable: it was
          centred with a translate and had neither a height bound nor an
          overflow, so the top and bottom fell outside the viewport with nothing
          to scroll. It is now a bounded flex column — the masthead and footer
          hold still while the body scrolls, which also keeps the primary action
          on screen instead of below the fold. */}
      <DialogPrimitive.Content
        className={cn(
          "fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)]",
          "-translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-[10px]",
          "bg-bg shadow-modal focus:outline-none",
          size === "form" ? "max-w-[560px]" : size === "alert" ? "max-w-[430px]" : "max-w-[420px]",
          className,
        )}
        {...props}
      >
        <div className={cn("shrink-0", pad, size === "form" ? "pb-4 pt-6" : "pb-3 pt-6")}>
          {eyebrow && <div className="eyebrow mb-2">{eyebrow}</div>}
          <div className={cn("flex items-baseline gap-3", hideTitle && "sr-only")}>
            <DialogPrimitive.Title
              className={cn(
                "tracking-[-0.02em] text-text",
                size === "form" ? "text-[22px] font-bold leading-tight" : "text-[18px] font-bold",
              )}
            >
              {title}
            </DialogPrimitive.Title>
            {!hideClose && !hideTitle && (
              <DialogPrimitive.Close
                aria-label="Close"
                className="ml-auto shrink-0 rounded p-0.5 text-[14px] leading-none text-text-faint hover:text-text"
              >
                <X className="h-4 w-4" />
              </DialogPrimitive.Close>
            )}
          </div>
          {/* A hidden masthead hides its description too: the text still
              reaches a screen reader as the dialog's aria-describedby, but a
              card that draws its own head (14f, ⌘K) must not grow a stray
              line of prose above it. */}
          {description ? (
            <DialogPrimitive.Description
              className={hideTitle ? "sr-only" : "mt-1.5 text-[12.5px] leading-relaxed text-text-mid"}
            >
              {description}
            </DialogPrimitive.Description>
          ) : (
            <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
          )}
        </div>

        <div className={cn("min-h-0 flex-1 overflow-y-auto", pad, size === "form" ? "pb-7" : "pb-6")}>
          {children}
        </div>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}
