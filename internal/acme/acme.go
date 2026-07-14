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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"
)

// Issuer obtains certificates from a configured ACME directory.
type Issuer struct {
	directory  string
	accountDir string
}

func NewIssuer(directory, accountDir string) *Issuer {
	return &Issuer{directory: directory, accountDir: accountDir}
}

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

// Obtain issues a certificate for domain, solving HTTP-01 via the given web
// root. The ACME account key is persisted and reused across calls.
func (i *Issuer) Obtain(domain, email, webRoot string) (*Result, error) {
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

	provider, err := webroot.NewHTTPProvider(webRoot)
	if err != nil {
		return nil, fmt.Errorf("acme: webroot provider: %w", err)
	}
	if err := client.Challenge.SetHTTP01Provider(provider); err != nil {
		return nil, fmt.Errorf("acme: setting http-01 provider: %w", err)
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
