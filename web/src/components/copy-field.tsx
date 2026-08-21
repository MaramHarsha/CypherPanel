// CopyField / SecretField (web-ui-design.md §6): every generated value has a
// copy button; secrets are write-only, masked until revealed by a click.
//
// Both are field-shaped, so both take the field's resting line
// (--border-input) rather than the row hairline: a box bounded by the line
// that separates table rows stops reading as a thing you can act on, which is
// exactly the mistake these two used to make sitting next to real Inputs.
import { Check, Copy, Eye, EyeOff, X } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";

/** Padding and line that make a read-only value box match a live Input. */
const shell =
  "flex min-w-0 items-center justify-between gap-2 rounded-md border border-border-input bg-surface py-1.5 pl-3 pr-2";

/**
 * Put `value` on the clipboard without trusting the browser to work out what
 * "the selection" means.
 *
 * The legacy path used to select a hidden textarea and let the default copy
 * handler take whatever was selected — which is why the button could report
 * success while the clipboard stayed empty: `execCommand` returns whether the
 * command *ran*, not whether anything was written, and any interference with
 * the selection (an overlay, a focus trap, a browser that declines to select an
 * off-screen node) leaves it copying nothing. Handling the `copy` event
 * ourselves fixes both halves: `setData` states the exact text, and the
 * handler firing is real proof, so the checkmark can only appear when the write
 * actually happened.
 */
function legacyCopy(value: string): boolean {
  let handled = false;
  const onCopy = (e: ClipboardEvent) => {
    e.clipboardData?.setData("text/plain", value);
    e.preventDefault();
    handled = true;
  };
  document.addEventListener("copy", onCopy);

  // `execCommand("copy")` is a no-op with an empty selection, so a selected
  // node still has to exist — but it is only there to arm the command. What
  // lands on the clipboard is the string above.
  const ta = document.createElement("textarea");
  ta.value = value;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.top = "-1000px";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  try {
    ta.select();
    ta.setSelectionRange(0, value.length);
    document.execCommand("copy");
  } catch {
    // Ignored: `handled` is the answer, not this call.
  } finally {
    document.removeEventListener("copy", onCopy);
    ta.remove();
  }
  return handled;
}

/**
 * `navigator.clipboard` exists only in a secure context — https, or localhost.
 * A self-hosted panel is routinely reached at `http://<ip>:<port>` before TLS
 * is set up, and the check matters beyond the call failing: awaiting a promise
 * that cannot resolve spends the user gesture, and the synchronous fallback
 * afterwards is then too late for the browser to allow. So the context decides
 * the path up front rather than one being tried and the other picked up late.
 */
async function writeClipboard(value: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Denied by permissions policy — worth one late attempt, which some
      // browsers still honour, but its result is reported honestly.
      return legacyCopy(value);
    }
  }
  return legacyCopy(value);
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  return (
    <button
      type="button"
      // The name stays the name. A control that renames itself to "Copied"
      // mid-interaction has stopped being the copy button as far as a screen
      // reader is concerned; the outcome belongs in the live region below,
      // which is what ui-principles §9 asks status changes to use.
      aria-label={label}
      title={state === "failed" ? "Could not copy — select the text and copy it manually" : label}
      onClick={() => {
        void writeClipboard(value).then((ok) => {
          // Say so when it fails. A button that silently does nothing is worse
          // than one that admits it: the operator stands there re-clicking it.
          setState(ok ? "copied" : "failed");
          setTimeout(() => setState("idle"), ok ? 1500 : 2500);
        });
      }}
      // shrink-0: the value beside it is a domain or a token and will happily
      // take every pixel in the row if the button lets it.
      className="shrink-0 rounded p-1 text-text-faint transition-colors hover:bg-raised hover:text-text"
    >
      {state === "copied" ? (
        <Check className="h-3.5 w-3.5 text-status-running" />
      ) : state === "failed" ? (
        <X className="h-3.5 w-3.5 text-danger" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
      <span role="status" className="sr-only">
        {state === "copied" ? "Copied" : state === "failed" ? "Could not copy" : ""}
      </span>
    </button>
  );
}

export function CopyField({ value, className, mono = true }: { value: string; className?: string; mono?: boolean }) {
  return (
    <div className={cn(shell, className)}>
      <span className={cn("truncate text-[13px]", mono && "font-mono")} title={value}>
        {value}
      </span>
      <CopyButton value={value} label="Copy" />
    </div>
  );
}

export function SecretField({ value, className }: { value: string; className?: string }) {
  const [revealed, setRevealed] = useState(false);
  return (
    <div className={cn(shell, className)}>
      <span className="truncate font-mono text-[13px]">
        {revealed ? value : "•".repeat(Math.min(value.length, 24))}
      </span>
      <span className="flex shrink-0 items-center">
        <button
          type="button"
          aria-label={revealed ? "Hide value" : "Reveal value"}
          onClick={() => setRevealed((r) => !r)}
          className="rounded p-1 text-text-faint transition-colors hover:bg-raised hover:text-text"
        >
          {revealed ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
        <CopyButton value={value} label="Copy value" />
      </span>
    </div>
  );
}
