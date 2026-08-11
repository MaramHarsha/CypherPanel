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
   * `form` is the 560px create surface from the design; `md` is a prompt;
   * `alert` is the 420–430px confirm/progress card (canvas 9g/9h/10d) with the
   * heavier 17px title those screens use.
   */
  size?: "md" | "form" | "alert";
  /**
   * Drops the ✕. For operations that genuinely cannot be dismissed into a
   * sensible state (canvas 10d) — a close control that does not stop the work
   * is a lie, and a lie is worse than no control.
   */
  hideClose?: boolean;
  children: ReactNode;
}

export function DialogContent({
  title,
  description,
  eyebrow,
  size = "md",
  hideClose,
  children,
  className,
  ...props
}: DialogContentProps) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/60" />
      {/* A dialog taller than the window used to be simply unreachable: it was
          centred with a translate and had neither a height bound nor an
          overflow, so the top and bottom fell outside the viewport with nothing
          to scroll. It is now a bounded flex column — the masthead and footer
          hold still while the body scrolls, which also keeps the primary action
          on screen instead of below the fold. */}
      <DialogPrimitive.Content
        className={cn(
          "fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)]",
          "-translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border",
          "bg-overlay shadow-2xl focus:outline-none",
          size === "form" ? "max-w-[560px]" : size === "alert" ? "max-w-[430px]" : "max-w-md",
          className,
        )}
        {...props}
      >
        <div
          className={cn(
            "shrink-0",
            size === "form" ? "px-7 pb-4 pt-6" : size === "alert" ? "px-[26px] pb-3 pt-6" : "p-5 pb-3",
          )}
        >
          {eyebrow && <div className="eyebrow mb-2">{eyebrow}</div>}
          <DialogPrimitive.Title
            className={cn(
              "tracking-tight text-text",
              size === "form"
                ? "text-[22px] font-bold leading-tight"
                : size === "alert"
                  ? "text-[17px] font-bold tracking-[-0.02em]"
                  : "text-[15px] font-bold",
            )}
          >
            {title}
          </DialogPrimitive.Title>
          {description ? (
            <DialogPrimitive.Description
              className={cn(
                "mt-1.5 leading-relaxed text-text-mid",
                size === "alert" ? "text-[12.5px]" : "text-[13px]",
              )}
            >
              {description}
            </DialogPrimitive.Description>
          ) : (
            <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
          )}
        </div>

        <div
          className={cn(
            "min-h-0 flex-1 overflow-y-auto",
            size === "form" ? "px-7 pb-7" : size === "alert" ? "px-[26px] pb-6" : "px-5 pb-5",
          )}
        >
          {children}
        </div>

        {!hideClose && (
          <DialogPrimitive.Close
            aria-label="Close"
            className="absolute right-3.5 top-3.5 rounded p-1 text-text-faint hover:bg-raised hover:text-text"
          >
            <X className="h-4 w-4" />
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}
