# Feature spec: The public documentation site

> Canvas turn 19 (`19a`–`19d`): `cypherpanel.in/docs` — a docs home with the
> complete contents, a guide article, an API endpoint reference generated from
> the OpenAPI spec, and a search overlay opened with `/`. Same Mission Control
> language as the panel, a wider measure for reading.
>
> Written 2026-09-07, just before implementing. Vocabulary per
> [glossary.md](../glossary.md).

## 1. What this is, and what it is not

It is a **static site generated from this repository's own `docs/` tree and
`core/api/rest/openapi.yaml`**. It is not a second copy of the documentation,
not a CMS, and not a page served by `cypherd`.

That single decision settles most of the others, and it is worth stating why it
is the only defensible one here. Hard rule 7 says *no stub files; one topic, one
home*. The documentation already has a home — 47 feature specs, 12 ADRs and six
reference documents, cross-linked to each other and written immediately before
the code they describe (rule 7 again). A hand-written docs site would be a
second home for every one of those topics, and it would be wrong within a week:
the specs change when the features do, and nobody would remember the copy.

The design confirms the reading rather than contradicting it. `19a`'s contents
column says *"47 features, 12 decisions, 6 references"*, and this repository has
exactly 47 files in `docs/features/`, exactly 12 in `docs/adrs/`, and exactly six
reference documents. The canvas was drawn from the tree. `19c` says the API
pages are *"generated from the repo's OpenAPI spec"* in as many words.

So the deliverable is a **renderer**, and the editorial work is a nav map and a
publication decision — not prose.

**Rule 4 is not in play.** "No feature ships UI-only — API first, always" is
about panel capabilities, and this is not one: it is the documentation *of* the
API, with no control-plane surface of its own. It adds no route to `cypherd`, no
table, no proto field and no work item.

## 2. What is published, and what is deliberately not

Published, under three URL prefixes chosen by the reader's question rather than
by the repository's layout — "how do I", "why is it like this", "what is this
word":

| Source | Prefix | Notes |
|---|---|---|
| `docs/features/*.md` | `/guides/` | Every feature spec, filed into the nine groups canvas 19a names |
| `docs/adrs/*.md` | `/decisions/` | Read from the directory, never listed by hand |
| `docs/architecture.md`, `glossary.md`, `vision.md`, `tech-stack.md`, `project-structure.md`, `security/threat-model.md` | `/reference/` | The six documents that explain the system rather than a feature of it |
| `docs/dev/deployment.md`, `docs/dev/release-signing.md` | `/reference/` | The two `dev/` documents that describe something an operator DOES |

`dev/` splits deliberately. Installing the panel and verifying a release you
downloaded are operator acts, and the design's own nav names both ("Install the
panel", "Self-host"). How CI is wired, how the review bot decides, and a
1,700-line machine-generated import report are not.

Not published: `docs/roadmap.md`, `docs/product/*`, and the rest of `docs/dev/`.
An operator reading "how do I restore a backup" is not served by a document that
argues about which screens are still unbuilt, and publishing a roadmap invites
reading it as a promise. They stay in the repository, where the people they are
written for already are.

The **exclusion is enforced, not merely intended**: the generator walks the
whole tree and every file it finds must be either placed in the nav or named in
the exclusion list. A new spec that nobody filed fails the build rather than
silently vanishing from the site — which is the failure this feature would
otherwise have, and it would be invisible.

## 3. The nav is editorial, so it is written down

`19a` groups the contents in a way no directory listing produces — "Getting
started" pulls `first-run-setup` next to `managed-databases`, and "Automate"
pulls `api-tokens` next to `outbound-webhooks`. That ordering is a judgement
about what a reader needs first, so it lives in one ordered map in `nav.go`
rather than being inferred.

Two invariants hold it honest, both asserted by tests:

- **Completeness.** Every publishable file appears in exactly one group. A
  duplicate is as much a failure as an omission — a page reachable from two
  places has two URLs and neither is canonical.
- **Truthfulness of the counts.** `19a` prints "47 features, 12 decisions, 6
  references" on the page. Those numbers are computed from what was actually
  generated, never typed, so the contents block cannot claim a number the site
  does not have — and the printed figures move when the tree does rather than
  going quietly stale.

## 4. URLs, and why they are directories

```
/                          docs home (19a)
/guides/<slug>/            one feature spec        e.g. /guides/routing-and-tls/
/decisions/<slug>/         one ADR                 e.g. /decisions/adr-005-desired-state-reconciliation/
/reference/<slug>/         one reference document  e.g. /reference/glossary/
/api/                      API index
/api/<tag>/<operation>/    one endpoint (19c)      e.g. /api/applications/deployapplication/
/search-index.json         the search corpus (19d)
```

Directory URLs with an `index.html` inside, so the address bar carries no
`.html` and a link written today survives the site moving between hosts. Every
page is reachable without JavaScript; search is the only thing that needs it,
and its absence costs the reader a keyboard shortcut, not a page.

## 5. Links between documents must survive the move

The specs link to each other constantly, in repository-relative form:
`[routing-and-tls.md](routing-and-tls.md)`, `[ADR-005](../adrs/ADR-005-…md)`,
`[the threat model](../security/threat-model.md#59-desired-state-gc)`. On the
site those paths mean nothing.

