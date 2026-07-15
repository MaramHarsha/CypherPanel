import { cn } from "@/lib/utils";
import type { ServerInfo } from "@/lib/api";

type Service = NonNullable<ServerInfo["services"]>[number];

// active → green, activating → amber, everything else (inactive/failed/
// unknown) → red. Managed-service health at a glance.
function toneFor(state?: string): string {
  switch (state) {
    case "active":
      return "bg-success/12 text-success";
    case "activating":
      return "bg-warning/15 text-warning-foreground dark:text-warning";
    default:
      return "bg-destructive/12 text-destructive";
  }
}

export function ServiceChips({ services }: { services?: Service[] }) {
  if (!services || services.length === 0) {
    return <span className="text-xs text-muted-foreground">No managed services reported</span>;
  }
  return (
    <div className="flex flex-wrap gap-1">
      {services.map((s) => (
        <span
          key={s.name}
          title={`${s.name}: ${s.state}`}
          className={cn(
            "inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium",
            toneFor(s.state),
          )}
        >
          <span className="h-1.5 w-1.5 rounded-full bg-current" />
          {s.name}
        </span>
      ))}
    </div>
  );
}
