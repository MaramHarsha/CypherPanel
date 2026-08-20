// Field: label + control + the beginner-first one-line hint (ui-principles
// §11 — the plain-language line lives inline, not in a tooltip).
//
// The label is 12px/600 with an optional lighter qualifier on the same line —
// "Name · what will use it", "Secret key · write-only" — which is how every
// form label in the canvas is set (9a, 9c, 9e, 12c, 13aj).
import { type ReactNode, useId } from "react";
import { cn } from "@/lib/utils";

interface FieldProps {
  label: string;
  /** The lighter half of the label: "· what will use it". */
  qualifier?: ReactNode;
  hint?: string;
  error?: string;
  children: (id: string) => ReactNode;
  className?: string;
}

export function Field({ label, qualifier, hint, error, children, className }: FieldProps) {
  const id = useId();
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={id} className="block text-[12px] font-semibold text-text">
        {label}
        {qualifier && <span className="font-normal text-text-faint"> {qualifier}</span>}
      </label>
      {children(id)}
      {hint && !error && <p className="text-xs leading-relaxed text-text-faint">{hint}</p>}
      {error && <p className="text-xs text-danger">{error}</p>}
    </div>
  );
}
