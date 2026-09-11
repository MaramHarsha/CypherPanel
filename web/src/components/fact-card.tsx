// FactCard — the read-only "here are the facts" surface that carries most of
// the detail pages: a mono eyebrow inside a bordered card, then label/value
// rows. Labels are the plain-language name, values are machine state in mono,
// which is the mono-as-identity bet applied at row scale.
import { type ReactNode } from "react";
import { cn } from "@/lib/utils";

export function FactCard({
  title,
  children,
  className,
  actions,
}: {
  title: string;
  children: ReactNode;
  className?: string;
  actions?: ReactNode;
}) {
  return (
    <section className={cn("rounded-lg border border-border bg-surface p-4.5", className)}>
      <div className="mb-3.5 flex items-center justify-between gap-3">
        <h2 className="eyebrow">{title}</h2>
        {actions}
      </div>
      <dl className="space-y-2.5 text-[13px]">{children}</dl>
    </section>
  );
}

export function Fact({ label, children }: { label: string; children: ReactNode }) {
  // A truncated domain or image ref is state the operator can no longer read,
  // and ui-principles §10 does not allow the panel to imply it knows something
  // it is not showing. Whenever the value is plain text the full string stays
  // reachable on hover; the row keeps its single-line rhythm either way.
  const full = typeof children === "string" || typeof children === "number" ? String(children) : undefined;
  return (
    // Below `sm` the pair stacks and the value goes left: at 360px a label and
    // a value competing for one line leaves the value a dozen characters, and
    // the value is the reason the row exists (ui-principles §9).
    <div className="flex flex-col gap-0.5 sm:flex-row sm:items-baseline sm:justify-between sm:gap-4">
      <dt className="text-text-mid sm:shrink-0">{label}</dt>
      <dd title={full} className="min-w-0 truncate font-mono text-[12.5px] text-text sm:text-right">
        {children}
      </dd>
    </div>
  );
}
