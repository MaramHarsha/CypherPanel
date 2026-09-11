// InlineHint — the one plain-language line under a field or title
// (ui-principles §11). Present, not hidden in a tooltip.
import { type ReactNode } from "react";

export function InlineHint({ children }: { children: ReactNode }) {
  return <p className="text-xs leading-relaxed text-text-mid">{children}</p>;
}
