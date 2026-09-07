// Where the public documentation site lives (docs/features/documentation-site.md).
//
// A constant rather than a panel setting. The site is generated from the same
// repository as the binary and published once, so every panel of a given
// version documents itself at the same address — making it configurable would
// be a field nobody sets and a support answer that starts "well, what did you
// put in it?".
//
// An air-gapped panel reaches nothing here, which is why this is a link in the
// chrome and never a step in a flow: no screen in the panel depends on it.
export const DOCS_URL = "https://cypherpanel.in/docs";

/** A specific guide, for a link that means one page rather than "the docs". */
export function docsGuide(slug: string): string {
  return `${DOCS_URL}/guides/${slug}/`;
}
