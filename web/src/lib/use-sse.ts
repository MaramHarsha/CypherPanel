// The one SSE hook (web-ui-design.md §5): fetch-based (native EventSource
// cannot send the bearer header), replay-buffering, exponential backoff, and
// an honest connection status so regions can mark themselves stale on
// disconnect instead of frozen-fresh (ui-principles §10).
import { fetchEventSource } from "@microsoft/fetch-event-source";
import { useEffect, useRef, useState } from "react";
import { getToken } from "@/lib/auth";

export type SSEStatus = "connecting" | "open" | "reconnecting";

export interface SSEMessage {
  event: string;
  data: string;
}

interface UseSSEOptions {
  /** Called for every message after `connected`. Stable identity not required. */
  onMessage: (msg: SSEMessage) => void;
  /** Pause the stream entirely (e.g. drawer closed). */
  enabled?: boolean;
}

const MAX_BACKOFF_MS = 30_000;

export function useSSE(url: string | null, { onMessage, enabled = true }: UseSSEOptions): SSEStatus {
  const [status, setStatus] = useState<SSEStatus>("connecting");
  const handler = useRef(onMessage);
  handler.current = onMessage;

  useEffect(() => {
    if (!url || !enabled) return;
    const ctrl = new AbortController();
    let attempts = 0;
    setStatus("connecting");

    void fetchEventSource(url, {
      signal: ctrl.signal,
      openWhenHidden: true, // a deploy watched from another tab keeps streaming
      headers: { Authorization: `Bearer ${getToken() ?? ""}` },
      async onopen(res) {
        if (res.ok) {
          attempts = 0;
          setStatus("open");
          return;
        }
        throw new Error(`stream refused: ${res.status}`);
      },
      onmessage(ev) {
        if (ev.event === "connected") return;
        handler.current({ event: ev.event, data: ev.data });
      },
      onclose() {
        // Server closed: reconnect (the library retries when onclose returns).
        setStatus("reconnecting");
      },
      onerror() {
        setStatus("reconnecting");
        attempts += 1;
        // Returning a number tells fetch-event-source to retry after that delay.
        return Math.min(1000 * 2 ** attempts, MAX_BACKOFF_MS);
      },
    }).catch(() => {
      // Aborted on unmount — nothing to do.
    });

    return () => ctrl.abort();
  }, [url, enabled]);

  return status;
}
