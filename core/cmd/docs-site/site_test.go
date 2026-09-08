package main

// These tests build the REAL site — every document in docs/, every endpoint in
// the OpenAPI spec — and assert against it. A generator tested against a
// fixture proves the fixture renders; the failures this feature actually has
// are a document nobody filed, a link that points at nothing, and a count on
// the home page that stopped being true, and none of those are visible except
// against the real tree.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	docsDir     = "../../../docs"
	openapiPath = "../../api/rest/openapi.yaml"
	tokensCSS   = "../../../web/src/styles/globals.css"
)

func buildSite(t *testing.T) *builder {
	t.Helper()
	b, err := build(docsDir, openapiPath, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return b
}

// The invariant that keeps the site honest: every publishable document is in
// exactly one nav group. A spec added without being filed fails HERE rather
// than silently vanishing from the site, which is the failure this feature
// would otherwise have — and it would be invisible.
func TestEveryDocumentIsEitherPublishedOrDeliberatelyExcluded(t *testing.T) {
	claimed, err := navIndex()
	if err != nil {
		t.Fatalf("navIndex: %v", err)
	}
	known, err := discover(docsDir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := checkComplete(known, claimed); err != nil {
		t.Fatal(err)
	}
}

// A duplicate is as much a failure as an omission: a page reachable from two
// groups has two sidebar homes and neither is canonical.
func TestNoDocumentIsClaimedByTwoGroups(t *testing.T) {
	seen := map[string]string{}
	for _, g := range nav {
		for _, p := range g.Paths {
			if prev, dup := seen[p]; dup {
				t.Errorf("%s is in both %q and %q", p, prev, g.Title)
			}
			seen[p] = g.Title
		}
	}
	if got, want := len(navPaths()), len(seen); got != want {
		t.Errorf("navPaths returned %d entries for %d documents", got, want)
	}
}

// Canvas 19a prints the contents counts on the page. They are computed, never
// typed — this asserts they describe the site that was actually generated.
func TestTheContentsCountsDescribeTheSiteThatWasGenerated(t *testing.T) {
	b := buildSite(t)
	var guides, decisions, references int
	for _, p := range b.pages {
		switch p.Section {
		case sectionGuides:
			guides++
		case sectionDecisions:
			decisions++
		default:
			references++
		}
	}
	c := b.site.Counts
	if c.Guides != guides || c.Decisions != decisions || c.References != references {
		t.Fatalf("counts %+v do not match the pages built (%d/%d/%d)", c, guides, decisions, references)
	}
	if c.Endpoints != len(b.endpoints) {
		t.Fatalf("endpoint count %d, built %d", c.Endpoints, len(b.endpoints))
	}
	if !strings.Contains(c.Sentence(), "12 decisions") {
		t.Fatalf("the ADR count is not in the sentence: %q", c.Sentence())
	}
	// Every ADR in the tree is on the home page: they are read from the
	// directory rather than typed, so a thirteenth appears without an edit.
	adrs, err := filepath.Glob(filepath.Join(docsDir, "adrs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.site.ADRs) != len(adrs) {
		t.Fatalf("the home page lists %d ADRs, the tree has %d", len(b.site.ADRs), len(adrs))
	}
}

// Every page is reachable, and every page reaches something real. This walks
// the GENERATED site rather than the markdown, so it also covers the links the
// chrome writes itself — the top bar, the home cards, prev/next.
func TestNoGeneratedPageLinksToAPageThatDoesNotExist(t *testing.T) {
	b := buildSite(t)
	href := regexp.MustCompile(`href="(/[^"]*)"`)

	var missing []string
	for from, body := range b.files {
		if !strings.HasSuffix(from, ".html") {
			continue
		}
		for _, m := range href.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if i := strings.IndexAny(target, "#?"); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			want := strings.TrimPrefix(target, "/")
			if strings.HasSuffix(want, "/") || want == "" {
				want += "index.html"
			}
			if _, ok := b.files[want]; !ok {
				missing = append(missing, from+" → "+m[1])
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d internal link(s) point at a page that was not generated:\n  %s",
			len(missing), strings.Join(missing[:min(len(missing), 20)], "\n  "))
	}
}

// A document that is not published must still take the reader somewhere. The
// roadmap is deliberately off the site, and a spec that links to it has to
// reach it on GitHub rather than 404.
func TestALinkToAnUnpublishedDocumentGoesToTheRepositoryRatherThanNowhere(t *testing.T) {
	known, err := discover(docsDir)
	if err != nil {
		t.Fatal(err)
	}
	published := map[string]string{"features/audit-log.md": "/guides/audit-log/"}
	r := newRenderer(docsDir, published, known)

	if got := r.rewrite("features/audit-log.md", "audit-log.md"); got != "/guides/audit-log/" {
		t.Fatalf("published link = %q", got)
	}
	if got := r.rewrite("features/audit-log.md", "../roadmap.md"); !strings.HasPrefix(got, repoBlobURL) {
		t.Fatalf("unpublished link = %q, want the repository", got)
	}
	if got := r.rewrite("features/audit-log.md", "../../ENGINEERING.md"); !strings.HasPrefix(got, repoBaseURL) {
		t.Fatalf("out-of-tree link = %q, want the repository", got)
	}
	if got := r.rewrite("features/audit-log.md", "https://example.com/x"); got != "https://example.com/x" {
		t.Fatalf("absolute link was rewritten to %q", got)
	}
	if len(r.broken) != 0 {
		t.Fatalf("reported %v as broken", r.broken)
	}
	// And a path that exists nowhere is a build failure, not a silent 404.
	if got := r.rewrite("features/audit-log.md", "no-such-document.md"); got != "no-such-document.md" {
		t.Fatalf("a dangling link was rewritten to %q", got)
	}
	if len(r.broken) != 1 {
		t.Fatalf("a dangling link was not reported: %v", r.broken)
	}
}

// The API reference is generated from the spec, so the page count and the spec
// have to agree — and every endpoint needs the three things canvas 19c draws.
func TestEveryEndpointInTheSpecGetsAPageWithARequestExample(t *testing.T) {
	b := buildSite(t)
	if len(b.endpoints) == 0 {
		t.Fatal("no endpoints were read from the spec")
	}
	seen := map[string]string{}
	for _, e := range b.endpoints {
		if e.Method == "" || e.Path == "" {
			t.Fatalf("endpoint with no method or path: %+v", e)
		}
		if prev, dup := seen[e.URL]; dup {
			t.Fatalf("%s and %s %s share the URL %s", prev, e.Method, e.Path, e.URL)
		}
		seen[e.URL] = e.Method + " " + e.Path
		if !strings.Contains(e.Curl, "curl -X "+e.Method) {
			t.Fatalf("%s %s has no request example: %q", e.Method, e.Path, e.Curl)
		}
		if !strings.Contains(e.Curl, e.Path) {
			t.Fatalf("%s %s's example does not call its own path: %q", e.Method, e.Path, e.Curl)
		}
		page, ok := b.files[strings.TrimPrefix(e.URL, "/")+"index.html"]
		if !ok {
			t.Fatalf("%s %s has no page at %s", e.Method, e.Path, e.URL)
		}
		if !strings.Contains(string(page), e.Path) {
			t.Fatalf("%s's page does not show its own path", e.URL)
		}
	}
}

// A made-up response is worse than no response for exactly the reader who
// copies it (documentation-site.md §6): a sample is rendered only where the
// spec declares a JSON body to derive it from.
func TestAResponseSampleIsOnlyShownWhereTheSpecDeclaresOne(t *testing.T) {
	b := buildSite(t)
	withSample := 0
	for _, e := range b.endpoints {
		if e.Sample == "" {
			continue
		}
		withSample++
		if !strings.HasPrefix(e.Sample, "{") && !strings.HasPrefix(e.Sample, "[") && !strings.HasPrefix(e.Sample, `"`) {
			t.Fatalf("%s %s sample is not JSON-shaped: %q", e.Method, e.Path, e.Sample)
		}
		var into any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(e.Sample, "…", `"…"`)), &into); err != nil {
			// The elision marker makes a truncated object invalid JSON on
			// purpose — it is a glance, not a fixture. Anything else is a bug.
			if !strings.Contains(e.Sample, "…") {
				t.Fatalf("%s %s sample is not valid JSON: %v\n%s", e.Method, e.Path, err, e.Sample)
			}
		}
	}
	if withSample == 0 {
		t.Fatal("no endpoint got a response sample; the deriving path is dead code")
	}
}

// Prose and endpoints in one list (canvas 19d), and every row has somewhere to
// go — a search result that 404s is worse than no search.
func TestTheSearchIndexCoversProseAndEndpointsAndEveryRowResolves(t *testing.T) {
	b := buildSite(t)
	raw, ok := b.files["search-index.json"]
	if !ok {
		t.Fatal("no search index was written")
	}
	var entries []searchEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("the search index is not valid JSON: %v", err)
	}
	if want := len(b.pages) + len(b.endpoints); len(entries) != want {
		t.Fatalf("the index has %d entries for %d pages and %d endpoints", len(entries), len(b.pages), len(b.endpoints))
	}
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.Kind]++
		if e.Title == "" {
			t.Fatalf("an index entry has no title: %+v", e)
		}
		target := strings.TrimPrefix(e.URL, "/") + "index.html"
		if _, ok := b.files[target]; !ok {
			t.Fatalf("index entry %q points at %s, which was not generated", e.Title, e.URL)
		}
	}
	for _, k := range []string{"guide", "decision", "reference", "api"} {
		if kinds[k] == 0 {
			t.Fatalf("the index has no %s entries: %v", k, kinds)
		}
	}
}

