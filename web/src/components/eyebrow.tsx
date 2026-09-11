// Mono section eyebrow — structure as information (web-ui-design.md §2).
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

export function Eyebrow({ children, className }: { children: ReactNode; className?: string }) {
  return <h2 className={cn("eyebrow", className)}>{children}</h2>;
}
