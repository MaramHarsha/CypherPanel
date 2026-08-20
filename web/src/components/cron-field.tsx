// CronField (web-ui-design.md §6): a 5-field cron input with a plain-language
// reading and the next few run times, parsed client-side so the operator sees
// what they typed before they save.
import { useMemo } from "react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

interface CronFieldProps {
  value: string;
  onChange: (value: string) => void;
}

export function CronField({ value, onChange }: CronFieldProps) {
  const parsed = useMemo(() => nextRuns(value, 3), [value]);
  return (
    <div className="space-y-1.5">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cn("mono", !parsed.ok && "border-danger")}
        aria-label="Cron schedule"
        spellCheck={false}
        autoComplete="off"
      />
      {parsed.ok ? (
        // The preview is the one green thing in the modal: it is the proof that
        // what was typed parses, so it reads like a result rather than a hint.
        <p className="font-mono text-[11px] text-status-running">
          next: {parsed.runs.map(formatRun).join(" · ")}
        </p>
      ) : (
        <p className="text-xs text-danger">{parsed.error}</p>
      )}
    </div>
  );
}

// `Aug 11 03:00` — the canvas line, and short enough that three of them stay on
// one row. Local time, deliberately: nextRuns matches against local calendar
// fields, so printing them in any other zone would name an hour it never matched.
const RUN_FORMAT = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

function formatRun(d: Date): string {
  const p: Record<string, string> = {};
  for (const part of RUN_FORMAT.formatToParts(d)) p[part.type] = part.value;
  return `${p.month} ${p.day} ${p.hour}:${p.minute}`;
}

interface CronParts {
  min: number[];
  hour: number[];
  dom: number[];
  mon: number[];
  dow: number[];
}

type ParseResult = { ok: true; runs: Date[] } | { ok: false; error: string };

// A compact standard 5-field cron evaluator: supports *, lists, ranges, and
// */step. Enough to preview the schedules operators actually write; the agent
// remains the source of truth for execution.
function nextRuns(expr: string, count: number): ParseResult {
  const fields = expr.trim().split(/\s+/);
  if (fields.length !== 5) {
    return { ok: false, error: "A cron schedule has five fields: minute hour day month weekday" };
  }
  let parts: CronParts;
  try {
    parts = {
      min: expand(fields[0]!, 0, 59),
      hour: expand(fields[1]!, 0, 23),
      dom: expand(fields[2]!, 1, 31),
      mon: expand(fields[3]!, 1, 12),
      dow: expand(fields[4]!, 0, 6),
    };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : "Invalid cron expression" };
  }

  const runs: Date[] = [];
  const t = new Date();
  t.setSeconds(0, 0);
  t.setMinutes(t.getMinutes() + 1);
  const domRestricted = fields[2] !== "*";
  const dowRestricted = fields[4] !== "*";

  for (let i = 0; i < 366 * 24 * 60 && runs.length < count; i++) {
    const dayOk = domRestricted && dowRestricted
      ? parts.dom.includes(t.getDate()) || parts.dow.includes(t.getDay())
      : parts.dom.includes(t.getDate()) && parts.dow.includes(t.getDay());
    if (
      parts.min.includes(t.getMinutes()) &&
      parts.hour.includes(t.getHours()) &&
      parts.mon.includes(t.getMonth() + 1) &&
      dayOk
    ) {
      runs.push(new Date(t));
    }
    t.setMinutes(t.getMinutes() + 1);
  }
  if (runs.length === 0) return { ok: false, error: "This schedule never runs" };
  return { ok: true, runs };
}

function expand(field: string, lo: number, hi: number): number[] {
  const out = new Set<number>();
  for (const piece of field.split(",")) {
    let step = 1;
    let range = piece;
    const slash = piece.indexOf("/");
    if (slash !== -1) {
      step = Number(piece.slice(slash + 1));
      range = piece.slice(0, slash);
      if (!Number.isInteger(step) || step < 1) throw new Error(`Invalid step in "${piece}"`);
    }
    let start = lo;
    let end = hi;
    if (range !== "*") {
      const dash = range.indexOf("-");
      if (dash !== -1) {
        start = Number(range.slice(0, dash));
        end = Number(range.slice(dash + 1));
      } else {
        start = end = Number(range);
      }
      if (!Number.isInteger(start) || !Number.isInteger(end) || start < lo || end > hi || start > end) {
        throw new Error(`"${piece}" is out of range (${lo}–${hi})`);
      }
    }
    for (let v = start; v <= end; v += step) out.add(v);
  }
  return [...out];
}
