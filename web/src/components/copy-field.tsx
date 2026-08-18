// CopyField / SecretField (web-ui-design.md §6): every generated value has a
// copy button; secrets are write-only, masked until revealed by a click.
import { Check, Copy, Eye, EyeOff, X } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";

/**
 * Copy text to the clipboard, however this page happens to be served.
 *
 * `navigator.clipboard` exists only in a secure context — https, or localhost.
 * A self-hosted panel is routinely reached at `http://<ip>:<port>` before TLS
 * is set up, and there the async API is simply `undefined`: the copy button
 * threw, nothing landed on the clipboard, and the button gave no sign either
 * way. The `execCommand` path is deprecated but works on plain HTTP, which is
 * the whole point of having it.
 */
async function writeClipboard(value: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Denied by permissions policy — fall through to the legacy path.
    }
  }
  const ta = document.createElement("textarea");
  ta.value = value;
  // Keep it off-screen and unfocusable-looking, but still selectable: a
  // `display: none` or `hidden` element cannot be selected, so the copy fails.
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.top = "-1000px";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  try {
    ta.select();
    ta.setSelectionRange(0, value.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(ta);
  }
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <button
      type="button"
      aria-label={state === "copied" ? "Copied" : label}
      title={state === "failed" ? "Could not copy — select the text and copy it manually" : label}
      onClick={() => {
        void writeClipboard(value).then((ok) => {
          // Say so when it fails. A button that silently does nothing is worse
          // than one that admits it: the operator stands there re-clicking it.
          setState(ok ? "copied" : "failed");
          setTimeout(() => setState("idle"), ok ? 1500 : 2500);
        });
      }}
      className="rounded p-1 text-text-faint hover:bg-raised hover:text-text"
    >
      {state === "copied" ? (
        <Check className="h-3.5 w-3.5 text-status-running" />
      ) : state === "failed" ? (
        <X className="h-3.5 w-3.5 text-danger" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
}

export function CopyField({ value, className, mono = true }: { value: string; className?: string; mono?: boolean }) {
  return (
    <div
      className={cn(
        "flex min-w-0 items-center justify-between gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5",
        className,
      )}
    >
      <span className={cn("truncate text-[13px]", mono && "mono")} title={value}>
        {value}
      </span>
      <CopyButton value={value} label="Copy" />
    </div>
  );
}

export function SecretField({ value, className }: { value: string; className?: string }) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div
      className={cn(
        "flex min-w-0 items-center justify-between gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5",
        className,
      )}
    >
      <span className="mono truncate text-[13px]">{revealed ? value : "•".repeat(Math.min(value.length, 24))}</span>
      <span className="flex shrink-0 items-center">
        <button
          type="button"
          aria-label={revealed ? "Hide value" : "Reveal value"}
          onClick={() => setRevealed((r) => !r)}
          className="rounded p-1 text-text-faint hover:bg-raised hover:text-text"
        >
          {revealed ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
        <CopyButton value={value} label="Copy value" />
      </span>
    </div>
  );
}
