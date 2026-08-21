// SSEBanner (ui-principles §10): never show state you can't verify — on
// stream drop, say "reconnecting" and mark the data stale, never frozen-fresh.
import { WifiOff } from "lucide-react";
import { cn } from "@/lib/utils";
import { type SSEStatus } from "@/lib/use-sse";

export function SSEBanner({ status, tone = "paper" }: { status: SSEStatus; tone?: "paper" | "ink" }) {
  if (status === "open") return null;
  return (
    <div
      role="status"
      className={cn(
        "flex items-center gap-2 border-b border-status-degraded/30 bg-status-degraded/10 px-4 py-1.5 text-xs",
        // --status-degraded-text is the amber darkened until it holds 4.5:1 on
        // paper; inside the log pane, which is ink in both themes, that same
        // brown lands on near-black. The warning has to stay the most legible
        // thing on the pane, so ink keeps the undarkened amber.
        tone === "ink" ? "text-status-degraded" : "text-status-degraded-text",
      )}
    >
      <WifiOff className="h-3.5 w-3.5" aria-hidden />
      {status === "connecting" ? "Connecting to live updates…" : "Reconnecting — data may be stale"}
    </div>
  );
}
