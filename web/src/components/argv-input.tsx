// ArgvInput — a scheduled-task command as an argv list, mirroring ADR-011:
// never a shell-string textbox (each token is passed straight to exec, so no
// shell parsing, no injection surface).
//
// Canvas 9c draws that as one field holding inline mono chips — `npm` `run`
// `migrate` — rather than a numbered stack of inputs. The chips ARE the point:
// the boundary between two arguments is a thing you can see, which is exactly
// what a shell string hides. The field's label carries the explanation
// ("· argv, never a shell string"), so no paragraph is needed under it.
//
// Each chip stays a real input with a real accessible name: the canvas is a
// static mockup, and it does not get to cost a keyboard user the ability to
// edit or remove an argument. The field as a whole is a labelled group, so the
// visible heading ("Command · argv, never a shell string") is what a screen
// reader hears on entering it, before the first chip's own name.
import { X } from "lucide-react";

interface ArgvInputProps {
  value: string[];
  onChange: (value: string[]) => void;
  /** id of the visible heading — becomes the group's accessible name. */
  labelledBy?: string;
}

export function ArgvInput({ value, onChange, labelledBy }: ArgvInputProps) {
  const args = value.length === 0 ? [""] : value;

  const set = (i: number, v: string) => onChange(args.map((a, idx) => (idx === i ? v : a)));
  const add = () => onChange([...args, ""]);
  const remove = (i: number) => onChange(args.filter((_, idx) => idx !== i));

  return (
    <div
      role="group"
      aria-labelledby={labelledBy}
      className="flex flex-wrap items-center gap-1.5 rounded-md border border-border-input bg-surface px-2.5 py-2"
    >
      {args.map((arg, i) => {
        const placeholder = i === 0 ? "command" : "argument";
        return (
          // The ring is the chip's, not the bare input's: an argument is a
          // token you can see the edges of, so the focused edge should be that
          // same edge. It stays keyboard-only (`:focus-visible`) and stays the
          // system's one ring — 2px orange at 3px offset (canvas 14g).
          <span
            key={i}
            className="inline-flex items-center gap-1 rounded-[4px] bg-text/[0.06] px-2 py-[3px] font-mono text-[12px] text-text has-[input:focus-visible]:outline-2 has-[input:focus-visible]:outline-offset-[3px] has-[input:focus-visible]:outline-focus"
          >
            <input
              value={arg}
              onChange={(e) => set(i, e.target.value)}
              placeholder={placeholder}
              autoComplete="off"
              spellCheck={false}
              aria-label={i === 0 ? "Command" : `Argument ${i}`}
              // Mono, so a character count is a width: the chip grows with the
              // token instead of reserving a full row for a three-letter verb.
              style={{ width: `${Math.max(arg.length, placeholder.length, 3)}ch` }}
              // Ringless on purpose — the chip around it takes the ring.
              className="bg-transparent text-text outline-none placeholder:text-text-faint"
            />
            {args.length > 1 && (
              <button
                type="button"
                aria-label={`Remove argument ${i}`}
                onClick={() => remove(i)}
                className="-mr-0.5 rounded-[3px] text-text-faint hover:text-text"
              >
                <X className="h-3 w-3" />
              </button>
            )}
          </span>
        );
      })}
      <button
        type="button"
        onClick={add}
        className="rounded-[4px] px-1 py-[3px] font-mono text-[12px] text-text-faint hover:text-text-mid"
      >
        add arg…
      </button>
    </div>
  );
}