// The site and the panel must not drift into two products. The docs stylesheet
// declares the panel's own token names and values (documentation-site.md §8);
// this reads both files and refuses a disagreement.
func TestTheDocsPaletteAgreesWithThePanels(t *testing.T) {
	panel, err := os.ReadFile(tokensCSS)
	if err != nil {
		t.Skipf("the panel stylesheet is not here: %v", err)
	}
	docs, err := assets.ReadFile("assets/docs.css")
	if err != nil {
		t.Fatal(err)
	}
	// The light block only: the panel's dark values live under `.dark` and the
	// docs' under a media query, and comparing across two different selectors
	// would compare the wrong pairs.
	panelTokens := tokensIn(lightBlock(string(panel), ":root {", "}\n\n.dark"))
	docsTokens := tokensIn(lightBlock(string(docs), ":root {", "}\n\n/* Dark maps"))
	if len(panelTokens) == 0 || len(docsTokens) == 0 {
		t.Fatalf("could not read the token blocks (panel %d, docs %d)", len(panelTokens), len(docsTokens))
	}
	shared := 0
	for name, docsValue := range docsTokens {
		panelValue, ok := panelTokens[name]
		if !ok {
			continue // a token the docs site needs and the panel has no use for
		}
		if exemptToken[name] {
			continue
		}
		shared++
		if panelValue != docsValue {
			t.Errorf("%s is %s in the panel and %s in the docs", name, panelValue, docsValue)
		}
	}
	if shared < 20 {
		t.Fatalf("only %d tokens were compared; the parser is not finding them", shared)
	}
}

