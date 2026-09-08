// Command docs-site generates the public documentation site
// (docs/features/documentation-site.md, canvas turn 19) from this repository's
// own docs/ tree and core/api/rest/openapi.yaml.
//
// It is a build-time tool, beside coolify-import and release-sign. cypherd does
// not import it, so the goldmark dependency it needs costs the shipped binary
// nothing — the point vision.md's footprint budget cares about.
//
//	go run ./cmd/docs-site -docs ../docs -openapi api/rest/openapi.yaml -out ../dist/docs
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	docsDir := flag.String("docs", "../docs", "the repository's docs/ directory")
	openapi := flag.String("openapi", "api/rest/openapi.yaml", "the OpenAPI document to generate the API reference from")
	out := flag.String("out", "../dist/docs", "output directory (emptied and rewritten)")
	version := flag.String("version", "", "the version this site documents; defaults to the OpenAPI info.version")
	flag.Parse()

	b, err := build(*docsDir, *openapi, *version)
	if err != nil {
		return err
	}
	if err := b.write(*out); err != nil {
		return err
	}
	fmt.Printf("docs-site: %d guides, %d decisions, %d references, %d endpoints → %s\n",
		b.site.Counts.Guides, b.site.Counts.Decisions, b.site.Counts.References,
		b.site.Counts.Endpoints, *out)
	return nil
}

// builder holds one generation. Split from run() so the tests can build the
// real site — every document, every endpoint — and assert against it.
type builder struct {
	site      *site
	pages     []page
	byPath    map[string]*page
	endpoints []endpoint
	// openapi is the spec's own bytes, published alongside the pages generated
	// from it.
	openapi []byte
	files   map[string][]byte
}

func build(docsDir, openapiPath, version string) (*builder, error) {
	claimed, err := navIndex()
	if err != nil {
		return nil, err
	}
	known, err := discover(docsDir)
	if err != nil {
		return nil, err
	}
	if err := checkComplete(known, claimed); err != nil {
		return nil, err
	}

	// The ADRs are read from the directory rather than typed: they are numbered,
	// they sort correctly, and a thirteenth must appear without anyone
	// remembering to add it (nav.go).
	var adrPaths []string
	for rel := range known {
		if strings.HasPrefix(rel, adrDir+"/") {
			adrPaths = append(adrPaths, rel)
		}
	}
	sort.Strings(adrPaths)

	published := map[string]string{}
	for rel := range claimed {
		published[rel] = urlOf(rel)
	}
	for _, rel := range adrPaths {
		published[rel] = urlOf(rel)
	}

	r := newRenderer(docsDir, published, known)
	b := &builder{site: &site{}, byPath: map[string]*page{}, files: map[string][]byte{}}

	for _, g := range nav {
		section := navSection{Title: g.Title}
		for _, rel := range g.Paths {
			p, err := r.render(rel, g.Title)
			if err != nil {
				return nil, err
			}
			b.pages = append(b.pages, p)
			section.Items = append(section.Items, navItem{Title: p.Title, URL: p.URL, Summary: p.Summary})
		}
		b.site.Nav = append(b.site.Nav, section)
	}
	for _, rel := range adrPaths {
		p, err := r.render(rel, "Design decisions")
		if err != nil {
			return nil, err
		}
		b.pages = append(b.pages, p)
		b.site.ADRs = append(b.site.ADRs, p)
	}
	for i := range b.pages {
		b.byPath[b.pages[i].Rel] = &b.pages[i]
	}

	// A broken link is a BUILD FAILURE, listed with the file that carries it.
	// 78 cross-linked documents accumulate broken links silently, and until now
	// nothing checked them (documentation-site.md §5).
	if len(r.broken) > 0 {
		var lines []string
		for _, bl := range r.broken {
			lines = append(lines, fmt.Sprintf("  %s → %s", bl.From, bl.Href))
		}
		sort.Strings(lines)
		return nil, fmt.Errorf("docs-site: %d link(s) point at nothing:\n%s", len(r.broken), strings.Join(lines, "\n"))
	}

	specVersion, endpoints, err := loadAPI(openapiPath)
	if err != nil {
		return nil, err
	}
	b.endpoints = endpoints
	if raw, rerr := os.ReadFile(openapiPath); rerr == nil { //nolint:gosec // a build-time path from a flag
		b.openapi = raw
	}
	b.site.APINav = groupEndpoints(endpoints)

	// The site documents the PRODUCT, so its version is the changelog's newest
	// release rather than the OpenAPI document's own `info.version` — those are
	// two different numbers and only one of them is what a reader means by
	// "which version is this". The spec version is the fallback.
	b.site.Version = version
	if b.site.Version == "" {
		b.site.Version = productVersion(filepath.Join(filepath.Dir(docsDir), "CHANGELOG.md"))
	}
	if b.site.Version == "" {
		b.site.Version = "v" + strings.TrimPrefix(specVersion, "v")
	}
	b.site.Counts = b.count()
	b.site.SelfHostURL = urlOf("dev/deployment.md")
	b.site.QuickstartURL = urlOf("features/first-run-setup.md")
	b.site.MigrateURL = urlOf("features/project-export.md")
	b.site.ProductionURL = urlOf("features/plane-disaster-recovery.md")

	if err := b.renderAll(); err != nil {
		return nil, err
	}
	return b, nil
}

