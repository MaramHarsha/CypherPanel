import { cn } from "@/lib/utils";

/** Quiet loading placeholder — matches final layout, no shimmer louder than
 *  content (web-ui-design.md §2). */
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded bg-raised", className)} aria-hidden />;
}
