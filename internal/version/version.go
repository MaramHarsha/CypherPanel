// Package version centralises CypherPanel's version identity and the
// Core↔Agent compatibility policy (plan.md §13). In a fleet, Core and Agents
// upgrade at different times, so mixed versions are normal; the proto contract's
// add-only rule keeps that safe. Core refuses an Agent older than MinAgent so a
// too-old node fails loudly at registration, not mysteriously mid-operation.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// Core is this control plane's version.
	Core = "0.1.0"
	// MinAgent is the oldest Agent version Core will accept at registration.
	MinAgent = "0.1.0"
)

// AgentCompatible reports whether an agent version may register. Development
// builds ("dev"/empty/unparseable) are allowed with ok=true and a reason for
// the caller to log; a well-formed version below MinAgent is refused.
func AgentCompatible(agentVersion string) (ok bool, reason string) {
	av := strings.TrimSpace(agentVersion)
	if av == "" || av == "dev" {
		return true, "development build"
	}
	a, aerr := parse(av)
	m, merr := parse(MinAgent)
	if aerr != nil || merr != nil {
		// Unparseable version: allow but flag, rather than lock a node out on a
		// formatting quirk.
		return true, "unrecognised version format"
	}
	if compare(a, m) < 0 {
		return false, fmt.Sprintf("agent version %s is older than the minimum supported %s", av, MinAgent)
	}
	return true, ""
}

type semver struct{ major, minor, patch int }

func parse(v string) (semver, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release/build suffix (1.2.3-rc1 -> 1.2.3).
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("version %q is not major.minor.patch", v)
	}
	var out semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, err
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
	return out, nil
}

func compare(a, b semver) int {
	switch {
	case a.major != b.major:
		return sign(a.major - b.major)
	case a.minor != b.minor:
		return sign(a.minor - b.minor)
	default:
		return sign(a.patch - b.patch)
	}
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	if n > 0 {
		return 1
	}
	return 0
}
