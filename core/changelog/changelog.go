// Package changelog is the in-panel "What's new" (panel-updates.md §9).
//
// EMBEDDED, NOT FETCHED. Fetching a list of releases would mean a second
// outbound call with its own rate limit, and it would render prose from a
// network source inside an operator-facing surface — the injection concern the
// threat model raises about a release's tag and notes URL, multiplied by a page
// of Markdown. Embedding costs a CHANGELOG.md that has to be maintained per
// release; it buys a changelog that works air-gapped and that cannot be written
// by anyone who compromises a feed.
//
// The AVAILABLE version's entry shows only what the signed release.json and the
// feed already give — version, kind, and a link pointing out — so no untrusted
// prose is ever rendered.
package changelog

import (
	_ "embed"
	"strings"
)

//go:embed CHANGELOG.md
var source string

// Entry is one release's section.
type Entry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Notes   []string `json:"notes"`
}

// Entries parses the embedded file, newest first — the order it is written in.
//
// The parser is deliberately narrow: a `## <version> — <date>` heading and `- `
// bullets, and ANYTHING ELSE IS SKIPPED rather than rendered. A changelog is
// operator-facing prose from this repository, but the renderer should still not
// be a Markdown engine inside a control plane.
func Entries() []Entry {
	var out []Entry
	var cur *Entry
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			version, date := splitHeading(strings.TrimPrefix(trimmed, "## "))
			if version == "" {
				cur = nil
				continue
			}
			out = append(out, Entry{Version: version, Date: date})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		cur.Notes = append(cur.Notes, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
	}
	return out
}

// splitHeading reads "v0.4.0 — 2026-09-07". Both an em dash and a hyphen are
// accepted, because a heading written by hand should not fail on punctuation.
func splitHeading(s string) (version, date string) {
	for _, sep := range []string{" — ", " - ", " – "} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
		}
	}
	v := strings.TrimSpace(s)
	if strings.ContainsAny(v, " \t") {
		return "", ""
	}
	return v, ""
}
