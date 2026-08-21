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
        "cursor-default select-none rounded px-2 py-1.5 text-[13px] text-text outline-none",
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
