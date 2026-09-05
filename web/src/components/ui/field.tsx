// Field: label + control + the beginner-first one-line hint (ui-principles
// §11 — the plain-language line lives inline, not in a tooltip).
//
// The label is 12px/600 with an optional lighter qualifier on the same line —
// "Name · what will use it", "Secret key · write-only" — which is how every
// form label in the canvas is set (9a, 9c, 9e, 12c, 13aj).
//
// The hint and the error each carry an id, handed to the control as its
// second argument so it can say `aria-describedby={describedBy}` and a screen
// reader hears the hint with the field rather than after it (canvas 14g). The
// error is a polite live region: it is announced when it appears, without
// cutting off whatever was being read.
import { type ReactNode, useId } from "react";
import { cn } from "@/lib/utils";

interface FieldProps {
  label: string;
  /** The lighter half of the label: "· what will use it". */
  qualifier?: ReactNode;
  hint?: string;
  error?: string;
  /** `id` for the control; `describedBy` for its aria-describedby, when set. */
  children: (id: string, describedBy: string | undefined) => ReactNode;
  className?: string;
}

export function Field({ label, qualifier, hint, error, children, className }: FieldProps) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = error ? errorId : hint ? hintId : undefined;
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={id} className="block text-[12px] font-semibold text-text">
        {label}
        {qualifier && <span className="font-normal text-text-faint"> {qualifier}</span>}
      </label>
      {children(id, describedBy)}
      {hint && !error && (
        <p id={hintId} className="text-xs leading-relaxed text-text-faint">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-xs text-danger" aria-live="polite">
          {error}
        </p>
      )}
    </div>
  );
}
