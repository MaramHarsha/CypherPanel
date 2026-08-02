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
// This runs on the release manager's machine, never in CI. An Actions secret is
// an online key by definition — available to every workflow and to whatever code
// a compromised tag makes the runner execute — so holding the release key there
// would give an attacker the power to sign fleet binaries, which is the single
// thing this design exists to deny. CI builds and publishes a DRAFT; signing and
// publishing are the human step.
//
// Usage:
//
//	release-sign -in dist/SHA256SUMS -out dist/SHA256SUMS.sig
//	release-sign -public                                  # print the public key
//	release-sign -verify -in SHA256SUMS -sig SHA256SUMS.sig -key <base64>
//
// The private key arrives base64-encoded in CYPHER_RELEASE_SIGNING_KEY, as
// either a 32-byte seed or a 64-byte expanded key. -verify needs only -key.
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
	in := flag.String("in", "", "file to sign or verify")
	out := flag.String("out", "", "signature output path")
	sigPath := flag.String("sig", "", "signature to check, with -verify")
	pubKey := flag.String("key", "", "base64 public key, with -verify")
	verify := flag.Bool("verify", false, "check a signature instead of making one")
	public := flag.Bool("public", false, "print the public key and exit")
	flag.Parse()

	// Verification needs no private key, so anyone can run it — that is the
	// point of publishing the signature at all.
	if *verify {
		verifyRelease(*in, *sigPath, *pubKey)
		return
	}

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

// verifyRelease checks a published manifest against the release public key.
// Exits non-zero on any failure, so it composes into a script without the
// caller having to parse output.
func verifyRelease(in, sigPath, pub string) {
	if in == "" || sigPath == "" || pub == "" {
		die("-verify needs -in, -sig and -key")
	}
	pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pub))
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		die("public key must be %d base64-encoded bytes", ed25519.PublicKeySize)
	}
	manifest, err := os.ReadFile(in)
	if err != nil {
		die("read %s: %v", in, err)
	}
	raw, err := os.ReadFile(sigPath)
	if err != nil {
		die("read %s: %v", sigPath, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		die("signature is not valid base64: %v", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), manifest, sig) {
		die("SIGNATURE DOES NOT VERIFY — do not install these artifacts")
	}
	fmt.Printf("signature verifies: %s covers %d bytes\n", sigPath, len(manifest))
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
