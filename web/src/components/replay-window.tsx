// Where a log replay starts (`?since=`, deployment-control.md), for both panes
// that offer one. A picker rather than a free field: the windows an operator
// actually reaches for are the last few minutes, the last hour, the last day
// and everything retained, and the API answers 400 for anything it cannot
// parse rather than quietly falling back — not a failure worth exposing a text
// box to earn.
//
// Two shapes of the same choice, because the phone has nowhere to put four
// chips: the row sits in the pane's desktop header, and 14d draws "1h ▾" on
// the pane itself, beside the LIVE pill.
import { ChevronDown } from "lucide-react";
import { Dropdown, DropdownContent, DropdownItem, DropdownTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/utils";

const REPLAY_WINDOWS = [
  { value: "15m", label: "15m" },
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "", label: "all" },
] as const;

interface PickerProps {
  value: string;
  onChange: (next: string) => void;
}

/** Mono, bordered, the same chip row the audit filters use. */
export function ReplayWindowChips({ value, onChange }: PickerProps) {
  return (
    <span className="flex items-center gap-1" role="group" aria-label="Replay window">
      <span className="mono mr-0.5 text-[11px] text-text-faint">from</span>
      {REPLAY_WINDOWS.map((w) => (
        <button
          key={w.value}
          type="button"
          aria-pressed={value === w.value}
          onClick={() => onChange(w.value)}
          className={cn(
            "mono rounded border px-2 py-[3px] text-[11px] transition-colors",
            value === w.value
              ? "border-border-strong bg-raised font-medium text-text"
              : "border-border text-text-mid hover:text-text",
          )}
        >
          {w.label}
        </button>
      ))}
    </span>
  );
}

/** The same choice on a phone, drawn on the ink pane: 14d's "1h ▾". The
 *  trigger wears the window rather than a word for it, so the screen still
 *  says how far back what you are reading goes. */
export function ReplayWindowMenu({ value, onChange }: PickerProps) {
  const current = REPLAY_WINDOWS.find((w) => w.value === value) ?? REPLAY_WINDOWS[3];
  return (
    <Dropdown>
      <DropdownTrigger
        aria-label={`Replay window — ${current.label}`}
        className="inline-flex flex-none items-center gap-1.5 rounded-md border border-pane-border px-2.5 py-[5px] text-[11.5px] text-pane-text"
      >
        {current.label} <ChevronDown className="h-3 w-3" aria-hidden />
      </DropdownTrigger>
      <DropdownContent align="end">
        {REPLAY_WINDOWS.map((w) => (
          <DropdownItem
            key={w.value}
            onSelect={() => onChange(w.value)}
            className={cn(value === w.value && "bg-raised font-semibold")}
          >
            {w.label}
          </DropdownItem>
        ))}
      </DropdownContent>
    </Dropdown>
  );
}
