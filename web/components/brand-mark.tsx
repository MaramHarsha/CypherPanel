// The CypherPanel mark: a "C" built from a block grid — encrypted blocks,
// container grid, and dashboard panels in one. The accent block is the
// deployed revision.
//
// Geometry is the approved 160-unit grid (7 blocks of 40, rx 9) and must not
// be redrawn per surface — this component is the single source, so the mark
// can never drift between the sidebar, the login screen, and the favicon.

type BrandMarkProps = {
  className?: string;
  /**
   * Renders the mark on its own accent tile (the app-icon lockup) instead of
   * as bare blocks. Use where the mark needs a bounded shape of its own.
   */
  tile?: boolean;
};

export function BrandMark({ className, tile = false }: BrandMarkProps) {
  if (tile) {
    return (
      <svg
        viewBox="0 0 160 160"
        className={className}
        role="img"
        aria-label="CypherPanel"
        fill="none"
      >
        <rect width="160" height="160" rx="36" fill="var(--brand-accent)" />
        {/* Blocks punch the C out of the tile, so they take the surface colour
            rather than a fixed ink value. */}
        <g fill="var(--background)">
          <rect x="26" y="26" width="32" height="32" rx="8" />
          <rect x="64" y="26" width="32" height="32" rx="8" />
          <rect x="102" y="26" width="32" height="32" rx="8" />
          <rect x="26" y="64" width="32" height="32" rx="8" />
          <rect x="26" y="102" width="32" height="32" rx="8" />
          <rect x="64" y="102" width="32" height="32" rx="8" />
          <rect x="102" y="102" width="32" height="32" rx="8" />
        </g>
      </svg>
    );
  }

  return (
    <svg
      viewBox="0 0 160 160"
      className={className}
      role="img"
      aria-label="CypherPanel"
      fill="none"
    >
      {/* currentColor so the mark inherits whatever surface it sits on and
          stays legible in both themes; only the accent block is fixed. */}
      <g fill="currentColor">
        <rect x="14" y="14" width="40" height="40" rx="9" />
        <rect x="60" y="14" width="40" height="40" rx="9" />
        <rect x="14" y="60" width="40" height="40" rx="9" />
        <rect x="14" y="106" width="40" height="40" rx="9" />
        <rect x="60" y="106" width="40" height="40" rx="9" />
        <rect x="106" y="106" width="40" height="40" rx="9" />
      </g>
      <rect x="106" y="14" width="40" height="40" rx="9" fill="var(--brand-accent)" />
    </svg>
  );
}
