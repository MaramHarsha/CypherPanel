// EmptyState — one sentence + one primary action, quiet (web-ui-design.md §6).
// On fresh panels these chain the golden path (ui-principles §11).
import { type ReactNode } from "react";

interface EmptyStateProps {
  title: string;
  /** The beginner-first "what belongs here / what happens next" line. */
  hint?: string;
  action?: ReactNode;
}

export function EmptyState({ title, hint, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-start gap-3 rounded-md border border-dashed border-border px-5 py-8">
      <div>
        <p className="text-sm font-medium text-text">{title}</p>
        {hint && <p className="mt-1 max-w-md text-[13px] leading-relaxed text-text-mid">{hint}</p>}
      </div>
      {action}
    </div>
  );
}
