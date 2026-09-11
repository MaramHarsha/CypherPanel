// Build strategy — canvas 6b/13b, spec docs/features/build-detection.md and
// docs/features/pack-builds.md.
//
// Auto-detect is the default and the thing a first-timer most needs to know
// exists, so this is a section of its own rather than a select folded into
// Advanced: a segmented control, one paragraph on how detection resolves, and —
// because the build can only fail on the builder, after the form is long gone —
// the failure it would print, shown here in advance with the operator's own
// paths in it. The strings are the agent's exact words
// (agent/builder/detect.go), not a paraphrase: what is previewed is what the
// deploy log will say.
//
// The canvas drew three segments, because three were all that existed. Packs
// added two more answers to the same question — "how does the checkout become
// an image" — so they are segments here rather than a second control beside it:
// splitting them out would say the choice is two decisions when it is one. The
// control keeps 13b's ink border, radius and filled segment; below `sm` it
// stacks, which is what every other wide control in the panel does at that
// width rather than shrinking five labels into thumbnails.
import { type KeyboardEvent, type ReactNode } from "react";
import { AppBuildKind } from "@/api/gen/model";
import { cn } from "@/lib/utils";

const OPTIONS: { value: AppBuildKind; label: string }[] = [
  { value: AppBuildKind.auto, label: "Auto-detect" },
  { value: AppBuildKind.dockerfile, label: "Dockerfile" },
  { value: AppBuildKind.static, label: "Static site" },
  { value: AppBuildKind.nixpacks, label: "Nixpacks" },
  { value: AppBuildKind.railpack, label: "Railpack" },
];

/** detect.go's staticIndexNames, joined the way its errors join them. */
const INDEX_NAMES = "index.html or index.htm";

/**
 * Where a radiogroup's arrow keys land (WAI-ARIA radio pattern): left/up go
 * back, right/down go forward, both wrapping. Null for any other key.
 */
export function radioArrowTarget(key: string, index: number, count: number): number | null {
  if (key === "ArrowRight" || key === "ArrowDown") return (index + 1) % count;
  if (key === "ArrowLeft" || key === "ArrowUp") return (index - 1 + count) % count;
  return null;
}

/** detect.go's describeContext: the directory named the way the agent names it. */
function describeContext(context: string): string {
  const base = context.trim().replace(/\/+$/, "").split("/").pop() ?? "";
  return base === "" || base === "." ? "the repository root" : `${base}/`;
}

/** The exact failure the builder prints for this strategy, or null when the
 *  failure would be the toolchain's own (a Dockerfile that does not build). */
function failurePreview(
  kind: AppBuildKind,
  dockerfilePath: string,
  context: string,
): { eyebrow: string; message: string } | null {
  const dockerfile = dockerfilePath.trim() || "./Dockerfile";
  switch (kind) {
    case "auto":
      return {
        eyebrow: "When neither is found — the build fails early, naming the fix",
        message:
          `could not work out how to build this repository: no Dockerfile at ${dockerfile}, ` +
          `and no ${INDEX_NAMES} to serve as a static site. ` +
          "Add a Dockerfile, or set the build context to the directory that contains your site",
      };
    case "static":
      return {
        eyebrow: "When there is no index file — the build fails early, naming the fix",
        message:
          `this app is set to build as a static site, but no ${INDEX_NAMES} was found in ${describeContext(context)} — ` +
          "point the build context at the directory holding your index.html",
      };
    // A pack chosen explicitly is an assertion — the operator said this is how
    // the repository builds — so a builder without it fails rather than
    // quietly falling back to something else. That refusal is the one worth
    // previewing: it is a property of the fleet, not of the commit, so it is
    // knowable here in a way a failing build command is not.
    case "nixpacks":
      return {
        eyebrow: "When no builder has the pack — the build fails early, naming the fix",
        message:
          "this app is set to build with Nixpacks, but the nixpacks binary is not installed on this builder — " +
          "install it, or set the build to auto or dockerfile",
      };
    case "railpack":
      return {
        eyebrow: "When a builder is missing either half — the build fails early, naming the fix",
        message:
          "this app is set to build with Railpack, but this builder is missing the railpack binary " +
          "or docker buildx — Railpack builds need both, because its output is a BuildKit frontend plan",
      };
    default:
      return null;
  }
}

const mono = "font-mono text-[11.5px]";