// exemptToken names the values that legitimately differ, with the reason. An
// exemption list a reader can audit beats a fudged value that makes the test
// pass and the palettes silently diverge.
//
// The two font stacks differ because the font is DELIVERED differently: the
// panel bundles the variable face through npm, where the family is registered
// as "Instrument Sans Variable"; the static site loads the same family from
// Google Fonts, where it is "Instrument Sans". Same typeface, two names.
var exemptToken = map[string]bool{
	"--font-sans": true,
	"--font-mono": true,
}

func lightBlock(css, start, end string) string {
	i := strings.Index(css, start)
	if i < 0 {
		return ""
	}
	rest := css[i+len(start):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

var tokenLine = regexp.MustCompile(`(?m)^\s*(--[a-z-]+):\s*([^;]+);`)

func tokensIn(block string) map[string]string {
	out := map[string]string{}
	for _, m := range tokenLine.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

// Every page carries the chrome canvas 19a-19c draws, and the shell is what
// guarantees it — so this checks one page of each shape rather than all 336.
func TestEveryPageShapeCarriesTheChrome(t *testing.T) {
	b := buildSite(t)
	for _, path := range []string{
		"index.html",
		"guides/routing-and-tls/index.html",
		"decisions/adr-005-desired-state-reconciliation/index.html",
		"api/index.html",
	} {
		body, ok := b.files[path]
		if !ok {
			t.Fatalf("%s was not generated", path)
		}
		s := string(body)
		for _, want := range []string{
			`<!doctype html>`,
			`data-search-open`,  // "/" opens search from every page (19d)
			`data-theme-toggle`, // dark maps through the same tokens
			`href="/docs.css"`,
			`src="/search.js"`,
			`<title>`,
			`name="description"`,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("%s is missing %s", path, want)
			}
		}
	}
}

// An article's on-this-page rail is the one thing that cannot be faked from the
// nav: it comes from the document's own headings.
func TestAnArticleGetsItsOwnHeadingsAndItsNeighbours(t *testing.T) {
	b := buildSite(t)
	p, ok := b.byPath["features/routing-and-tls.md"]
	if !ok {
		t.Fatal("routing-and-tls was not rendered")
	}
	if len(p.Headings) < 3 {
		t.Fatalf("only %d headings were collected", len(p.Headings))
	}
	for _, h := range p.Headings {
		if h.ID == "" || h.Text == "" {
			t.Fatalf("a heading has no id or text: %+v", h)
		}
		if !strings.Contains(p.HTML, `id="`+h.ID+`"`) {
			t.Fatalf("the rail links to #%s, which the body does not define", h.ID)
		}
	}
	body := string(b.files["guides/routing-and-tls/index.html"])
	if !strings.Contains(body, `class="prevnext"`) {
		t.Fatal("the article has no prev/next")
	}
	if !strings.Contains(body, "PREVIOUS") && !strings.Contains(body, "NEXT") {
		t.Fatal("the article's prev/next is empty in the middle of the reading order")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
