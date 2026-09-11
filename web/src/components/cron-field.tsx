// CronField (web-ui-design.md §6, canvas 9c/13aa): a 5-field cron input with
// the next few run times underneath, parsed client-side so the operator sees
// what they typed before they save.
//
// Which clock the preview runs on is the whole question. The agent evaluates a
// schedule against its own host clock (agent/cron/cron.go: time.Now +
// robfig/cron ParseStandard) and no timezone is stored or exposed anywhere
// (docs/features/scheduled-tasks.md §9 lists it as out of scope) — so the
// panel cannot know the server's zone, and the browser's zone is not it
// either. The honest preview evaluates in UTC, which is what nearly every
// server keeps, and SAYS so, with the caveat that the server's clock decides.
// When the operator reads timestamps in another zone (profile → Timezone) the
// same instants are printed in that zone too, so "03:00 UTC" does not have to
// be converted in their head.
import { useId, useMemo } from "react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

interface CronFieldProps {
  value: string;
  onChange: (value: string) => void;
  /** From `Field`, so the visible label names the input. */
  id?: string;
  describedBy?: string;
  /**
   * IANA zone the operator reads timestamps in (profile → Timezone). Empty
   * or "UTC" prints the UTC line alone; anything else adds the same runs
   * translated into that zone.
   */
  viewerZone?: string;
}

const PREVIEW_COUNT = 3;

export function CronField({ value, onChange, id, describedBy, viewerZone }: CronFieldProps) {
  const previewId = useId();
  const parsed = useMemo(() => cronNextRuns(value, PREVIEW_COUNT), [value]);
  const viewer = useMemo(() => viewerFormatter(viewerZone), [viewerZone]);
  return (
    <div className="space-y-1.5">
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={cn("mono", !parsed.ok && "border-danger")}
        aria-label={id ? undefined : "Cron schedule"}
        aria-invalid={!parsed.ok || undefined}
        aria-describedby={[previewId, describedBy].filter(Boolean).join(" ")}
        spellCheck={false}
        autoComplete="off"
      />
      {parsed.ok ? (
        <div id={previewId} className="space-y-0.5 font-mono text-[11px]">
          {/* The preview is the one green thing in the modal: it is the proof
              that what was typed parses, so it reads like a result rather than
              a hint. */}
          <p className="text-status-running">next: {parsed.runs.map((d) => UTC_FORMAT.format(d)).join(" · ")}</p>
          <p className="text-text-faint">
            shown in UTC — the server's own clock decides
            {viewer && <> · in {viewer.zone}: {parsed.runs.map((d) => viewer.format(d)).join(" · ")}</>}
          </p>
        </div>
      ) : (
        <p id={previewId} aria-live="polite" className="text-xs text-danger">
          {parsed.error}
        </p>
      )}
    </div>
  );
}

// `Aug 11 03:00` — the canvas line, and short enough that three of them stay
// on one row. Assembled from parts because Intl's own string puts a comma
// after the day; en-US on purpose, because a locale-dependent month name would
// move the columns and the panel's machine values are English anyway. The
// year appears only once a run leaves the current one, so a yearly job does
// not print the same three words three times.
interface RunFormatter {
  zone: string;
  format: (d: Date) => string;
}

function runFormatter(zone: string): RunFormatter {
  const f = new Intl.DateTimeFormat("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    // h23, not hour12:false — the latter prints midnight as "24:00" in Chrome.
    hourCycle: "h23",
    timeZone: zone,
  });
  const thisYear = f.formatToParts(new Date()).find((x) => x.type === "year")?.value;
  return {
    zone,
    format: (d) => {
      const p: Partial<Record<Intl.DateTimeFormatPartTypes, string>> = {};
      for (const part of f.formatToParts(d)) p[part.type] = part.value;
      const year = p.year !== thisYear ? ` ${p.year}` : "";
      return `${p.month} ${p.day}${year} ${p.hour}:${p.minute}`;
    },
  };
}

const UTC_FORMAT = runFormatter("UTC");

/** A formatter for the operator's zone, or null when it adds nothing to UTC. */
function viewerFormatter(zone: string | undefined): RunFormatter | null {
  if (!zone || zone.toUpperCase() === "UTC" || zone === "Etc/UTC") return null;
  try {
    return runFormatter(zone);
  } catch {
    // An IANA name this browser does not know: better one true line than a
    // second line in the wrong zone.
    return null;
  }
}

interface CronParts {
  min: Set<number>;
  hour: Set<number>;
  dom: Set<number>;
  mon: Set<number>;
  dow: Set<number>;
  /** robfig's star bit: whether the day fields are unrestricted (`*` / `?`). */
  domStar: boolean;
  dowStar: boolean;
}

export type CronPreview = { ok: true; runs: Date[] } | { ok: false; error: string };

// How far ahead to look before declaring a schedule never runs. Eight years
// covers "29 Feb" across the one century year that skips a leap day.
const HORIZON_MS = 8 * 366 * 24 * 60 * 60 * 1000;

