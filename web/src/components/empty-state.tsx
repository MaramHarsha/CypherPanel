// EmptyState — one sentence + one primary action, quiet (web-ui-design.md §6).
// On fresh panels these chain the golden path (ui-principles §11): each empty
// state is the next step, already in context, so there is no wizard to build.
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

interface EmptyStateProps {
  title: string;
  /** The beginner-first "what belongs here / what happens next" line. */
  hint?: string;
  action?: ReactNode;
  /** Golden-path steps get the accent rule; ordinary empties stay hairline. */
  emphasis?: boolean;
  className?: string;
}

export function EmptyState({ title, hint, action, emphasis, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-start gap-4 rounded-lg border bg-surface px-6 py-8",
        emphasis ? "border-accent/35 bg-accent/[0.03]" : "border-dashed border-border",
        className,
      )}
    >
      <div>
        <p className="text-[15px] font-semibold text-text">{title}</p>
        {hint && <p className="mt-1.5 max-w-lg text-[13px] leading-relaxed text-text-mid">{hint}</p>}
      </div>
      {action}
    </div>
  );
}
