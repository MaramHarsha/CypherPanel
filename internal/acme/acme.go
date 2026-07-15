// Package acme issues SSL certificates via the Lego ACME client (plan.md SSL
// Engine). HTTP-01 is solved with the webroot provider — the challenge token
// is written under the account's web root and served by the running web
// server, so nginx keeps serving :80 (no standalone challenge port). Runs
// agent-side inside the ssl.issue task.
package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"
)

// ErrWildcardNeedsDNS is returned when a wildcard certificate is requested but
// no DNS-01 provider is configured. Wildcards cannot be validated over HTTP-01
// (an ACME rule), so this is a permanent, non-retryable configuration error —
// it clears once DNS management (Phase 5, PowerDNS) is wired via SetDNSProvider.
var ErrWildcardNeedsDNS = errors.New("acme: wildcard certificate requires a DNS-01 provider")

// Issuer obtains certificates from a configured ACME directory. HTTP-01 (via
// the account web root) covers single hostnames; DNS-01 (via an injected
// provider) covers wildcards and is selected automatically by domain shape.
type Issuer struct {
	directory  string
	accountDir string
	dns        challenge.Provider // nil until DNS management is configured
}

func NewIssuer(directory, accountDir string) *Issuer {
	return &Issuer{directory: directory, accountDir: accountDir}
}

// SetDNSProvider wires the DNS-01 solver used for wildcard issuance. The DNS
// management layer (Phase 5) provides the concrete PowerDNS-backed provider.
func (i *Issuer) SetDNSProvider(p challenge.Provider) { i.dns = p }

// IsWildcard reports whether domain is a wildcard name (must use DNS-01).
func IsWildcard(domain string) bool { return strings.HasPrefix(domain, "*.") }

// Result is an issued certificate and its expiry.
type Result struct {
	CertPEM  []byte
	KeyPEM   []byte
	NotAfter time.Time
}

type acmeUser struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Obtain issues a certificate for domain. Single hostnames are solved over
// HTTP-01 via the given web root; wildcards are solved over DNS-01 via the
// configured provider. The ACME account key is persisted and reused.
func (i *Issuer) Obtain(domain, email, webRoot string) (*Result, error) {
	// Fail wildcards fast when no DNS provider exists — before any network I/O.
	if IsWildcard(domain) && i.dns == nil {
		return nil, fmt.Errorf("%w: %s", ErrWildcardNeedsDNS, domain)
	}

	key, err := i.loadOrCreateAccountKey()
	if err != nil {
		return nil, err
	}

	user := &acmeUser{email: email, key: key}
	cfg := lego.NewConfig(user)
	cfg.CADirURL = i.directory
	cfg.Certificate.KeyType = certcrypto.EC256

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("acme: creating client: %w", err)
	}

	if IsWildcard(domain) {
		// DNS-01: the provider sets the _acme-challenge TXT record.
		if err := client.Challenge.SetDNS01Provider(i.dns); err != nil {
			return nil, fmt.Errorf("acme: setting dns-01 provider: %w", err)
		}
	} else {
		provider, err := webroot.NewHTTPProvider(webRoot)
		if err != nil {
			return nil, fmt.Errorf("acme: webroot provider: %w", err)
		}
		if err := client.Challenge.SetHTTP01Provider(provider); err != nil {
			return nil, fmt.Errorf("acme: setting http-01 provider: %w", err)
		}
	}

	// Registration is idempotent for a given account key (the CA returns the
	// existing account), so re-issuing does not create duplicate accounts.
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("acme: registering account: %w", err)
	}
	user.reg = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("acme: obtaining certificate for %s: %w", domain, err)
	}

	notAfter, err := parseNotAfter(res.Certificate)
	if err != nil {
		return nil, err
	}
	return &Result{CertPEM: res.Certificate, KeyPEM: res.PrivateKey, NotAfter: notAfter}, nil
}

func (i *Issuer) loadOrCreateAccountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(i.accountDir, "account.key")
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("acme: account key is not PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generating account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: marshaling account key: %w", err)
	}
	if err := os.MkdirAll(i.accountDir, 0o700); err != nil {
		return nil, fmt.Errorf("acme: creating account dir: %w", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, fmt.Errorf("acme: writing account key: %w", err)
	}
	return key, nil
}

func parseNotAfter(certPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, fmt.Errorf("acme: issued certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("acme: parsing issued certificate: %w", err)
	}
	return cert.NotAfter, nil
}

// CertValidUntil returns the NotAfter of an already-installed certificate, or
// zero time if absent/unreadable — used to skip re-issuing a still-valid cert.
func CertValidUntil(certPath string) time.Time {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}
	}
	notAfter, err := parseNotAfter(data)
	if err != nil {
		return time.Time{}
	}
	return notAfter
}
