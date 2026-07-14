import { cn } from "@/lib/utils";
import type { ServerInfo } from "@/lib/api";

type Service = NonNullable<ServerInfo["services"]>[number];

// active → green, activating → amber, everything else (inactive/failed/
// unknown) → red. Managed-service health at a glance.
function toneFor(state?: string): string {
  switch (state) {
    case "active":
      return "bg-green-500/15 text-green-700 dark:text-green-400";
    case "activating":
      return "bg-amber-500/15 text-amber-700 dark:text-amber-400";
    default:
      return "bg-red-500/15 text-red-700 dark:text-red-400";
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
