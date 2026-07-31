// Package dkim generates and manages per-domain DKIM signing keys.
//
// Outbound mail from a fresh VPS without DKIM is treated as suspicious by every
// major provider, so a mailbox is not really provisioned until its domain can
// sign. The agent owns the private key (it never leaves the mail server); Core
// only ever sees the public half, which it publishes as the `<selector>._domainkey`
// TXT record.
package dkim

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSelector names the key in DNS: <selector>._domainkey.<domain>.
// Configurable so an operator can rotate by publishing a second selector
// before retiring the first.
const DefaultSelector = "cypher"

// KeyBits is the RSA modulus size. 2048 is the DKIM sweet spot: 1024 is
// deprecated by major receivers, and 4096 overflows what some resolvers will
// return in a single UDP response.
const KeyBits = 2048

// Key is a generated DKIM keypair as the two sides need it.
type Key struct {
	Selector string
	Domain   string
	// PrivatePEM is written to disk on the mail server, mode 0600.
	PrivatePEM []byte
	// PublicTXT is the DNS record value, e.g. "v=DKIM1; k=rsa; p=MIIBI...".
	PublicTXT string
}

// RecordName returns the FQDN the public key is published at.
func RecordName(domain, selector string) string {
	if selector == "" {
		selector = DefaultSelector
	}
	return selector + "._domainkey." + domain
}

// Generate creates a fresh DKIM keypair for a domain.
func Generate(domain, selector string) (*Key, error) {
	if domain == "" {
		return nil, fmt.Errorf("dkim: domain is required")
	}
	if selector == "" {
		selector = DefaultSelector
	}
	priv, err := rsa.GenerateKey(rand.Reader, KeyBits)
	if err != nil {
		return nil, fmt.Errorf("dkim: generating key: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: mustPKCS8(priv),
	})

	// DKIM publishes the SubjectPublicKeyInfo DER, base64'd — not a PEM body.
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("dkim: marshalling public key: %w", err)
	}

	return &Key{
		Selector:   selector,
		Domain:     domain,
		PrivatePEM: privPEM,
		PublicTXT:  "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pubDER),
	}, nil
}

func mustPKCS8(priv *rsa.PrivateKey) []byte {
	// MarshalPKCS8PrivateKey only fails for key types it does not support;
	// *rsa.PrivateKey is always supported, so an error here is impossible.
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic("dkim: marshalling an RSA private key cannot fail: " + err.Error())
	}
	return der
}

// EnsureKey returns the existing key for a domain, generating and persisting
// one if absent. It is idempotent, so a redelivered mail.create task reuses
// the published key instead of rotating it out from under working senders.
func EnsureKey(dir, domain, selector string) (*Key, error) {
	if selector == "" {
		selector = DefaultSelector
	}
	path := KeyPath(dir, domain, selector)

	if data, err := os.ReadFile(path); err == nil {
		pub, perr := PublicTXTFromPrivate(data)
		if perr == nil {
			return &Key{Selector: selector, Domain: domain, PrivatePEM: data, PublicTXT: pub}, nil
		}
		// A corrupt key file would silently break signing for this domain
		// forever; fail loudly rather than quietly overwrite it.
		return nil, fmt.Errorf("dkim: existing key for %s is unreadable: %w", domain, perr)
	}

	key, err := Generate(domain, selector)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("dkim: creating key dir: %w", err)
	}
	// 0600: the signing key is as sensitive as a TLS private key.
	if err := os.WriteFile(path, key.PrivatePEM, 0o600); err != nil {
		return nil, fmt.Errorf("dkim: writing key: %w", err)
	}
	return key, nil
}

// KeyPath is where a domain's private signing key lives.
func KeyPath(dir, domain, selector string) string {
	if selector == "" {
		selector = DefaultSelector
	}
	return filepath.Join(dir, domain, selector+".private")
}

// PublicTXTFromPrivate recovers the published record value from a stored key,
// so the DNS record can be republished without rotating the key.
func PublicTXTFromPrivate(privPEM []byte) (string, error) {
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return "", fmt.Errorf("dkim: not a PEM private key")
	}
	var pub any
	switch key, err := x509.ParsePKCS8PrivateKey(block.Bytes); {
	case err == nil:
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("dkim: key is not RSA")
		}
		pub = &rsaKey.PublicKey
	default:
		// Tolerate PKCS#1, which older tooling (and opendkim-genkey) emits.
		k1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err1 != nil {
			return "", fmt.Errorf("dkim: parsing private key: %w", err)
		}
		pub = &k1.PublicKey
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("dkim: marshalling public key: %w", err)
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
}

// SplitTXT chunks a TXT value into DNS-legal 255-byte strings, each quoted.
//
// A 2048-bit DKIM record is ~400 characters, well past the 255-byte limit for a
// single character-string. Publishing it unsplit yields a record that most
// authoritative servers reject and every verifier fails on.
func SplitTXT(v string) string {
	const maxChunk = 255
	if len(v) <= maxChunk {
		return `"` + v + `"`
	}
	var b strings.Builder
	for i := 0; i < len(v); i += maxChunk {
		end := i + maxChunk
		if end > len(v) {
			end = len(v)
		}
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(`"` + v[i:end] + `"`)
	}
	return b.String()
}
