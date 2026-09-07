package metrics

// Path normalisation (metrics-and-usage.md §4.4).
//
// The TOP PATHS table is the part of this feature that can destroy a database,
// because /api/contacts/8f3ac1d0 is a distinct path for every contact. Two
// bounds apply and both are on the agent, because the node is where the cost
// is: normalisation here, and the hard cap in bucket.go.
//
// This is a HEURISTIC and the spec does not pretend otherwise: an application
// whose real route IS /v1/2024/report will see it rewritten. A router-aware
// breakdown would need the application to declare its routes, and nothing in a
// container image lets us ask for that.

import "strings"

// MaxSegments is how deep a normalised path goes. Truncation is the second
// half of the cardinality bound, and it also removes depth from paths that
// carry sensitive material further down.
const MaxSegments = 3

// NormalisePath strips the query string, truncates to MaxSegments, and
// replaces every id-shaped segment with ":id".
//
// The query string goes FIRST and unconditionally: ?token=…, ?api_key=… and
// ?reset=… are the ordinary way secrets end up in a URL, and ENGINEERING rule
// 20 has no exception for "it was already in a log".
func NormalisePath(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return "/"
	}
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return "/"
	}
	if len(parts) > MaxSegments {
		parts = parts[:MaxSegments]
	}
	for i, p := range parts {
		if looksLikeID(p) {
			parts[i] = ":id"
		}
	}
	return "/" + strings.Join(parts, "/")
}

// looksLikeID is all digits, a UUID, or eight-or-more characters that are all
// hexadecimal. Deliberately not "anything long": a slug like
// /blog/how-we-scaled-postgres is a real route and belongs in the table.
func looksLikeID(s string) bool {
	if s == "" {
		return false
	}
	digits, hex := true, true
	for _, r := range s {
		isDigit := r >= '0' && r <= '9'
		isHex := isDigit || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isDigit {
			digits = false
		}
		if !isHex && r != '-' {
			hex = false
		}
	}
	if digits {
		return true
	}
	if isUUID(s) {
		return true
	}
	return hex && len(strings.ReplaceAll(s, "-", "")) >= 8
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
