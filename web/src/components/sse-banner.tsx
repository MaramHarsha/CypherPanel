// SSEBanner (ui-principles §10): never show state you can't verify — on
// stream drop, say "reconnecting" and mark the data stale, never frozen-fresh.
import { WifiOff } from "lucide-react";
import { type SSEStatus } from "@/lib/use-sse";

export function SSEBanner({ status }: { status: SSEStatus }) {
  if (status === "open") return null;
  return (
    <div
      role="status"
      className="flex items-center gap-2 border-b border-status-degraded/30 bg-status-degraded/10 px-4 py-1.5 text-xs text-status-degraded"
    >
      <WifiOff className="h-3.5 w-3.5" aria-hidden />
      {status === "connecting" ? "Connecting to live updates…" : "Reconnecting — data may be stale"}
    </div>
  );
}
