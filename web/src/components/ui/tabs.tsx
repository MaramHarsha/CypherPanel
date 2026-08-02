// Resource tabs: a hairline rule with an ink underline on the active tab. The
// orange underline is reserved for the top-bar nav, so the two levels of
// navigation never read as the same thing.
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { type ComponentPropsWithoutRef } from "react";
import { cn } from "@/lib/utils";

export const Tabs = TabsPrimitive.Root;
export const TabsContent = TabsPrimitive.Content;

export function TabsList({ className, ...props }: ComponentPropsWithoutRef<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("flex gap-5 overflow-x-auto border-b border-border", className)}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "-mb-px whitespace-nowrap border-b-2 border-transparent px-0.5 py-2.5 text-[13px] text-text-mid",
        "hover:text-text data-[state=active]:border-border-strong data-[state=active]:font-semibold",
        "data-[state=active]:text-text",
        className,
      )}
      {...props}
    />
  );
}
