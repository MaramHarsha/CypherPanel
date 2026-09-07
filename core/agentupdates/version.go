package agentupdates

// Version comparison, plane-side. It answers two questions and no others: is
// this newer than the panel, and does it go backwards from what the channel
// already names.
//
// A DEVELOPMENT BUILD CANNOT MAKE THE FIRST COMPARISON AND SO DOES NOT — it
// warns and allows. Only half of that is borrowed from the join command:
// core/updates.IsRelease is the shared judgement of what counts as a release,
// but the join command's dev-build behaviour is SILENCE, not a warning. The
// difference in the act warrants it: an operator pasting a join line is not
// choosing a version, and an operator setting one for the fleet is.

import (
	"strconv"
	"strings"
)

type semver struct {
	major, minor, patch int
	pre                 string
}

func parse(s string) (semver, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "v"))
	if s == "" {
		return semver{}, false
	}
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	out := semver{pre: pre}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
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

func cmp(a, b semver) int {
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

// newerThanPanel reports whether want is ahead of the panel's own build. A
// panel that is not a release cannot make the comparison, so it does not: the
// caller allows it and the screen warns.
func newerThanPanel(want, panel string) bool {
	w, wok := parse(want)
	p, pok := parse(panel)
	if !wok || !pok {
		return false
	}
	return cmp(w, p) > 0
}

// goesBackwards reports whether moving from `from` to `to` is a downgrade,
// which is what sets a channel's rollback flag.
func goesBackwards(from, to string) bool {
	f, fok := parse(from)
	t, tok := parse(to)
	if !fok || !tok {
		return false
	}
	return cmp(t, f) < 0
}
