// "Report issue" — design canvas 9k/13ai. The promise of this dialog is in
// its subtitle: nothing is sent silently, and the preview *is* the payload.
// So the block below is built from the same lines the issue body is, and
// only from values that already left the server as an answer to this browser:
// the route, the status, the server's own sentence, and the agent versions
// the fleet reports. The canvas also lists a trace id and the cypherd version;
// cypherd stamps neither on anything the API returns, so those lines are not
// drawn rather than drawn with a made-up value. The "attach the last 20 panel
// log lines" row is missing for the same reason — there is no panel-log
// endpoint to read them from.
import { useMemo } from "react";
import { ApiError, NetworkError, requestLineOf } from "@/api/client";
import { useListServers } from "@/api/gen/servers/servers";
import { Dialog, DialogClose, DialogContent } from "@/components/ui/dialog";

const ISSUES_URL = "https://github.com/MaramHarsha/CypherPanel/issues/new";

export function ReportIssueDialog({
  open,
  onOpenChange,
  error,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  error: unknown;
}) {
  // Only asked for while the dialog is up — the page this sits on has just
  // failed, and a fault page that fires more requests on mount is not calm.
  const servers = useListServers({ query: { enabled: open } });

  const lines = useMemo(() => {
    const out: string[] = [];
    const route = requestLineOf(error);
    if (route) out.push(`route: ${route}`);
    else out.push(`page: ${window.location.pathname}`);
    if (error instanceof ApiError) out.push(`status: ${error.status}`);
    else if (error instanceof NetworkError) out.push("status: no response");
    const message = error instanceof Error ? error.message : String(error);
    if (message) out.push(`error: ${message}`);
    const fleet = servers.data ?? [];
    if (fleet.length > 0) {
      const versions = [...new Set(fleet.map((s) => s.agent_version).filter(Boolean))];
      out.push(
        `agent: ${versions.length ? versions.join(" / ") : "unknown"} (${fleet.length} server${fleet.length === 1 ? "" : "s"})`,
      );
    }
    return out;
  }, [error, servers.data]);

  const openIssue = () => {
    const params = new URLSearchParams({
      title: `Panel fault: ${requestLineOf(error) ?? "render error"}`,
      body: `${lines.join("\n")}\n\nWhat I was doing:\n`,
    });
    window.open(`${ISSUES_URL}?${params.toString()}`, "_blank", "noopener,noreferrer");
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="alert"
        title="Report this to the project"
        description="Opens a pre-filled public GitHub issue. Nothing is sent silently — this is everything it will contain:"
      >
        {/* Ink in both themes, like a log pane: this is a machine payload, not prose. */}
        <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-toast px-3.5 py-3 font-mono text-[12px] leading-[1.7] text-toast-text">
          {lines.join("\n")}
          {"\n"}
          <span className="text-pane-ok">— no hostnames, no env vars, no logs, no IPs —</span>
        </pre>
        <div className="mt-[18px] flex items-center justify-end gap-2">
          <DialogClose className="rounded-full px-3.5 py-[9px] text-[13px] text-text-mid hover:text-text">
            Cancel
          </DialogClose>
          <button
            type="button"
            onClick={openIssue}
            className="rounded-full bg-primary px-[18px] py-[9px] text-[13px] font-semibold text-primary-fg hover:bg-primary-hover"
          >
            Open GitHub issue ↗
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
