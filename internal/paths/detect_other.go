//go:build !linux

package paths

// detectFamily on non-Linux platforms (development machines) — CypherAgent
// only manages Linux servers, so there is nothing to detect here.
func detectFamily() Family {
	return FamilyUnknown
}
