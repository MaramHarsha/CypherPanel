package updater

// The signed manifest IS the checksum (agent-updates.md §3.2).
//
// ADR-010 leaves open where the checksum comes from, given that the plane must
// not become a distributor. This is the answer: `SHA256SUMS` plus
// `SHA256SUMS.sig`, verified against a key baked into this binary. A digest
// copied into the plane's database would be a second, weaker source of the same
// fact — and one a compromised plane controls.

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Sentinels a caller may distinguish (ENGINEERING rule 3).
var (
	// ErrNoTrustedKeys: this build bakes in no release key, so it can verify
	// nothing. Reported as PHASE_DISABLED, never retried as a failure.
	ErrNoTrustedKeys = errors.New("updater: this build trusts no release key")
	// ErrBadSignature: the manifest is not signed by any key we trust.
	ErrBadSignature = errors.New("updater: manifest signature does not verify")
	// ErrNotInManifest: the artifact this host needs is not listed.
	ErrNotInManifest = errors.New("updater: artifact is not listed in the manifest")
	// ErrDigestMismatch: the downloaded bytes are not the ones signed for.
	ErrDigestMismatch = errors.New("updater: downloaded artifact does not match its signed digest")
)

// manifest maps a release file name to its SHA-256, as parsed from a verified
// SHA256SUMS. Nothing outside verifyManifest may construct one, which is what
// keeps "parsed" and "verified" from drifting apart.
type manifest map[string][32]byte

// verifyManifest checks sig over the EXACT bytes of sums against the trusted
// keys, then parses. Order matters: nothing in an unverified manifest is read,
// not even to decide whether it is worth verifying.
func verifyManifest(sums, sig []byte) (manifest, error) {
	keys := trustedKeys()
	if len(keys) == 0 {
		return nil, ErrNoTrustedKeys
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature is %d bytes", ErrBadSignature, len(sig))
	}
	ok := false
	for _, k := range keys {
		if ed25519.Verify(k, sums, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	return parseManifest(sums)
}

// parseManifest reads sha256sum(1)'s own output shape: "<64 hex>  <name>", one
// per line, with a leading "*" on the name in binary mode. Anything that is not
// that shape is skipped rather than failing the parse — a manifest may list
// files this host will never ask for, in formats a later release adds.
func parseManifest(sums []byte) (manifest, error) {
	out := manifest{}
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		digest, name, found := strings.Cut(line, " ")
		if !found || len(digest) != 64 {
			continue
		}
		raw, err := hex.DecodeString(digest)
		if err != nil {
			continue
		}
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "*")
		// `sha256sum ./*` writes "./cypher-agent-linux-amd64", and the release
		// workflow did exactly that: every lookup by bare name missed.
		name = strings.TrimPrefix(name, "./")
		if name == "" {
			continue
		}
		var sum [32]byte
		copy(sum[:], raw)
		out[name] = sum
	}
	if len(out) == 0 {
		return nil, errors.New("updater: manifest lists no artifacts")
	}
	return out, nil
}

// artifactName is the release file this host needs. The plane never guesses the
// host's architecture — it has never needed to know it — so the agent names its
// own (agent-updates.md §3.3).
func artifactName(goarch string) string { return "cypher-agent-linux-" + goarch }
