package main

// Markdown → a page (documentation-site.md §5).
//
// Three things happen here that a plain renderer does not do, and each is the
// reason this is a generator rather than a `find | pandoc`:
//
//  1. Intra-repository links are REWRITTEN to their published URLs. The specs
//     link to each other constantly in repository-relative form, and on the
//     site those paths mean nothing.
//  2. A link to a document that is not published is rewritten to the file on
//     GitHub rather than left dangling — the reader still gets where they were
//     being sent, and the site does not pretend the roadmap is part of it.
//  3. A link to a path that does not exist AT ALL is collected as a build
//     failure. 78 cross-linked documents accumulate broken links silently, and
//     until now nothing checked them.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	gmutil "github.com/yuin/goldmark/util"
)

// Where a link the site cannot serve is sent instead of nowhere.
const (
	repoBaseURL = "https://github.com/MaramHarsha/CypherPanel/blob/main/"
	repoBlobURL = repoBaseURL + "docs/"
)

// heading is one entry in the on-this-page rail (canvas 19b).
type heading struct {
	ID    string
	Text  string
	Level int
}

// page is one rendered document.
type page struct {
	// Rel is the docs-relative source ("features/routing-and-tls.md").
	Rel string
	// Group is the nav heading that claims it; Section is the URL prefix.
	Group   string
	Section string
	URL     string
	Title   string
	// Summary is the first paragraph, flattened — the contents blurb and the
	// search index both read it, so it is computed once.
	Summary string
	HTML    string
	// Updated is when the document last changed, as canvas 19b prints it
	// ("UPDATED AUG 2026"). Read from git rather than from the file's mtime: a
	// fresh clone rewrites every mtime to the moment it was cloned, which would
	// have every page claiming it was updated today.
	Updated  string
	Headings []heading
}

// brokenLink is one link that points at nothing, reported with enough to fix it.
type brokenLink struct {
	From string
	Href string
}

// renderer builds pages against a fixed set of known documents.
type renderer struct {
	docsDir string
	// repoRoot is docsDir's parent. A document may legitimately link OUT of
	// docs/ — into research/, into ENGINEERING.md — and those targets are real
	// files the site cannot serve, so they are sent to GitHub. Verified against
	// disk, so a link out of the tree is checked as strictly as a link inside it.
	repoRoot string
	// published maps a docs-relative path to its URL. A path present in the
	// tree but absent here is a document that exists and is not published.
	published map[string]string
	// known is every markdown file in the tree, published or not.
	known  map[string]bool
	md     goldmark.Markdown
	broken []brokenLink
}

func newRenderer(docsDir string, published map[string]string, known map[string]bool) *renderer {
	return &renderer{
		docsDir:   docsDir,
		repoRoot:  filepath.Dir(docsDir),
		published: published,
		known:     known,
		md: goldmark.New(
			// GFM for the tables the specs use heavily, plus strikethrough and
			// autolinks. AutoHeadingID is what the on-this-page rail links to.
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
			// Unsafe: the corpus is this repository's own documents, and a few
			// of them embed HTML deliberately. It is not user input.
			goldmark.WithRendererOptions(html.WithUnsafe()),
		),
	}
}

func (r *renderer) render(rel, group string) (page, error) {
	src, err := os.ReadFile(filepath.Join(r.docsDir, filepath.FromSlash(rel))) //nolint:gosec // paths come from the nav map, not from input
	if err != nil {
		return page{}, fmt.Errorf("docs-site: reading %s: %w", rel, err)
	}
	doc := r.md.Parser().Parse(text.NewReader(src))

	p := page{Rel: rel, Group: group, Section: sectionOf(rel), URL: urlOf(rel)}
	if err := r.walk(rel, src, doc, &p); err != nil {
		return page{}, err
	}

	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, src, doc); err != nil {
		return page{}, fmt.Errorf("docs-site: rendering %s: %w", rel, err)
	}
	p.HTML = buf.String()
	p.Updated = r.lastChanged(rel)
	if p.Title == "" {
		p.Title = slugOf(rel)
	}
	return p, nil
}

