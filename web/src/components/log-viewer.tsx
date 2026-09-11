// LogViewer (web-ui-design.md §6): replay + live tail over one SSE stream,
// autoscroll with opt-out, mono, wrap toggle. The live tail is one of the
// three earned animations — the content itself moves; nothing else does.
import { ArrowDownToLine, WrapText } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { SSEBanner } from "@/components/sse-banner";
import { cn } from "@/lib/utils";
import { useSSE } from "@/lib/use-sse";

const MAX_LINES = 5000;

// A log pane earns its density by giving the timestamp its own colour column
// and the severity word its own colour, so the eye can skip whichever it does
// not need. Nothing else on the line is touched: a guess painted red would be
// worse than plain text, so anything unrecognised stays --pane-text.
const TIMESTAMP = /^(?:\d{4}-\d{2}-\d{2}[T ])?\d{1,2}:\d{2}(?::\d{2})?(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?/;

const SEVERITY: Record<string, string> = {
  ok: "text-pane-ok",
  ready: "text-pane-ok",
  healthy: "text-pane-ok",
  info: "text-pane-info",
  warn: "text-pane-warn",
  warning: "text-pane-warn",
  err: "text-pane-error",
  error: "text-pane-error",
  fatal: "text-pane-error",
  panic: "text-pane-error",
};

/** One line split into its coloured parts; `severity` is "" when unrecognised. */
function parseLine(line: string): { stamp: string; severity: string; severityClass: string; rest: string } {
  const stamp = TIMESTAMP.exec(line)?.[0] ?? "";
  const after = line.slice(stamp.length);
  // The severity word is only ever the first token after the stamp — matching
  // it anywhere would colour the word "error" inside a message that reports one.
  const token = /^(\s*)\[?([A-Za-z]+)\]?/.exec(after);
  const cls = token ? SEVERITY[token[2]!.toLowerCase()] : undefined;
  if (!token || !cls) return { stamp, severity: "", severityClass: "", rest: after };
  return {
    stamp: stamp + token[1],
    severity: token[0].slice(token[1]!.length),
    severityClass: cls,
    rest: after.slice(token[0].length),
  };
}

export function LogViewer({
  url,
  live = false,
  className,
}: {
  url: string;
  /**
   * True while the thing being logged can still emit — a running deployment, a
   * container tail. cypherd never closes a replay stream (it parks on the
   * context), so the transport says "open" for a build that finished yesterday:
   * only the caller knows whether more output is coming, and until it says so
   * the pane does not promise any.
   */
  live?: boolean;
  className?: string;
}) {
  const [lines, setLines] = useState<string[]>([]);
  const [wrap, setWrap] = useState(false);
  const [follow, setFollow] = useState(true);
  const scrollRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(follow);
  followRef.current = follow;

  const status = useSSE(url, {
    onMessage: useCallback((msg: { data: string }) => {
      setLines((prev) => {
        const next = prev.length >= MAX_LINES ? prev.slice(prev.length - MAX_LINES + 1) : prev.slice();
        next.push(msg.data);
        return next;
      });
    }, []),
  });

  useEffect(() => {
    if (followRef.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [lines]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (atBottom !== followRef.current) setFollow(atBottom);
  };

  return (
    // The log pane is ink in BOTH themes (4e token table): it is already a
    // terminal, and inverting it in light mode would only make it harder to
    // read at 2am.
    <div
      className={cn(
        "flex min-h-0 flex-col overflow-hidden rounded-md border border-pane-border bg-pane",
        className,
      )}
    >
      <SSEBanner status={status} tone="ink" />
      <div className="flex items-center justify-between gap-2 border-b border-pane-border px-2.5 py-1">
        <span className="eyebrow text-pane-faint">log</span>
        <div className="flex items-center gap-1.5">
          <LiveChip live={live} open={status === "open"} following={follow} />
          <button
            type="button"
            aria-pressed={wrap}
            aria-label="Toggle line wrap"
            onClick={() => setWrap((w) => !w)}
            className={cn("rounded p-1 hover:bg-white/10", wrap ? "text-accent" : "text-pane-faint")}
          >
            <WrapText className="h-3.5 w-3.5" />
          </button>
          <button
            type="button"
            aria-pressed={follow}
            aria-label="Follow tail"
            onClick={() => {
              setFollow(true);
              scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
            }}
            className={cn("rounded p-1 hover:bg-white/10", follow ? "text-accent" : "text-pane-faint")}
          >
            <ArrowDownToLine className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto p-3"
        role="log"
        aria-live="off"
      >
        {lines.length === 0 ? (
          <p className="font-mono text-[11px] text-pane-faint">Waiting for output…</p>
        ) : (
          <div
            className={cn(
              "min-w-full font-mono text-[11px] leading-[1.75] text-pane-text",
              wrap ? "whitespace-pre-wrap break-all" : "w-fit whitespace-pre",
            )}
          >
            {lines.map((line, i) => {
              const { stamp, severity, severityClass, rest } = parseLine(line);
              const last = i === lines.length - 1;
              return (
                // min-h keeps a blank line blank rather than collapsing it —
                // spacing in build output is often the only paragraphing it has.
                <div key={i} className="min-h-[1.75em]">
                  {stamp && <span className="text-pane-faint">{stamp}</span>}
                  {severity && <span className={severityClass}>{severity}</span>}
                  {rest}
                  {/* The caret belongs to a stream that is still open AND to a
                      source that can still write. On a finished deploy's replay
                      it would promise output that will never arrive
                      (ui-principles §10). */}
                  {last && live && status === "open" && (
                    <span
                      aria-hidden
                      className="ml-1 inline-block h-[11px] w-[6px] translate-y-[2px] bg-pane-ok"
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * The word for the caret (canvas 14d: "● LIVE"). The caret alone says the
 * stream is open only to someone who knows what a caret means; this says it
 * in a word, and says the other thing too — PAUSED, once the operator has
 * scrolled up to read something and the tail has stopped following them. It
 * is absent when the source cannot write: a finished deploy's replay is
 * neither live nor paused, it is just done — and absent while the stream is
 * down, when the amber banner above is already the louder, truer word.
 * `role="status"` so the change is announced when the tail stops or resumes.
 */
function LiveChip({ live, open, following }: { live: boolean; open: boolean; following: boolean }) {
  if (!live || !open) return null;
  return (
    <span
      role="status"
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-[3px] font-mono text-[10px] font-medium uppercase tracking-wider",
        following ? "border-pane-ok text-pane-ok" : "border-pane-border text-pane-dim",
      )}
    >
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", following ? "bg-pane-ok" : "bg-pane-dim")} />
      {following ? "Live" : "Paused"}
    </span>
  );
}