So every link is rewritten to its published URL, anchors preserved. A link to a
document that is **not** published — a spec pointing at `roadmap.md` — is
rewritten to the file on GitHub rather than left dangling: the reader still gets
where they were being sent, and the site does not pretend the roadmap is part of
it.

A link to a path that does not exist at all is a **build failure**, listed with
the file and line. This is the part of the feature that pays for itself: 78
cross-linked documents accumulate broken links silently, and until now nothing
checked them.

## 6. The API reference is generated, one endpoint per page

`core/api/rest/openapi.yaml` is the source of truth for the HTTP surface
(ENGINEERING rule 19), so the API pages are read from it and never written by
hand. Per `19c`, each endpoint page carries:

- the method chip in its own colour, the path, and the **scope** the route needs;
- the summary as a heading and the description as prose;
- a body table — field, type, notes — from the request schema;
- an error list from the declared responses;
- a dark request/response rail, which is the same ink surface the deploy drawer
  uses (`1c`), carrying a `curl` example and a sample response.

The `curl` example is **assembled from the operation**, not stored: method, the
path with its parameters filled by their own names, the bearer header, and a
body built from the request schema's example or its field names. A hand-written
example is a lie with a shelf life.

The rail's response body is the response schema's `example` where the spec has
one and a shape derived from the schema where it does not — and where neither is
possible it is omitted rather than invented, because a made-up response is worse
than no response for exactly the reader who copies it.

## 7. Search is an index, not a service

`19d` puts prose and endpoints in one list, opened with `/` from any page. It is
a static site, so search is a prebuilt `search-index.json` and about eighty lines
of vanilla JavaScript: title, section, URL, and the first ~30 words of each page,
plus every endpoint's method and path. Ranking is prefix-and-substring with title
matches first — enough for a corpus of a few hundred entries, and honest about
being that rather than pretending to be a relevance engine.

No search service, no build-time bundler, and no npm dependency: the site's whole
client-side surface is one CSS file and one JS file, both embedded in the
generator with `go:embed`.

## 8. It looks like the panel because it uses the panel's tokens

The site's stylesheet declares the same token names and the same values as
`web/src/styles/globals.css` — `--bg: #faf8f4`, `--text: #16130e`, `--accent:
#e8490f`, the same two faces (Instrument Sans, Fragment Mono), the same three
border weights. Dark maps mechanically through the same names, exactly as the
panel's does, and the toggle remembers the choice in `localStorage` while
`prefers-color-scheme` decides the first visit.

The one deliberate difference is measure. The panel is a dense operations
surface; this is prose, so the article column is **64ch** and gets a third rail
for on-this-page, which is what `19b` draws.

The values are duplicated rather than imported, and that is a real cost worth
naming: the panel's tokens live in a Tailwind stylesheet that a static Go
generator cannot read without pulling in the whole web build. A test asserts the
shared values agree, so a drift is a failing test rather than a slow divergence
nobody sees.

## 9. Where it lives and how it is built

```
core/cmd/docs-site/        the generator (a build-time tool, beside
                           coolify-import and release-sign)
make docs-site             writes dist/docs/
```

`github.com/yuin/goldmark` is added to the `core` module for CommonMark plus
tables, strikethrough and heading anchors. It is pure Go with no transitive
dependencies, and — this is the part that matters against vision.md's footprint
budget — **`cypherd` does not import it**, so the shipped binary is byte-identical
either way. The dependency is a build-time cost only, exactly as the Coolify
importer's is.

Output goes to `dist/docs/`, which is gitignored: generated bytes are not
committed, and the site is published from a build rather than from the tree.

## 10. Deliberately out of scope

- **Serving it from `cypherd`.** The panel is authenticated and the docs are
  public; putting them behind the login is the opposite of a documentation site,
  and putting an unauthenticated static handler in the control plane widens its
  surface for no benefit the reader can feel. The in-panel API reference
  ([in-panel-api-reference.md](in-panel-api-reference.md)) already covers the
  case of "I am signed in and want the spec".
- **Versioned docs (`v1.1 ▾` in the header).** The switcher is drawn because the
  design draws it, and it names the current version only. Real version switching
  means building and hosting N sites and deciding what happens to a page that did
  not exist in v1.0 — a hosting decision, not a rendering one.
- **"Was this page useful?" telemetry.** `19b` draws the control. It is rendered
  as a `mailto:`-free link to a GitHub issue prefilled with the page, because the
  alternative is an analytics endpoint on a static site, which is a backend with
  a cookie banner attached.
- **Edit this page.** Kept — it is a link to the file on GitHub, which costs
  nothing and is the single most effective docs contribution path.
- **The `Changelog` nav item.** `CHANGELOG.md` is already embedded in the binary
  and rendered in-panel (panel-updates.md §9). The site links to the repository's
  copy rather than rendering a second one.
- **Prose written for the site.** Every page is a document that already exists.
  Where the design names a page this repository has no document for — "Install
  the panel", "CI recipes" — the nav omits it rather than inventing a stub, which
  is rule 7's first clause.
