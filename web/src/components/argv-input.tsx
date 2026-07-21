// ArgvInput — a scheduled-task command as an argv list, mirroring ADR-011:
// never a shell-string textbox (each token is passed straight to exec, so no
// shell parsing, no injection surface).
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

interface ArgvInputProps {
  value: string[];
  onChange: (value: string[]) => void;
}

export function ArgvInput({ value, onChange }: ArgvInputProps) {
  const args = value.length === 0 ? [""] : value;

  const set = (i: number, v: string) => onChange(args.map((a, idx) => (idx === i ? v : a)));
  const add = () => onChange([...args, ""]);
  const remove = (i: number) => onChange(args.filter((_, idx) => idx !== i));

  return (
    <div className="space-y-1.5">
      {args.map((arg, i) => (
        <div key={i} className="flex items-center gap-2">
          <span className="mono w-6 shrink-0 text-right text-xs text-text-faint">{i}</span>
          <Input
            value={arg}
            onChange={(e) => set(i, e.target.value)}
            className="mono"
            placeholder={i === 0 ? "command" : "argument"}
            autoComplete="off"
            spellCheck={false}
            aria-label={`Argument ${i}`}
          />
          {args.length > 1 && (
            <button
              type="button"
              aria-label={`Remove argument ${i}`}
              onClick={() => remove(i)}
              className="rounded p-1 text-text-faint hover:bg-raised hover:text-text"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
      ))}
      <Button type="button" size="sm" variant="ghost" onClick={add}>
        <Plus className="h-3.5 w-3.5" /> Add argument
      </Button>
      <p className="text-xs text-text-faint">
        Each box is one argument passed directly to the program — no shell, so quoting and{" "}
        <span className="mono">{"&&"}</span> don&apos;t apply. Example: <span className="mono">rails</span> ·{" "}
        <span className="mono">db:migrate</span>.
      </p>
    </div>
  );
}
