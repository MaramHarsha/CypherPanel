// LogViewer (web-ui-design.md §6): replay + live tail over one SSE stream,
// autoscroll with opt-out, mono, wrap toggle. The live tail is one of the
// three earned animations — the content itself moves; nothing else does.
import { ArrowDownToLine, WrapText } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { SSEBanner } from "@/components/sse-banner";
import { cn } from "@/lib/utils";
import { useSSE } from "@/lib/use-sse";

const MAX_LINES = 5000;

export function LogViewer({ url, className }: { url: string; className?: string }) {
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
      <SSEBanner status={status} />
      <div className="flex items-center justify-between border-b border-pane-border px-2.5 py-1">
        <span className="eyebrow text-pane-faint">log</span>
        <div className="flex items-center gap-1">
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
          <p className="font-mono text-xs text-pane-faint">Waiting for output…</p>
        ) : (
          <pre
            className={cn(
              "font-mono text-[11.5px] leading-[1.75] text-pane-text",
              wrap ? "whitespace-pre-wrap break-all" : "whitespace-pre",
            )}
          >
            {lines.join("\n")}
          </pre>
        )}
      </div>
    </div>
  );
}
