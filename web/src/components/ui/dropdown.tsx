import * as DropdownPrimitive from "@radix-ui/react-dropdown-menu";
import { type ComponentPropsWithoutRef } from "react";
import { cn } from "@/lib/utils";

export const Dropdown = DropdownPrimitive.Root;
export const DropdownTrigger = DropdownPrimitive.Trigger;

export function DropdownContent({ className, ...props }: ComponentPropsWithoutRef<typeof DropdownPrimitive.Content>) {
  return (
    <DropdownPrimitive.Portal>
      <DropdownPrimitive.Content
        sideOffset={4}
        className={cn(
          "z-50 min-w-40 rounded-md border border-border bg-overlay p-1 shadow-pop",
          className,
        )}
        {...props}
      />
    </DropdownPrimitive.Portal>
  );
}

export function DropdownItem({ className, ...props }: ComponentPropsWithoutRef<typeof DropdownPrimitive.Item>) {
  return (
    <DropdownPrimitive.Item
      className={cn(
        // `data-[highlighted]:bg-raised` is the hover wash, and --raised on
        // --overlay is barely over 1:1 in either theme — read as a pointer
        // affordance it is enough, read as the only keyboard indicator it is
        // nothing. So the item keeps the global 14g ring: Radix focuses the
        // item itself, and because it does so inside the keydown handler the
        // browser matches :focus-visible for the arrows and not for the mouse.
        // The offset is inset because the ring would otherwise draw outside the
        // menu's own 4px padding.
        "cursor-default select-none rounded px-2 py-1.5 text-[13px] text-text focus-visible:outline-offset-[-2px]",
        "data-[highlighted]:bg-raised",
        className,
      )}
      {...props}
    />
  );
}

export const DropdownSeparator = ({ className, ...props }: ComponentPropsWithoutRef<typeof DropdownPrimitive.Separator>) => (
  <DropdownPrimitive.Separator className={cn("my-1 h-px bg-border", className)} {...props} />
);
