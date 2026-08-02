// release-sign signs a release manifest with the offline Ed25519 release key.
//
// ADR-010 requires agents to verify a signature against a public key baked into
// their own binary before swapping in fleet-wide code. A SHA256SUMS file hosted
// beside the binaries cannot carry that weight: whoever can replace an asset can
// replace the sums next to it, so checksums prove integrity in transit and
// nothing at all about origin. The signature is what a compromised release
// cannot forge, because the key that makes it never touches CI.
//
// Ed25519 raw over the manifest bytes, deliberately: crypto/ed25519 is in the
// standard library, so the agent verifies with no dependency to compromise and
// no verification service to be offline at the moment a fleet needs patching.
//
// Usage:
//
//	release-sign -in dist/SHA256SUMS -out dist/SHA256SUMS.sig
//	release-sign -public   # print the public key for the given private key
//
// The private key arrives base64-encoded in CYPHER_RELEASE_SIGNING_KEY, as
// either a 32-byte seed or a 64-byte expanded key.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	in := flag.String("in", "", "file to sign")
	out := flag.String("out", "", "signature output path")
	public := flag.Bool("public", false, "print the public key and exit")
	flag.Parse()

	key, err := privateKey(os.Getenv("CYPHER_RELEASE_SIGNING_KEY"))
	if err != nil {
		die("%v", err)
	}

	if *public {
		pub := key.Public().(ed25519.PublicKey)
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}
	if *in == "" || *out == "" {
		die("-in and -out are required")
	}

	manifest, err := os.ReadFile(*in)
	if err != nil {
		die("read %s: %v", *in, err)
	}
	if len(manifest) == 0 {
		// An empty manifest would sign cleanly and assert nothing, which is
		// worse than no signature because it looks verified.
		die("%s is empty — refusing to sign a manifest that covers nothing", *in)
	}

	sig := ed25519.Sign(key, manifest)
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), manifest, sig) {
		die("the signature did not verify against its own key — refusing to publish")
	}
	if err := os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		die("write %s: %v", *out, err)
	}
	fmt.Printf("signed %s (%d bytes) -> %s\n", *in, len(manifest), *out)
}

// privateKey decodes the signing key, rejecting anything it cannot use rather
// than falling back to an unsigned release.
func privateKey(raw string) (ed25519.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("CYPHER_RELEASE_SIGNING_KEY is not set — see docs/dev/release-signing.md")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("CYPHER_RELEASE_SIGNING_KEY is not valid base64: %w", err)
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("signing key is %d bytes, want %d (seed) or %d (expanded)",
			len(b), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "release-sign: "+format+"\n", a...)
	os.Exit(1)
}
