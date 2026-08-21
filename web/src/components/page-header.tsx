// The masthead every page shares: mono breadcrumb dateline, a large tight
// headline, an optional status marker beside it, and a right rail for stats
// and the page's actions. The rule underneath separates masthead from content
// — hairline by default, ink when the page is a list whose first row needs the
// stronger line (Mission Control, web-ui-design.md §2).
import { type ReactNode } from "react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useCrumbsValue } from "@/lib/crumbs";
import { cn } from "@/lib/utils";

interface PageHeaderProps {
  title: ReactNode;
  /** Sits inline after the title — a status pill or rollup. */
  badge?: ReactNode;
  /** Right rail: stat readouts, then actions. */
  actions?: ReactNode;
  /** One plain-language line under the title (ui-principles §11). */
  hint?: string;
  /** Tabs or environment strip rendered flush with the bottom rule. */
  below?: ReactNode;
  size?: "lg" | "sm";
  className?: string;
}

export function PageHeader({ title, badge, actions, hint, below, size = "lg", className }: PageHeaderProps) {
  const crumbs = useCrumbsValue();
  return (
    // How much air the band takes above the dateline is set by what sits under
    // the title: a masthead with nothing below it gets 1a's full 34px, one
    // carrying a tab strip gives 4px back (1b), and a resource header — whose
    // title is 28px rather than 34px — starts 26px down (1c).
    <header
      className={cn(
        "border-b border-border px-4 sm:px-8",
        size === "lg" ? (below ? "pt-[30px]" : "pt-[34px]") : "pt-[26px]",
        below ? "pb-0" : "pb-[22px]",
        className,
      )}
    >
      <Breadcrumbs crumbs={crumbs} />
      <div className="mt-2 flex flex-wrap items-end gap-x-4 gap-y-3">
        <div className={cn("flex min-w-0 flex-wrap items-center", size === "lg" ? "gap-4" : "gap-3.5")}>
          <h1 className={cn(size === "lg" ? "page-title" : "text-[28px] font-bold leading-none tracking-[-0.03em]")}>
            {title}
          </h1>
          {badge}
        </div>
        {actions && <div className="ml-auto flex flex-wrap items-end gap-3">{actions}</div>}
      </div>
      {hint && <p className="mt-2.5 max-w-2xl text-[13px] leading-relaxed text-text-mid">{hint}</p>}
      {below && <div className="mt-5">{below}</div>}
    </header>
  );
}

/** A stat readout for the header's right rail: big mono number, small label. */
export function HeaderStat({
  value,
  label,
  tone,
}: {
  value: ReactNode;
  label: string;
  tone?: "default" | "error";
}) {
  return (
    <div className="text-right">
      <div className={cn("font-mono text-[22px] leading-none", tone === "error" && "text-danger")}>{value}</div>
      <div className={cn("mt-1.5 text-[11px]", tone === "error" ? "text-danger" : "text-text-faint")}>{label}</div>
    </div>
  );
}

/** Page body padding, matched to the header's gutters. */
export function PageBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("px-4 py-6 sm:px-8", className)}>{children}</div>;
}
