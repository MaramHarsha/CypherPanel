package updater

// Version comparison, agent-side (agent-updates.md §6).
//
// The agent refuses a version below its running one unless the channel's
// rollback flag is set (ADR-010 §3). Be precise about what that buys: it does
// NOT stop a compromised plane from pinning the fleet to a known-bad release,
// because a plane that sets the version sets the flag. It stops an ACCIDENT — a
// stale desired set, a mistyped tag, a restored database snapshot walking the
// fleet backwards. The bound against a hostile plane is the signature.
//
// core/updates has its own parser and this is deliberately a second one: go.work
// declares agent and core as separate modules, so there is nothing to import.

import (
	"regexp"
	"strconv"
	"strings"
)

// tagShape bounds what a version may LOOK like, which is a separate question
// from whether it is newer. The version arrives from the plane and is
// concatenated into a URL, so a tag of "../../evil" would aim this fetcher at
// an arbitrary path under the release host. The signature bounds what the agent
// will RUN; it says nothing about where the agent looks, and both bounds have
// to exist.
var tagShape = regexp.MustCompile(`^v?[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)

// validTag reports whether s is a release tag this agent will build a URL from.
func validTag(s string) bool { return len(s) <= 48 && tagShape.MatchString(s) }

type version struct {
	major, minor, patch int
	pre                 string
}

// parseVersion accepts "v1.2.3" and "1.2.3", with an optional "-rc.1" suffix.
// ok is false for anything else — a development build included, which is what
// makes "compare against dev" impossible rather than wrong.
func parseVersion(s string) (version, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return version{}, false
	}
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	out := version{pre: pre}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		switch i {
		case 0:
			out.major = n
		case 1:
			out.minor = n
		case 2:
			out.patch = n
		}
	}
	return out, true
}

// compare returns -1, 0 or 1. A pre-release sorts BEFORE its own release, which
// is what makes v1.2.0-rc.1 → v1.2.0 an upgrade rather than a downgrade.
func compare(a, b version) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	case a.pre < b.pre:
		return -1
	default:
		return 1
	}
}

// isDowngrade reports whether moving from running to target goes backwards.
// An unparseable version on either side is NOT a downgrade: a development build
// has no place in this comparison, and refusing to move off one would make a
// dev agent permanently un-updatable.
func isDowngrade(running, target string) bool {
	r, rok := parseVersion(running)
	t, tok := parseVersion(target)
	if !rok || !tok {
		return false
	}
	return compare(t, r) < 0
}
