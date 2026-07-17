package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"
)

func newTestCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA(time.Now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	return ca
}

func TestCARoundTripsThroughPEM(t *testing.T) {
	ca := newTestCA(t)
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		t.Fatalf("KeyPEM: %v", err)
	}
	reloaded, err := Load(ca.CertPEM(), keyPEM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A cert signed by the reloaded CA must verify against the original CA's
	// cert — proving the reloaded key is the same signing key.
	_, csrPEM, err := GenerateAgentKey("agent")
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	certPEM, err := reloaded.SignAgentCSR(csrPEM, "srv_x", time.Hour, time.Now())
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	assertSignedBy(t, certPEM, ca.CertPEM(), "srv_x")
}

func TestSignAgentCSRSetsIdentityAndClientAuth(t *testing.T) {
	ca := newTestCA(t)
	_, csrPEM, err := GenerateAgentKey("whatever-the-agent-put")
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	certPEM, err := ca.SignAgentCSR(csrPEM, "srv_abc123", 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	cert := parseCert(t, certPEM)
	if cert.Subject.CommonName != "srv_abc123" {
		t.Errorf("CommonName = %q, want srv_abc123 (plane must override the CSR subject)", cert.Subject.CommonName)
	}
	if !hasClientAuth(cert) {
		t.Errorf("issued agent cert lacks ExtKeyUsageClientAuth")
	}
}

func TestSignAgentCSRRejectsGarbage(t *testing.T) {
	ca := newTestCA(t)
	for name, in := range map[string][]byte{
		"empty":     nil,
		"not-pem":   []byte("hello"),
		"wrong-pem": ca.CertPEM(), // a CERTIFICATE, not a CERTIFICATE REQUEST
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ca.SignAgentCSR(in, "srv_x", time.Hour, time.Now()); err == nil {
				t.Fatalf("expected error signing %s input, got nil", name)
			}
		})
	}
}

func TestExpiredCertFailsVerification(t *testing.T) {
	ca := newTestCA(t)
	_, csrPEM, _ := GenerateAgentKey("agent")
	// Issue a cert that expired an hour ago (ttl relative to a past 'now').
	past := time.Now().Add(-2 * time.Hour)
	certPEM, err := ca.SignAgentCSR(csrPEM, "srv_old", time.Hour, past)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	pool, _ := CertPool(ca.CertPEM())
	cert := parseCert(t, certPEM)
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err == nil {
		t.Fatal("expected expired cert to fail verification")
	}
}

// TestMutualTLSHandshake proves the plane and agent tls.Config builders
// interoperate over a real connection, and that the plane rejects a client
// with no certificate (threat-model §5.4).
func TestMutualTLSHandshake(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()

	serverCert, serverKey, err := ca.IssueServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)}, time.Hour, now)
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	serverCfg, err := ServerTLSConfig(serverCert, serverKey, ca.CertPEM())
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	agentKey, agentCSR, err := GenerateAgentKey("agent")
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	agentCert, err := ca.SignAgentCSR(agentCSR, "srv_handshake", time.Hour, now)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	clientCfg, err := ClientTLSConfig(agentCert, agentKey, ca.CertPEM(), "localhost")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	// Success case: mutual auth completes and the plane sees CN=srv_handshake.
	cnCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			errCh <- aerr
			return
		}
		defer conn.Close()
		tc := conn.(*tls.Conn)
		if herr := tc.Handshake(); herr != nil {
			errCh <- herr
			return
		}
		cnCh <- tc.ConnectionState().PeerCertificates[0].Subject.CommonName
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("client Dial: %v", err)
	}
	if err := conn.Handshake(); err != nil {
		t.Fatalf("client Handshake: %v", err)
	}
	conn.Close()

	select {
	case cn := <-cnCh:
		if cn != "srv_handshake" {
			t.Errorf("server saw client CN %q, want srv_handshake", cn)
		}
	case err := <-errCh:
		t.Fatalf("server handshake failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("handshake timed out")
	}

	// Rejection case: a client presenting no certificate is refused. In TLS 1.3
	// the client can complete its side before the server's alert arrives, so the
	// authoritative assertion is that the server's Handshake returns an error.
	srvErrCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			srvErrCh <- aerr
			return
		}
		defer conn.Close()
		srvErrCh <- conn.(*tls.Conn).Handshake()
	}()
	noCertCfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", InsecureSkipVerify: true} //nolint:gosec // deliberately skipping server verification to isolate the client-cert requirement
	if badConn, derr := tls.Dial("tcp", ln.Addr().String(), noCertCfg); derr == nil {
		// Force the server's rejection alert to surface, then discard it.
		_ = badConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var buf [1]byte
		_, _ = badConn.Read(buf[:])
		badConn.Close()
	}
	select {
	case serr := <-srvErrCh:
		if serr == nil {
			t.Fatal("plane accepted a client with no certificate")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake did not resolve")
	}
}

func assertSignedBy(t *testing.T, certPEM, caPEM []byte, wantCN string) {
	t.Helper()
	pool, err := CertPool(caPEM)
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}
	cert := parseCert(t, certPEM)
	chains, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(chains) == 0 {
		t.Fatal("no valid chains")
	}
	if cert.Subject.CommonName != wantCN {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, wantCN)
	}
}

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("no CERTIFICATE block in PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func hasClientAuth(cert *x509.Certificate) bool {
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}
