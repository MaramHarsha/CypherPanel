package upgrade

// Version comparison, shared by the pre-flight's agent floor and the rollback
// floor. Semver-ish and deliberately narrow: `v1.2.3` with an optional leading
// v and an optional pre-release suffix that is ignored for ordering, because a
// release either is or is not a tag this project cut.

import (
	"strconv"
	"strings"
)

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
