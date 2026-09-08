package upgrade

// Version comparison, shared by the pre-flight's agent floor and the rollback
// floor. Semver-ish and deliberately narrow: `v1.2.3` with an optional leading
// v and an optional pre-release suffix that is ignored for ordering, because a
// release either is or is not a tag this project cut.

import (
	"regexp"
	"strconv"
	"strings"
)

// tagShape is what a release tag may be, and nothing else: an optional v, three
// dotted numbers, and an optional pre-release suffix of letters, digits, dots
// and dashes.
//
// This is a SECURITY boundary, not a tidiness one. A version reaches
// VerifyRelease from an API query parameter, and VerifyRelease interpolates it
// into a release URL — so without this a caller could put "../../" in it and
// aim the panel's own fetcher at an arbitrary path on the release host, or put
// a "?" in it and change the request's query. Bounding the SHAPE is what stops
// that, rather than trying to sanitise a string after it has become a URL.
var tagShape = regexp.MustCompile(`^v?[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)

// ValidTag reports whether s is a release tag this panel will build a URL from.
func ValidTag(s string) bool {
	return len(s) <= 48 && tagShape.MatchString(s)
}

// olderThan reports whether a is strictly older than b. An unparseable version
// on either side answers FALSE — never "older" — so a version string nobody
// recognises cannot be the reason an upgrade is refused or a rollback allowed.
func olderThan(a, b string) bool {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return false
}

// Older is olderThan, exported for the rollback floor check.
func Older(a, b string) bool { return olderThan(a, b) }

func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