// productVersion is the newest release the changelog names — the file the panel
// already embeds and renders in-panel (panel-updates.md §9), so there is one
// home for the answer rather than two.
func productVersion(changelog string) string {
	raw, err := os.ReadFile(changelog) //nolint:gosec // a path derived from the docs dir
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(line, "## ")
		if !ok {
			continue
		}
		version, _, _ := strings.Cut(rest, " ")
		version = strings.TrimSpace(version)
		if strings.HasPrefix(version, "v") {
			return version
		}
	}
	return ""
}

// discover walks docs/ and returns every markdown file it may publish, keyed by
// its docs-relative path. Excluded files are still returned as KNOWN — a link to
// the roadmap has to resolve to the roadmap on GitHub rather than to nothing.
func discover(docsDir string) (map[string]bool, error) {
	known := map[string]bool{}
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(docsDir, path)
		if err != nil {
			return err
		}
		known[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("docs-site: walking %s: %w", docsDir, err)
	}
	return known, nil
}

// checkComplete is the invariant that keeps the site honest: every file in the
// tree is either placed in the nav or named in `excluded`. A new spec that
// nobody filed fails the build rather than silently vanishing from the site,
// which is the failure this feature would otherwise have — and it would be
// invisible (nav.go).
func checkComplete(known map[string]bool, claimed map[string]string) error {
	var orphans []string
	for rel := range known {
		if strings.HasPrefix(rel, adrDir+"/") || isExcluded(rel) {
			continue
		}
		if _, ok := claimed[rel]; !ok {
			orphans = append(orphans, rel)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		return fmt.Errorf("docs-site: %d document(s) are in neither the nav nor the exclusion list:\n  %s\n"+
			"add each to a group in nav.go, or to `excluded` if it is a working note",
			len(orphans), strings.Join(orphans, "\n  "))
	}
	var missing []string
	for rel := range claimed {
		if !known[rel] {
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("docs-site: the nav names %d document(s) that do not exist:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	return nil
}

func (b *builder) count() counts {
	var c counts
	for _, p := range b.pages {
		switch p.Section {
		case sectionGuides:
			c.Guides++
		case sectionDecisions:
			c.Decisions++
		default:
			c.References++
		}
	}
	c.Endpoints = len(b.endpoints)
	return c
}

// groupEndpoints turns the flat endpoint list into the sidebar canvas 19c
// draws: one block per OpenAPI tag, in the order the tags first appear, which
// is the order the spec declares its paths in.
func groupEndpoints(eps []endpoint) []apiSection {
	var order []string
	byTag := map[string][]apiNavItem{}
	for _, e := range eps {
		if _, seen := byTag[e.Tag]; !seen {
			order = append(order, e.Tag)
		}
		byTag[e.Tag] = append(byTag[e.Tag], apiNavItem{Method: e.Method, Path: e.Path, URL: e.URL})
	}
	out := make([]apiSection, 0, len(order))
	for _, tag := range order {
		out = append(out, apiSection{Title: strings.ToUpper(tag), Items: byTag[tag]})
	}
	return out
}

const (
	editBase     = "https://github.com/MaramHarsha/CypherPanel/edit/main/docs/"
	feedbackBase = "https://github.com/MaramHarsha/CypherPanel/issues/new?labels=docs"
)

func (b *builder) renderAll() error {
	// The home (19a).
	home, err := render("home", &view{
		Site: b.site, Tab: "guides", Title: "CypherPanel",
		Description: "Self-hosted deployment for your own servers: one binary, one database, and an agent that dials home.",
		EditURL:     "https://github.com/MaramHarsha/CypherPanel/tree/main/docs",
		FeedbackURL: feedbackBase,
	})
	if err != nil {
		return err
	}
	b.files["index.html"] = home

	// Every document (19b). Prev/next follow the nav's own reading order, which
	// is what the article footer promises.
	flat := b.readingOrder()
	for i := range flat {
		p := flat[i]
		v := &view{
			Site: b.site, Tab: "guides", Page: p,
			Title: p.Title, Description: p.Summary,
			EditURL:     editBase + p.Rel,
			FeedbackURL: feedbackBase,
			ReadMinutes: readMinutes(p.HTML),
		}
		if strings.HasSuffix(p.Rel, "dev/deployment.md") {
			v.Tab = "selfhost"
		}
		if i > 0 {
			v.Prev = &navItem{Title: flat[i-1].Title, URL: flat[i-1].URL}
		}
		if i+1 < len(flat) {
			v.Next = &navItem{Title: flat[i+1].Title, URL: flat[i+1].URL}
		}
		b.markActive(v.Site, p.URL)
		out, err := render("article", v)
		if err != nil {
			return err
		}
		b.files[strings.TrimPrefix(p.URL, "/")+"index.html"] = out
	}
	b.markActive(b.site, "")

	// The API index and one page per endpoint (19c).
	idx, err := render("apiindex", &view{
		Site: b.site, Tab: "api", Title: "API reference",
		Description: fmt.Sprintf("Every route the panel serves — %d endpoints, generated from the OpenAPI document the binary itself serves.", len(b.endpoints)),
		EditURL:     "https://github.com/MaramHarsha/CypherPanel/blob/main/core/api/rest/openapi.yaml",
		FeedbackURL: feedbackBase,
	})
	if err != nil {
		return err
	}
	b.files["api/index.html"] = idx

	for i := range b.endpoints {
		e := &b.endpoints[i]
		b.markAPIActive(e.URL)
		nav, others := b.apiRailFor(e.Tag)
		v := &view{
			Site: b.site, Tab: "api", Endpoint: e,
			EndpointNav:     nav,
			OtherTags:       others,
			Title:           e.Method + " " + e.Path,
			Description:     summarize(e.Description),
			EndpointTitle:   summaryTitle(*e),
			DescriptionHTML: paragraphs(e.Description),
			EditURL:         "https://github.com/MaramHarsha/CypherPanel/blob/main/core/api/rest/openapi.yaml",
			FeedbackURL:     feedbackBase,
		}
		out, err := render("endpoint", v)
		if err != nil {
			return err
		}
		b.files[strings.TrimPrefix(e.URL, "/")+"index.html"] = out
	}
	b.markAPIActive("")

	// Assets and the search corpus (19d).
	css, err := assets.ReadFile("assets/docs.css")
	if err != nil {
		return err
	}
	js, err := assets.ReadFile("assets/search.js")
	if err != nil {
		return err
	}
	b.files["docs.css"] = css
	b.files["search.js"] = js
	// The spec itself, beside the reference generated from it (canvas 19c's
	// "openapi.json ↓"). A reader who wants to generate a client should not have
	// to find the repository first.
	if b.openapi != nil {
		b.files["openapi.yaml"] = b.openapi
	}

	index, err := json.Marshal(b.searchIndex())
	if err != nil {
		return fmt.Errorf("docs-site: encoding the search index: %w", err)
	}
	b.files["search-index.json"] = index
	return nil
}

// readingOrder is the nav's order followed by the ADRs — what prev/next walks.
func (b *builder) readingOrder() []*page {
	out := make([]*page, 0, len(b.pages))
	for _, g := range nav {
		for _, rel := range g.Paths {
			out = append(out, b.byPath[rel])
		}
	}
	for i := range b.site.ADRs {
		out = append(out, b.byPath[b.site.ADRs[i].Rel])
	}
	return out
}

func (b *builder) markActive(s *site, url string) {
	for i := range s.Nav {
		for j := range s.Nav[i].Items {
			s.Nav[i].Items[j].Active = s.Nav[i].Items[j].URL == url
		}
	}
}

// apiRailFor scopes the endpoint sidebar to one tag, with the rest as counts.
// Canvas 19c draws it that way, and it is also what keeps 276 sidebar entries
// out of 276 pages — a full rail would have been most of the site's bytes.
func (b *builder) apiRailFor(tag string) ([]apiSection, []tagCount) {
	want := strings.ToUpper(tag)
	var mine []apiSection
	var others []tagCount
	for _, s := range b.site.APINav {
		if s.Title == want {
			mine = append(mine, s)
			continue
		}
		first := "/api/"
		if len(s.Items) > 0 {
			first = s.Items[0].URL
		}
		others = append(others, tagCount{Title: s.Title, Count: len(s.Items), URL: first})
	}
	return mine, others
}

func (b *builder) markAPIActive(url string) {
	for i := range b.site.APINav {
		for j := range b.site.APINav[i].Items {
			b.site.APINav[i].Items[j].Active = b.site.APINav[i].Items[j].URL == url
		}
	}
}

// searchEntry is one row of the corpus (19d): prose and endpoints in one list.
type searchEntry struct {
	Title   string `json:"t"`
	URL     string `json:"u"`
	Kind    string `json:"k"` // guide | decision | reference | api
	Section string `json:"s"`
	Body    string `json:"b"`
	Method  string `json:"m,omitempty"`
}

func (b *builder) searchIndex() []searchEntry {
	out := make([]searchEntry, 0, len(b.pages)+len(b.endpoints))
	for _, p := range b.pages {
		kind := "reference"
		switch p.Section {
		case sectionGuides:
			kind = "guide"
		case sectionDecisions:
			kind = "decision"
		}
		out = append(out, searchEntry{
			Title: p.Title, URL: p.URL, Kind: kind, Section: p.Group, Body: p.Summary,
		})
	}
	for _, e := range b.endpoints {
		out = append(out, searchEntry{
			Title: e.Path, URL: e.URL, Kind: "api", Section: strings.ToUpper(e.Tag),
			Body: summarize(e.Summary), Method: e.Method,
		})
	}
	return out
}

// write empties out and rewrites it. Emptying is deliberate: a stale page from
// a renamed document would otherwise stay on the site forever, still linked
// from nowhere and still indexed by search engines.
func (b *builder) write(out string) error {
	if err := os.RemoveAll(out); err != nil {
		return fmt.Errorf("docs-site: clearing %s: %w", out, err)
	}
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("docs-site: creating %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, b.files[p], 0o644); err != nil { //nolint:gosec // a public static site
			return fmt.Errorf("docs-site: writing %s: %w", full, err)
		}
	}
	return nil
}

// readMinutes at 220 words a minute, floored at one. Canvas 19b prints it, and
// a computed number is the only kind that stays true.
func readMinutes(html string) int {
	words := len(strings.Fields(stripTags(html)))
	m := words / 220
	if m < 1 {
		return 1
	}
	return m
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// paragraphs renders an OpenAPI description's blank-line-separated blocks as
// paragraphs. The spec writes long prose there, and a single <p> would run it
// all together — which is exactly what the in-panel reference had to fix too.
func paragraphs(s string) string {
	var b strings.Builder
	for _, block := range strings.Split(s, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		b.WriteString("<p>" + inlineCode(escape(oneLine(block))) + "</p>")
	}
	return b.String()
}

// inlineCode turns `backticked` spans into <code>, which is the only markdown
// the OpenAPI descriptions actually use.
func inlineCode(s string) string {
	parts := strings.Split(s, "`")
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + part + "</code>")
			continue
		}
		b.WriteString(part)
	}
	return b.String()
}
