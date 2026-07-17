// Package pki is CypherPanel's certificate authority and mTLS toolkit.
//
// It implements the trust model of ADR-002 and the threat model (§5.1–5.4):
//
//   - The control plane holds one CA (a self-signed ECDSA P-256 cert + key).
//   - Agents generate their own private key locally and send only a CSR; the
//     plane signs a short-lived client certificate whose CommonName is the
//     server ID. The agent's private key never crosses the wire (§5.1).
//   - All agent↔plane transport is mTLS, TLS 1.3, verified against the pinned
//     CA in both directions (§5.4; ENGINEERING rule 23).
//
// Nothing here logs, and no function returns secret bytes in an error.
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
	"time"
)

const (
	pemCertificate = "CERTIFICATE"
	pemPrivateKey  = "PRIVATE KEY"
	pemCSR         = "CERTIFICATE REQUEST"

	// agentOrg tags issued agent certs so the plane can distinguish an agent
	// identity from its own server certs when inspecting the peer chain.
	agentOrg = "cypherpanel-agent"
)

// CA is the control plane's certificate authority. Construct it with NewCA on
// first boot and persist the PEM material (KeyPEM encrypted at rest — the CA
// key is asset A2, threat-model §2); reload it thereafter with Load.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// NewCA generates a fresh CA valid for ten years from now.
func NewCA(now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generating CA key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "CypherPanel Control Plane CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: self-signing CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parsing new CA cert: %w", err)
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}),
	}, nil
}

// Load reconstructs a CA from persisted PEM material.
func Load(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != pemCertificate {
		return nil, fmt.Errorf("pki: CA cert PEM: expected %s block", pemCertificate)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parsing CA cert: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != pemPrivateKey {
		return nil, fmt.Errorf("pki: CA key PEM: expected %s block", pemPrivateKey)
	}
	anyKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parsing CA key: %w", err)
	}
	key, ok := anyKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("pki: CA key is %T, want *ecdsa.PrivateKey", anyKey)
	}
	return &CA{cert: cert, key: key, certPEM: certPEM}, nil
}

// CertPEM returns the CA certificate in PEM form (safe to distribute; this is
// what agents pin).
func (c *CA) CertPEM() []byte {
	out := make([]byte, len(c.certPEM))
	copy(out, c.certPEM)
	return out
}

// KeyPEM returns the CA private key in PKCS#8 PEM form. This is asset A2 —
// callers must encrypt it at rest and never log it (threat-model §5.1).
func (c *CA) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		return nil, fmt.Errorf("pki: marshaling CA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: der}), nil
}

// SignAgentCSR verifies an agent's CSR and issues a client certificate whose
// CommonName is serverID, valid for ttl. The CSR's own subject is ignored: the
// plane is authoritative for identity. Proof-of-possession is enforced by
// checking the CSR signature, so only the holder of the matching private key
// can obtain a cert for that key.
func (c *CA) SignAgentCSR(csrPEM []byte, serverID string, ttl time.Duration, now time.Time) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != pemCSR {
		return nil, fmt.Errorf("pki: CSR PEM: expected %s block", pemCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parsing CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: CSR proof-of-possession: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   serverID,
			Organization: []string{agentOrg},
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("pki: signing agent cert: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), nil
}

// IssueServerCert issues a serverAuth certificate for the plane's own TLS
// listeners (gRPC enrollment endpoint, embedded NATS), signed by the CA and
// valid for the supplied DNS names and IPs.
func (c *CA) IssueServerCert(dnsNames []string, ipAddrs []net.IP, ttl time.Duration, now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generating server key: %w", err)
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "CypherPanel Control Plane"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddrs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: signing server cert: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshaling server key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// GenerateAgentKey creates a fresh agent private key and a CSR to send to the
// plane. commonName is advisory (the plane overrides it with the server ID).
// The returned keyPEM stays on the agent host and is never transmitted.
func GenerateAgentKey(commonName string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generating agent key: %w", err)
	}
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: creating CSR: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshaling agent key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: keyDER})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: pemCSR, Bytes: csrDER})
	return keyPEM, csrPEM, nil
}

// CertPool returns an x509 pool containing exactly the given CA PEM, for
// pinning. Callers use it as both RootCAs (verify the plane) and ClientCAs
// (verify agents).
func CertPool(caPEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("pki: CA PEM contained no usable certificate")
	}
	return pool, nil
}

// ServerTLSConfig builds the plane's mTLS listener config: it presents the
// server cert and requires a client cert signed by the CA (§5.4). TLS 1.3 only.
func ServerTLSConfig(serverCertPEM, serverKeyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: loading server keypair: %w", err)
	}
	pool, err := CertPool(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// ServerBootstrapTLSConfig builds the enrollment endpoint's config: server-auth
// only. The enrolling agent has no client cert yet (it is bootstrapping), so
// this listener authenticates callers by join token at the application layer,
// not by client cert (threat-model §5.3).
func ServerBootstrapTLSConfig(serverCertPEM, serverKeyPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: loading bootstrap keypair: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}, nil
}

// ClientTLSConfig builds the agent's mTLS config: it presents the agent cert
// and verifies the plane against the pinned CA. serverName must match a SAN on
// the plane's server cert.
func ClientTLSConfig(clientCertPEM, clientKeyPEM, caPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: loading client keypair: %w", err)
	}
	pool, err := CertPool(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}, nil
}

// BootstrapClientTLSConfig builds the agent's config for the enrollment call:
// it pins the plane's CA (verifying it is talking to the real plane) but
// presents no client cert, because it does not have one yet.
func BootstrapClientTLSConfig(caPEM []byte, serverName string) (*tls.Config, error) {
	pool, err := CertPool(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
	}, nil
}

func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("pki: generating serial: %w", err)
	}
	return n, nil
}