// lastChanged asks git when the document last changed. It returns "" outside a
// checkout — a tarball build still produces the site, just without the stamp,
// which is better than printing a date it made up.
func (r *renderer) lastChanged(rel string) string {
	cmd := exec.Command("git", "log", "-1", "--format=%cd", "--date=format:%b %Y", "--", rel) //nolint:gosec // rel comes from the nav map
	cmd.Dir = r.docsDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(string(out)))
}

// walk collects the title, the first paragraph and the on-this-page headings,
// and rewrites every link — one pass over the tree rather than three.
func (r *renderer) walk(rel string, src []byte, doc ast.Node, p *page) error {
	return ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Heading:
			txt := string(node.Text(src)) //nolint:staticcheck // Text is the documented way to flatten a heading
			if node.Level == 1 && p.Title == "" {
				p.Title = txt
				return ast.WalkContinue, nil
			}
			// Two levels in the rail and no more: three would make the rail
			// taller than the article it indexes (canvas 19b draws five items).
			if node.Level == 2 || node.Level == 3 {
				id, ok := node.AttributeString("id")
				if !ok {
					return ast.WalkContinue, nil
				}
				p.Headings = append(p.Headings, heading{
					ID: string(id.([]byte)), Text: txt, Level: node.Level,
				})
			}
		case *ast.Paragraph:
			if p.Summary == "" && p.Title != "" {
				p.Summary = summarize(string(node.Text(src))) //nolint:staticcheck // ditto
			}
		case *ast.Link:
			node.Destination = []byte(r.rewrite(rel, string(node.Destination)))
		}
		return ast.WalkContinue, nil
	})
}

// rewrite turns a repository-relative link into a site URL. Anything absolute
// is left exactly as written.
//
// Three outcomes, and the third is the one that pays for this function: a
// published document becomes a site URL; an unpublished-but-real file becomes a
// GitHub link, so the reader still gets where they were being sent; and a path
// that exists nowhere is collected as a build failure.
func (r *renderer) rewrite(from, href string) string {
	if href == "" || strings.HasPrefix(href, "#") ||
		strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
		return href
	}
	target, anchor, _ := strings.Cut(href, "#")
	if anchor != "" {
		anchor = "#" + anchor
	}
	if target == "" {
		return href
	}
	// Resolve against the linking document's own directory, which is what a
	// relative link in a markdown file means.
	resolved := path.Clean(path.Join(path.Dir(from), target))

	// A link that climbs out of docs/ — into research/, into ENGINEERING.md,
	// into the code. Real repository paths the site cannot serve.
	if strings.HasPrefix(resolved, "../") {
		repoRel := path.Clean(path.Join("docs", path.Dir(from), target))
		if strings.HasPrefix(repoRel, "../") {
			return href // climbs above the repository; not ours to judge
		}
		if r.repoHas(repoRel) {
			return repoBaseURL + repoRel + anchor
		}
		r.broken = append(r.broken, brokenLink{From: from, Href: href})
		return href
	}

	if !strings.HasSuffix(resolved, ".md") {
		// A directory or a non-document inside docs/ — link to it on GitHub
		// rather than to a page that does not exist.
		if r.repoHas(path.Join("docs", resolved)) {
			return repoBaseURL + "docs/" + resolved + anchor
		}
		r.broken = append(r.broken, brokenLink{From: from, Href: href})
		return href
	}
	if url, ok := r.published[resolved]; ok {
		return url + anchor
	}
	if r.known[resolved] {
		// Exists, deliberately not published: send the reader to the file.
		return repoBlobURL + resolved + anchor
	}
	r.broken = append(r.broken, brokenLink{From: from, Href: href})
	return href
}

// repoHas reports whether a repository-relative path is a real file or
// directory. The link checker is only worth having if it checks.
func (r *renderer) repoHas(repoRel string) bool {
	_, err := os.Stat(filepath.Join(r.repoRoot, filepath.FromSlash(repoRel)))
	return err == nil
}

// summarize flattens a paragraph to the first ~30 words. The contents blurb and
// the search index both read it, and both want one line rather than a page.
func summarize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	words := strings.Split(s, " ")
	const max = 30
	if len(words) <= max {
		return s
	}
	return strings.Join(words[:max], " ") + "…"
}

// escape is the HTML escaper the templates use for text the generator inserts
// itself. Page bodies are already HTML from goldmark and are not re-escaped.
func escape(s string) string { return string(gmutil.EscapeHTML([]byte(s))) }
