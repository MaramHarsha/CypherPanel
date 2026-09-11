// Inputs are set in mono: nearly everything typed into this panel is machine
// state (domains, repos, images, cron, emails), and the mono-as-identity bet
// (web-ui-design.md §2) is worth more than a uniform sans field. Focus deepens
// the border to ink and adds a hairline ring, so nothing shifts.
//
// The resting border is --border-input, a step darker than the row hairline:
// a field bounded by the same line that separates table rows stops reading as
// something you can type into (canvas 9a draws both, one above the other).
import { forwardRef, type InputHTMLAttributes, type SelectHTMLAttributes, type TextareaHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

const base =
  "w-full rounded-md border border-border-input bg-surface px-3 font-mono text-[13px] text-text placeholder:text-text-faint " +
  "transition-colors focus-visible:border-border-strong focus-visible:ring-1 focus-visible:ring-border-strong " +
  "focus-visible:outline-none disabled:opacity-50";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => <input ref={ref} className={cn(base, "h-9", className)} {...props} />,
);
Input.displayName = "Input";

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, ...props }, ref) => (
    <textarea ref={ref} className={cn(base, "min-h-20 py-2 leading-relaxed", className)} {...props} />
  ),
);
Textarea.displayName = "Textarea";

/** Native select styled to match Input — engines, roles, notifier channels. */
export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, ...props }, ref) => (
    <select ref={ref} className={cn(base, "h-9 cursor-pointer pr-8", className)} {...props} />
  ),
);
Select.displayName = "Select";
