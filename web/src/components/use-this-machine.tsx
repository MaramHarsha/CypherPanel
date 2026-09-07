// "Use this machine" — the second, quieter path in the Join a server dialog
// (local-server.md §8).
//
// The paste stays the primary, because most servers are not this one. But the
// single most common FIRST server is the machine the panel is already installed
// on, and the join command works there as well as anywhere — it is just not
// discoverable, because the dialog says "run this on the server you want to
// add" and the box you are already signed into is not obviously one of those.
//
// When the panel cannot do it, the button is REPLACED by the sentence that says
// why, never disabled without one: a dead control with no explanation is what
// ui-principles §11 is named against. The three reasons it can be unavailable
// are all version or install facts the operator can act on.
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Monitor } from "lucide-react";
import {
  getGetLocalServerQueryKey,
  getListServersQueryKey,
  useCreateLocalServer,
  useGetLocalServer,
} from "@/api/gen/servers/servers";
import { ActionButton } from "@/components/ui/action-button";
import { toastFailed } from "@/lib/toast";

export function UseThisMachine({ onStarted }: { onStarted?: (serverId: string) => void }) {
  const qc = useQueryClient();
  // While an install is running the helper's phase changes on its own, so this
  // polls — and stops once it reaches an end state, because a screen that keeps
  // asking after the answer arrives is just noise on someone's bill.
  const local = useGetLocalServer({
    query: {
      refetchInterval: (q) => {
        const phase = q.state.data?.phase;
        return phase === "installing" || phase === "enrolling" ? 3_000 : false;
      },
      retry: false,
    },
  });

  const create = useCreateLocalServer({
    mutation: {
      onSuccess: (res) => {
        void qc.invalidateQueries({ queryKey: getGetLocalServerQueryKey() });
        void qc.invalidateQueries({ queryKey: getListServersQueryKey() });
        onStarted?.(res.server.id);
      },
      onError: (e: unknown) => toastFailed("Could not add this machine", e),
    },
  });

  const state = local.data;
  if (local.isPending || !state) return null;

  return (
    <div className="rounded-lg border border-border bg-raised px-4 py-3.5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-2 text-[13px] font-semibold text-text">
            <Monitor className="h-3.5 w-3.5 shrink-0" aria-hidden />
            This machine
            {state.hostname && <span className="mono text-[11.5px] font-normal text-text-faint">{state.hostname}</span>}
          </p>
          <p className="mt-0.5 text-[12.5px] leading-[1.5] text-text-mid">
            {describe(state.state, state.phase)}
          </p>
        </div>

        <div className="shrink-0">
          {state.state === "already_joined" && state.server_id ? (
            <Link
              to="/servers/$serverId"
              params={{ serverId: state.server_id }}
              className="inline-block rounded-full border border-border-input bg-surface px-4 py-1.5 text-[12.5px] font-semibold hover:border-border-strong"
            >
              Open it →
            </Link>
          ) : state.state === "available" ? (
            <ActionButton
              variant="secondary"
              size="sm"
              state={create.isPending || state.phase === "installing" || state.phase === "enrolling" ? "busy" : "idle"}
              busyLabel="Installing…"
              onClick={() => create.mutate()}
            >
              Use this machine
            </ActionButton>
          ) : null}
        </div>
      </div>

      {/* The reason replaces the button rather than sitting beside a disabled
          one. Each of these is a fact the operator can act on. */}
      {state.reason && state.state !== "already_joined" && (
        <p className="mt-2 border-t border-border-subtle pt-2 text-[12px] leading-[1.5] text-text-faint">
          {state.reason}
        </p>
      )}
      {state.phase === "failed" && state.detail && (
        <p role="alert" className="mt-2 rounded-md border border-danger/35 bg-danger/[0.06] px-3 py-2 text-[12px] leading-[1.5] text-danger">
          {state.detail}
        </p>
      )}
    </div>
  );
}

/**
 * One sentence per state. The in-flight phases come from the root helper's own
 * status file, so they are facts about a real process rather than a spinner on
 * a timer (ui-principles §3).
 */
function describe(state: string, phase?: string): string {
  if (phase === "installing") return "Installing the agent on this host…";
  if (phase === "enrolling") return "Enrolling — it appears in the fleet within one heartbeat.";
  switch (state) {
    case "available":
      return "Install the agent here and deploy to the same box the panel runs on. No command to paste.";
    case "already_joined":
      return "Already running an agent for this panel.";
    default:
      // helper_missing / unsupported both carry their own reason line below,
      // so this stays the neutral half of the sentence.
      return "Adding this host from the panel is not available.";
  }
}
