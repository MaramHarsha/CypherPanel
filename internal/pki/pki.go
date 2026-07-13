// Package pki manages the CypherPanel certificate authority used for
// CypherCore <-> CypherAgent mTLS (plan.md Section 7). The CA lives on the
// control plane; each agent gets a client certificate at enrollment.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	certValidity = 2 * 365 * 24 * time.Hour
)

// InitCA creates a new certificate authority in dir (ca.crt / ca.key).
// Fails if a CA already exists — never silently overwrite trust roots.
func InitCA(dir string) error {
	caCert := filepath.Join(dir, "ca.crt")
	if _, err := os.Stat(caCert); err == nil {
		return fmt.Errorf("pki: CA already exists at %s", caCert)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("pki: creating %s: %w", dir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generating CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "CypherPanel CA", Organization: []string{"CypherPanel"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("pki: creating CA certificate: %w", err)
	}

	if err := writeCert(caCert, der); err != nil {
		return err
	}
	return writeKey(filepath.Join(dir, "ca.key"), key)
}

// IssueOptions describe a leaf certificate to issue.
type IssueOptions struct {
	Name     string   // file basename and CommonName
	IsServer bool     // server cert (Core) vs client cert (Agent)
	DNSNames []string
	IPs      []string
}

// Issue creates a CA-signed leaf certificate (<name>.crt / <name>.key) in dir.
func Issue(dir string, opts IssueOptions) error {
	caCert, caKey, err := loadCA(dir)
	if err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generating key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: opts.Name, Organization: []string{"CypherPanel"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     opts.DNSNames,
	}
	for _, ip := range opts.IPs {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return fmt.Errorf("pki: invalid IP %q", ip)
		}
		tmpl.IPAddresses = append(tmpl.IPAddresses, parsed)
	}
	if opts.IsServer {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("pki: signing certificate: %w", err)
	}

	if err := writeCert(filepath.Join(dir, opts.Name+".crt"), der); err != nil {
		return err
	}
	return writeKey(filepath.Join(dir, opts.Name+".key"), key)
}

// ServerTLS builds the mTLS config for the Core gRPC listener: present the
// server cert, require and verify client certs against the CA.
func ServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("pki: loading server keypair: %w", err)
	}
	pool, err := loadPool(clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLS builds the mTLS config for an Agent dialing Core.
func ClientTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("pki: loading client keypair: %w", err)
	}
	pool, err := loadPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("pki: reading CA cert (run `pki init` first?): %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("pki: reading CA key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("pki: ca.crt is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parsing CA cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("pki: ca.key is not PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: parsing CA key: %w", err)
	}
	return cert, key, nil
}

func loadPool(caFile string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("pki: reading CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("pki: no certificates found in %s", caFile)
	}
	return pool, nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: generating serial: %w", err)
	}
	return serial, nil
}

func writeCert(path string, der []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("pki: writing %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("pki: marshaling key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("pki: writing %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
