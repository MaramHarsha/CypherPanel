package sharedvars

import (
	"errors"
	"regexp"
	"strings"
)

// The reference grammar (shared-variables.md §3) and its two operations. This
// file touches no store and no secret: it is one grammar with two callers —
// core/applications validates and records references on write, core/scheduler
// expands them at spec-build — so the writer and the resolver cannot drift.

const (
	open     = "{{"
	closeSeq = "}}"

	// prefix is the only namespace. One namespace with shadowing (rather than
	// Coolify's {{project.K}}/{{environment.K}} pair) makes scope a property of
	// the VARIABLE, not of the reference: promoting a value from project to
	// environment scope needs no edit in any referencing application (§3).
	prefix = "shared."
)

// MaxReferences bounds how many distinct shared variables one value may
// reference (§6). An env var is a value, not a template engine.
const MaxReferences = 16

// ErrMalformedReference is returned for a "{{…}}" that is not a well-formed
// {{shared.KEY}} — `{{ shared.X }}`, `{{shared.}}`, `{{project.X}}`, an
// unterminated `{{`.
//
// The message deliberately does not echo the offending text: it is a fragment
// of a value that is about to be sealed, and error messages are one of the
// places secrets must never appear (ENGINEERING rule 20).
var ErrMalformedReference = errors.New(`value contains "{{…}}" that is not a well-formed {{shared.KEY}} reference`)

// ErrTooManyReferences is returned when a value exceeds MaxReferences.
var ErrTooManyReferences = errors.New("a value may reference at most 16 shared variables")

// MissingReferenceError names a well-formed reference that does not resolve.
// Only the KEY is carried: key names are already returned by
// GET /applications/{id}/env, so naming one leaks nothing, and naming it is the
// whole point — the failure has to say which variable is missing (§4).
type MissingReferenceError struct{ Key string }

func (e *MissingReferenceError) Error() string {
	return "{{shared." + e.Key + "}} is not defined in this scope"
}

// reference matches the inside of a well-formed {{shared.KEY}}: the same
// portable charset an application's own env keys use, with no inner whitespace
// and nothing else allowed.
var reference = regexp.MustCompile(`^shared\.([A-Za-z_][A-Za-z0-9_]*)$`)

// key matches a shared variable's own key.
var key = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidKey reports whether k is a usable shared-variable key (§2).
func ValidKey(k string) bool { return key.MatchString(k) }

// ContainsReference reports whether a value contains a "{{shared." sequence at
// all. It is the check a shared variable's OWN value must fail: values are
// stored verbatim, so nesting would need a recursion, a cycle check and an
// expansion order this slice deliberately does not have (§3).
func ContainsReference(value string) bool { return strings.Contains(value, open+prefix) }

// Refs returns the distinct shared keys a value references, in first-seen
// order, and rejects any malformed "{{…}}" outright. A value with no references
// returns nil and no error, which is the common case and costs one scan.
//
// Strictness here is the behaviour we pointedly do not port from Coolify, whose
// resolver skips a reference it does not recognise and ships the literal
// `{{project.FOO}}` into the container (§3).
func Refs(value string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for i := 0; ; {
		rel := strings.Index(value[i:], open)
		if rel < 0 {
			return out, nil
		}
		inner, next, err := scan(value, i+rel)
		if err != nil {
			return nil, err
		}
		if !seen[inner] {
			if len(out) == MaxReferences {
				return nil, ErrTooManyReferences
			}
			seen[inner] = true
			out = append(out, inner)
		}
		i = next
	}
}

// Expand substitutes every {{shared.KEY}} with its value from vals. Expansion
// is SUBSTRING-wise and may occur several times in one value — composing
// `postgres://{{shared.DB_USER}}:{{shared.DB_PASS}}@db:5432/app` out of shared
// parts is the main reason operators want this (§3).
//
// It is deliberately ONE LEVEL: the substituted text is written past the scan
// cursor and never re-examined, so a `{{shared.…}}` that happens to live inside
// a value expands to nothing and defines no order. An unresolved key is an
// error, never an empty string (§4).
func Expand(value string, vals map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; ; {
		rel := strings.Index(value[i:], open)
		if rel < 0 {
			b.WriteString(value[i:])
			return b.String(), nil
		}
		start := i + rel
		b.WriteString(value[i:start])
		inner, next, err := scan(value, start)
		if err != nil {
			return "", err
		}
		v, ok := vals[inner]
		if !ok {
			return "", &MissingReferenceError{Key: inner}
		}
		b.WriteString(v)
		i = next
	}
}

// scan reads one reference starting at the "{{" at index start, returning the
// referenced key and the index just past the closing "}}".
func scan(value string, start int) (refKey string, next int, err error) {
	rest := value[start+len(open):]
	end := strings.Index(rest, closeSeq)
	if end < 0 {
		return "", 0, ErrMalformedReference
	}
	m := reference.FindStringSubmatch(rest[:end])
	if m == nil {
		return "", 0, ErrMalformedReference
	}
	return m[1], start + len(open) + end + len(closeSeq), nil
}
