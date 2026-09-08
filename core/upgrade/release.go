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
// One or two comma-separated keys, the same list release-pubkey.txt holds and
// the agent bakes in: rotation ships the outgoing and the incoming key together
// for one release, and a panel that accepted only one would refuse the release
// signed with the other.
var ReleasePublicKey = ""

// The compatibility floors release.json carries, as CODE rather than as release
// inputs: CI and the signer's rebuild must produce a byte-identical
// release.json, so a value either side could type differently cannot exist.
// Bump these in the commit that breaks the thing they guard.
const (
	// AgentMinVersion is the oldest agent this panel can still talk to. The
	// wire is additive since the first release (control-plane-hardening.md
	// records the one deliberate break, before it), so this stays at the first
	// release until a field is removed or repurposed.
	AgentMinVersion = "v0.1.0"
	// RollbackFloor is the oldest panel whose binary can still read this
	// release's schema. Migrations are additive so far; a migration that drops
	// or rewrites a column moves this to the release that shipped it.
	RollbackFloor = "v0.1.0"
)

// trustedKeys parses the baked-in list. A malformed entry is dropped, as the
// agent does, so a typo in the second key cannot disable every upgrade.
func trustedKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, entry := range strings.Split(ReleasePublicKey, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(entry)
		if err != nil || len(b) != ed25519.PublicKeySize {
			continue
		}
		out = append(out, ed25519.PublicKey(b))
		if len(out) == 2 {
			break
		}
	}
	return out
}

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

// AssetName is the release file for this platform, by the layout release.yml
// publishes and install.sh downloads: cypherd-linux-<arch>. The first version
// of the helper asked for cypherd_<version>_linux_<arch>, a name nothing has
// ever uploaded, so every guided upgrade would have stopped at "the signed
// manifest does not cover" — after the signature had verified.
func AssetName(goarch string) string { return "cypherd-linux-" + goarch }

// VerifyRelease fetches SHA256SUMS, its signature and release.json for a tag,
// and returns the manifest only when every link holds.
//
// baseURL is where the release assets live, with a %s for the tag.
func VerifyRelease(ctx context.Context, f Fetcher, baseURL, version string) (VerifiedRelease, error) {
	// The version is interpolated into a URL below, and it arrives from an API
	// query parameter. Bounding its SHAPE here — rather than at the one handler
	// that happens to be the caller today — is what makes it impossible for any
	// call path to aim the panel's fetcher somewhere with a "../" or a "?".
	if !ValidTag(version) {
		return VerifiedRelease{}, fmt.Errorf("%w: %q is not a release tag", ErrUnverifiable, version)
	}
	if ReleasePublicKey == "" {
		return VerifiedRelease{}, fmt.Errorf("%w: this build carries no release public key, so it cannot check a signature", ErrUnverifiable)
	}
	keys := trustedKeys()
	if len(keys) == 0 {
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
	verified := false
	for _, pub := range keys {
		if ed25519.Verify(pub, sums, sig) {
			verified = true
			break
		}
	}
	if !verified {
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
		// "*name" is sha256sum's binary-mode marker and "./name" is what
		// `sha256sum ./*` writes; the release file is called neither.
		out[strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")] = strings.ToLower(fields[0])
	}
	return out
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
