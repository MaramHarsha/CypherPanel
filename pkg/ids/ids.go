// Package ids generates the identifiers and secrets used across CypherPanel:
// prefixed, URL-safe resource IDs and high-entropy opaque tokens.
//
// All randomness comes from crypto/rand via the standard library's rand.Text,
// which draws from the OS CSPRNG and does not surface an error (Go 1.24+).
package ids

import (
	"crypto/rand"
	"strings"
)

// Resource ID prefixes. These are stable wire/DB values — never renumber or
// repurpose an existing prefix (it would alias historical identifiers).
const (
	PrefixServer      = "srv"
	PrefixUser        = "usr"
	PrefixJoinToken   = "jt"
	PrefixAPIToken    = "tok"
	PrefixSession     = "ses"
	PrefixProject     = "prj"
	PrefixEnvironment = "env"
	PrefixApplication = "app"
	PrefixRevision    = "rev"
	PrefixDeployment  = "dep"
	PrefixWebhook     = "wh"
	PrefixDeployKey   = "dk"
)

// New returns a prefixed, URL-safe, collision-resistant identifier such as
// "srv_k7q2v9v3m8...". The random component carries ~130 bits of entropy.
func New(prefix string) string {
	return prefix + "_" + strings.ToLower(rand.Text())
}

// Secret returns a fresh high-entropy opaque secret (~130 bits) suitable for a
// join token or similar bearer credential. Unlike New it carries no prefix and
// preserves the full base32 alphabet, so it is meant to be compared, never
// parsed. Store only a hash of it (see core/store); compare in constant time.
func Secret() string {
	return rand.Text()
}
