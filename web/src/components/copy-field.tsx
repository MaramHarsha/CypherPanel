// CopyField / SecretField (web-ui-design.md §6): every generated value has a
// copy button; secrets are write-only, masked until revealed by a click.
import { Check, Copy, Eye, EyeOff } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={copied ? "Copied" : label}
      onClick={() => {
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        });
      }}
      className="rounded p-1 text-text-faint hover:bg-raised hover:text-text"
    >
      {copied ? <Check className="h-3.5 w-3.5 text-status-running" /> : <Copy className="h-3.5 w-3.5" />}
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
