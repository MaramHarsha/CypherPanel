package updater

// The release trust store (agent-updates.md §6, ADR-010 §3).
//
// Keys are compiled-in defaults, overridable with `-ldflags`, which is also how
// an air-gapped fleet bakes its own. Rotation therefore needs no side channel:
// release N is signed with A and bakes in {A, B}; N+1 is signed with B; N+2
// bakes in {B} alone. Every step is an ordinary signed update, and at most two
// keys are trusted at once — a list that only grows is a trust surface that
// only grows, so the list is capped rather than appended to forever.
//
// THERE IS NO FLAG THAT SKIPS VERIFICATION. An operator running an internal
// mirror signs their builds and bakes their key, or does not get automatic
// updates. A skip flag is the hole everything else here leaks through.
//
// This repository has published no signed release yet
// (docs/dev/release-signing.md), so the default list is EMPTY — which is not a
// stubbed check but its opposite: with nothing to verify against, the updater
// verifies nothing successfully, reports PHASE_DISABLED naming the reason, and
// never renames a file. The check is real from the first build; what is missing
// is a key, and the release pipeline supplies it.

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
)

// publicKeys is a comma-separated list of base64 (standard encoding) ed25519
// public keys, set at build time with:
//
//	-ldflags "-X github.com/MaramHarsha/cypherpanel/agent/updater.publicKeys=<b64>[,<b64>]"
var publicKeys = ""

// maxTrustedKeys bounds the store at what rotation actually needs: the key that
// signed the release before this one, and the key that will sign the next.
const maxTrustedKeys = 2

// trustedKeys parses the baked-in list. A malformed or wrong-length entry is
// DROPPED rather than failing the parse, because the alternative is a build
// whose typo'd second key disables updates entirely — and a store that ends up
// empty already reports itself as such.
func trustedKeys() []ed25519.PublicKey {
	raw := strings.Split(publicKeys, ",")
	out := make([]ed25519.PublicKey, 0, len(raw))
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(entry)
		if err != nil || len(b) != ed25519.PublicKeySize {
			continue
		}
		out = append(out, ed25519.PublicKey(b))
		if len(out) == maxTrustedKeys {
			break
		}
	}
	return out
}
