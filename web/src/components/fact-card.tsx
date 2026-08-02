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
  return (
    <div className="flex items-baseline justify-between gap-4">
      <dt className="shrink-0 text-text-mid">{label}</dt>
      <dd className="min-w-0 truncate text-right font-mono text-[12.5px] text-text">{children}</dd>
    </div>
  );
}