function explanation(kind: AppBuildKind, dockerfilePath: string): ReactNode {
  switch (kind) {
    case "dockerfile":
      return (
        <>
          Builds <span className={mono}>{dockerfilePath.trim() || "./Dockerfile"}</span> as written. A Dockerfile is never
          second-guessed — an author who wrote one meant it.
        </>
      );
    case "static":
      return (
        <>
          Serves the build context as a website: an <span className={mono}>index.html</span> at the context root, behind a
          generated nginx image on the app's own port, with SPA fallback.
        </>
      );
    case "nixpacks":
      return (
        <>
          Hands the repository to Nixpacks, which works out the language, package manager, build command and runtime and
          writes a Dockerfile the ordinary path then builds. Configure it with{" "}
          <span className={mono}>nixpacks.toml</span> in the repository — this app&rsquo;s env vars are deliberately not
          passed to it, because a pack takes them as argv and argv is world-readable through{" "}
          <span className={mono}>ps</span>.
        </>
      );
    case "railpack":
      return (
        <>
          Hands the repository to Railpack, which writes a BuildKit plan rather than a Dockerfile — so it builds through{" "}
          <span className={mono}>docker buildx</span> instead of the daemon&rsquo;s classic endpoint. The image it produces
          is indistinguishable from any other: same tag, same labels, same rollout, relay, rollback and cleanup.
          Auto-detect never picks it — choosing between two packs that claim the same repositories would be arbitrary.
        </>
      );
    default:
      return (
        <>
          Resolved <b className="font-semibold text-text">on the builder, at build time</b>, against the commit being
          built. A Dockerfile always wins; then a language manifest (<span className={mono}>package.json</span>,{" "}
          <span className={mono}>go.mod</span>, …) makes it a Nixpacks build, where that pack is installed; then an{" "}
          <span className={mono}>index.html</span> at the context root makes it a static site — served by a generated
          nginx image on the app's own port, with SPA fallback.
        </>
      );
  }
}

export function BuildStrategyField({
  value,
  onChange,
  /** The form's Dockerfile path and build context, so the preview names the
   *  operator's own paths rather than the defaults. */
  dockerfilePath,
  context,
  /** Mono breadcrumb above the control — "new application / build" (13b). */
  eyebrow,
  className,
}: {
  value: AppBuildKind;
  onChange: (kind: AppBuildKind) => void;
  dockerfilePath: string;
  context: string;
  eyebrow?: ReactNode;
  className?: string;
}) {
  const failure = failurePreview(value, dockerfilePath, context);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const target = radioArrowTarget(e.key, index, OPTIONS.length);
    if (target === null) return;
    e.preventDefault();
    const next = OPTIONS[target];
    if (!next) return;
    onChange(next.value);
    // Roving tabindex: the choice and the focus move together, as they do on
    // native radios, so the ring is always on the selected segment.
    const group = e.currentTarget.parentElement;
    const buttons = group?.querySelectorAll<HTMLButtonElement>('[role="radio"]');
    buttons?.[target]?.focus();
  };

  return (
    <div className={cn("space-y-3", className)}>
      {eyebrow && <div className="eyebrow">{eyebrow}</div>}

      {/* 13b: a full-width three-segment control, 1.5px ink border, 8px
          radius, the chosen segment filled ink. The overflow is not clipped so
          the focus ring can draw; the segments round their own outer corners. */}
      <div
        role="radiogroup"
        aria-label="Build strategy"
        className="grid rounded-lg border-[1.5px] border-border-strong text-center text-[12.5px] font-semibold sm:grid-flow-col sm:auto-cols-fr"
      >
        {OPTIONS.map((o, i) => {
          const checked = o.value === value;
          return (
            <button
              key={o.value}
              type="button"
              role="radio"
              aria-checked={checked}
              tabIndex={checked ? 0 : -1}
              onClick={() => onChange(o.value)}
              onKeyDown={(e) => onKeyDown(e, i)}
              className={cn(
                "relative px-2 py-2.5 transition-colors focus-visible:z-10 focus-visible:outline-offset-[-3px]",
                // Stacked below `sm`, so the outer radius is on the top and
                // bottom of the column; in a row it is on the ends.
                "first:rounded-t-[6.5px] last:rounded-b-[6.5px]",
                "sm:first:rounded-l-[6.5px] sm:first:rounded-tr-none sm:last:rounded-r-[6.5px] sm:last:rounded-bl-none",
                i > 0 && "border-t border-border sm:border-l sm:border-t-0",
                checked ? "bg-primary text-primary-fg" : "text-text hover:bg-raised",
                "disabled:cursor-default disabled:opacity-60",
              )}
            >
              {o.label}
            </button>
          );
        })}
      </div>

      <p className="text-[12.5px] leading-[1.55] text-text-mid">{explanation(value, dockerfilePath)}</p>

      {failure && (
        <div className="rounded-lg border border-border bg-surface px-4 py-[13px]">
          <p className="mb-2 font-mono text-[10px] uppercase tracking-[0.1em] text-text-faint">{failure.eyebrow}</p>
          {/* Ink in both themes: this is the deploy log's own line, shown early. */}
          <pre className="whitespace-pre-wrap break-words rounded-md bg-pane px-[13px] py-2.5 font-mono text-[11px] leading-[1.7] text-pane-error">
            {failure.message}
          </pre>
        </div>
      )}

      <p className="text-xs leading-[1.5] text-text-faint">
        A pack is something an operator installs on a builder. Where none is installed, auto-detect behaves exactly as it
        did before packs existed — a framework build is never guessed at, so commit the output or write a Dockerfile.
      </p>
    </div>
  );
}
