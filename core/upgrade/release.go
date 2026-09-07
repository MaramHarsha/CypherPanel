package upgrade

// The release manifest and its verification (panel-updates.md §4).
//
// The chain is: SHA256SUMS is signed offline by a human against a local
// rebuild of the tag; release.json's digest appears IN that manifest; so a
// release gains a machine-readable description with no new signing machinery.
// A SHA256SUMS file hosted beside the binaries cannot carry that weight on its
// own — whoever can replace an asset can replace the sums next to it, so
// checksums prove integrity in transit and nothing at all about origin.
//
// A verification failure is a REFUSAL, never a warning. An unverifiable
// artifact is the one thing this feature exists to not install.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReleasePublicKey is the offline signing key's public half, baked into this
// binary. Empty means this build cannot verify anything, and every upgrade path
// refuses rather than proceeding unverified — an unsigned build must not be
// able to install a signed one on trust.
//
// Set at build time with -ldflags "-X …/core/upgrade.ReleasePublicKey=<base64>".
var ReleasePublicKey = ""

// ErrUnverifiable is any break in the chain from the signature to the file.
var ErrUnverifiable = errors.New("upgrade: the release could not be verified")

// Manifest is release.json — the machine-readable description of one release.
type Manifest struct {
	Version string `json:"version"`
	// SchemaVersion is the migration this release expects. History compares it
	// so a rollback below the floor is refused NAMING the floor rather than
	// attempted and half-failed.
	SchemaVersion int `json:"schema_version"`
	// RollbackFloor is the oldest version whose schema this one can still be
	// read by.
	RollbackFloor string `json:"rollback_floor"`
	// AgentMinVersion is the fleet compatibility floor.
	AgentMinVersion string `json:"agent_min_version"`
	NotesURL        string `json:"notes_url"`
	PublishedAt     string `json:"published_at"`
}

// Fetcher is the hardened HTTP path (consumer-defined; *updates.Checker
// satisfies it). There is deliberately only one: a second, softer client is how
// a body cap or a private-redirect check quietly stops applying.
type Fetcher interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// VerifiedRelease is a manifest whose whole chain checked out, plus the digests
// the helper will hold the downloaded binary against.
type VerifiedRelease struct {
	Manifest Manifest
	// Digests maps a release file name to its hex sha256, from the SIGNED
	// manifest — so the binary is checked against a signature, not against a
	// checksum file anyone who can replace the binary could also replace.
	Digests map[string]string
}

// VerifyRelease fetches SHA256SUMS, its signature and release.json for a tag,
// and returns the manifest only when every link holds.
//
// baseURL is where the release assets live, with a %s for the tag.
func VerifyRelease(ctx context.Context, f Fetcher, baseURL, version string) (VerifiedRelease, error) {
	if ReleasePublicKey == "" {
		return VerifiedRelease{}, fmt.Errorf("%w: this build carries no release public key, so it cannot check a signature", ErrUnverifiable)
	}
	pub, err := base64.StdEncoding.DecodeString(ReleasePublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return VerifiedRelease{}, fmt.Errorf("%w: the baked release key is malformed", ErrUnverifiable)
	}

	base := strings.TrimSuffix(fmt.Sprintf(baseURL, version), "/")
	sums, err := f.Fetch(ctx, base+"/SHA256SUMS")
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("%w: fetching the manifest: %v", ErrUnverifiable, err)
	}
	rawSig, err := f.Fetch(ctx, base+"/SHA256SUMS.sig")
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("%w: fetching the signature: %v", ErrUnverifiable, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(rawSig)))
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("%w: the signature is not base64", ErrUnverifiable)
	}
	// THE load-bearing line. Everything after it is arithmetic over bytes a
	// human signed offline.
	if !ed25519.Verify(ed25519.PublicKey(pub), sums, sig) {
		return VerifiedRelease{}, fmt.Errorf("%w: the manifest's signature does not match the release key", ErrUnverifiable)
	}

	digests := ParseSums(string(sums))
	wantRelease, ok := digests["release.json"]
	if !ok {
		return VerifiedRelease{}, fmt.Errorf("%w: the signed manifest does not cover release.json", ErrUnverifiable)
	}
	body, err := f.Fetch(ctx, base+"/release.json")
	if err != nil {
		return VerifiedRelease{}, fmt.Errorf("%w: fetching release.json: %v", ErrUnverifiable, err)
	}
	if got := hex.EncodeToString(sha256Sum(body)); got != wantRelease {
		return VerifiedRelease{}, fmt.Errorf("%w: release.json does not match its signed digest", ErrUnverifiable)
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return VerifiedRelease{}, fmt.Errorf("%w: release.json will not parse: %v", ErrUnverifiable, err)
	}
	if m.Version != version {
		return VerifiedRelease{}, fmt.Errorf("%w: release.json names %s but %s was requested", ErrUnverifiable, m.Version, version)
	}
	return VerifiedRelease{Manifest: m, Digests: digests}, nil
}

// ParseSums reads a `<hex>  <name>` manifest. Unknown lines are skipped rather
// than failing: the manifest covers more files than any one consumer needs.
func ParseSums(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	return out
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