/**
 * The next `count` times `expr` fires, evaluated on the UTC calendar.
 *
 * A compact standard 5-field cron evaluator that follows robfig/cron's
 * standard parser (the one the agent runs) rather than a generic cron: `?` is
 * `*`, month and weekday names are accepted, `N/step` means N to max, and
 * when both day fields are restricted a day matches if EITHER does. The
 * agent remains the source of truth for execution; this only has to agree
 * with it.
 */
export function cronNextRuns(expr: string, count: number): CronPreview {
  const fields = expr.trim().split(/\s+/);
  if (expr.trim() === "" || fields.length !== 5) {
    return { ok: false, error: "A cron schedule has five fields: minute hour day month weekday" };
  }
  let parts: CronParts;
  try {
    const dom = expand(fields[2]!, 1, 31);
    const dow = expand(fields[4]!, 0, 6, DOW_NAMES);
    parts = {
      min: expand(fields[0]!, 0, 59).values,
      hour: expand(fields[1]!, 0, 23).values,
      dom: dom.values,
      mon: expand(fields[3]!, 1, 12, MONTH_NAMES).values,
      dow: dow.values,
      domStar: dom.star,
      dowStar: dow.star,
    };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : "Invalid cron expression" };
  }

  const runs: Date[] = [];
  const t = new Date();
  t.setUTCSeconds(0, 0);
  t.setUTCMinutes(t.getUTCMinutes() + 1);
  const stop = t.getTime() + HORIZON_MS;

  // Walk forward, skipping whole months, days and hours that cannot match, so
  // a yearly schedule costs hundreds of steps rather than millions of minutes.
  while (runs.length < count && t.getTime() < stop) {
    if (!parts.mon.has(t.getUTCMonth() + 1)) {
      t.setUTCMonth(t.getUTCMonth() + 1, 1);
      t.setUTCHours(0, 0, 0, 0);
      continue;
    }
    const domOk = parts.dom.has(t.getUTCDate());
    const dowOk = parts.dow.has(t.getUTCDay());
    const dayOk = parts.domStar || parts.dowStar ? domOk && dowOk : domOk || dowOk;
    if (!dayOk) {
      t.setUTCDate(t.getUTCDate() + 1);
      t.setUTCHours(0, 0, 0, 0);
      continue;
    }
    if (!parts.hour.has(t.getUTCHours())) {
      t.setUTCHours(t.getUTCHours() + 1, 0, 0, 0);
      continue;
    }
    if (parts.min.has(t.getUTCMinutes())) runs.push(new Date(t));
    t.setUTCMinutes(t.getUTCMinutes() + 1);
  }
  if (runs.length === 0) return { ok: false, error: "This schedule never runs" };
  return { ok: true, runs };
}

const MONTH_NAMES: Record<string, number> = {
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
};
const DOW_NAMES: Record<string, number> = { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 };

function expand(
  field: string,
  lo: number,
  hi: number,
  names?: Record<string, number>,
): { values: Set<number>; star: boolean } {
  const values = new Set<number>();
  let star = false;
  // robfig splits on commas and drops empty pieces ("1,,2" is "1,2").
  for (const piece of field.split(",").filter((p) => p !== "")) {
    const [range, stepText, ...moreSlashes] = piece.split("/");
    if (moreSlashes.length > 0) throw new Error(`Too many slashes in "${piece}"`);
    const [startText, endText, ...moreDashes] = range!.split("-");
    if (moreDashes.length > 0) throw new Error(`Too many hyphens in "${piece}"`);

    let start: number;
    let end: number;
    const isStar = startText === "*" || startText === "?";
    if (isStar) {
      start = lo;
      end = hi;
    } else {
      start = parseIntOrName(startText!, names, piece);
      end = endText === undefined ? start : parseIntOrName(endText, names, piece);
    }

    let step = 1;
    if (stepText !== undefined) {
      if (!/^\d+$/.test(stepText)) throw new Error(`Invalid step in "${piece}"`);
      step = Number(stepText);
      if (step === 0) throw new Error(`Step must be a positive number in "${piece}"`);
      // "N/step" means "N-max/step".
      if (!isStar && endText === undefined) end = hi;
    }
    // A stepped star ("*/2") is a restriction, not a wildcard, for the
    // day-of-month/day-of-week rule — exactly as robfig clears the star bit.
    if (isStar && step === 1) star = true;

    if (start < lo || end > hi || start > end) {
      throw new Error(`"${piece}" is out of range (${lo}–${hi})`);
    }
    for (let v = start; v <= end; v += step) values.add(v);
  }
  return { values, star };
}

function parseIntOrName(text: string, names: Record<string, number> | undefined, piece: string): number {
  const named = names?.[text.toLowerCase()];
  if (named !== undefined) return named;
  if (!/^\d+$/.test(text)) throw new Error(`"${piece}" is not a number${names ? " or a name" : ""}`);
  return Number(text);
}
