// The mark: a "C" drawn from seven cells of a 3x3 block grid — encrypted
// blocks, container grid, and dashboard panels in one. The top-right block is
// the accent (the deployed revision); the rest inherit the surrounding text
// colour so the mark works on every surface in both themes.
//
// Geometry matches web/public/favicon.svg and docs/assets/ exactly: 32-unit
// blocks on a 38-unit pitch inside a 108-unit box.
const CELLS = [
  [0, 0],
  [1, 0],
  [2, 0],
  [0, 1],
  [0, 2],
  [1, 2],
  [2, 2],
] as const;

export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 108 108" className={className} aria-hidden focusable="false">
      {CELLS.map(([col, row]) => (
        <rect
          key={`${col}-${row}`}
          x={col * 38}
          y={row * 38}
          width={32}
          height={32}
          rx={8}
          fill={col === 2 && row === 0 ? "var(--mark-accent)" : "currentColor"}
        />
      ))}
    </svg>
  );
}

/** Mark + wordmark, for the sidebar and the unauthenticated entry point. The
 *  mark is sized in em so the lockup keeps its proportions wherever it sits. */
export function Logo({ className }: { className?: string }) {
  return (
    <span className={className}>
      <LogoMark className="h-[1.35em] w-[1.35em] shrink-0" />
      <span className="font-semibold tracking-tight">CypherPanel</span>
    </span>
  );
}
