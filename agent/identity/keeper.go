package identity

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sync"
	"time"
)

// Keeper owns the identity a running agent is using: the material on disk and
// the parsed keypair every TLS handshake presents.
//
// It exists so a renewal takes effect without tearing anything down. The bus
// and the relay hand tls.Config a GetClientCertificate callback backed by this
// type rather than a fixed Certificates slice, so the next handshake — a NATS
// reconnect after a plane restart, a relay dial — presents whatever certificate
// is current at that moment. Nothing has to be reconnected on rotation, which
// means a renewal cannot drop desired state, redeliver work items, or interrupt
// a transfer: the connection in flight keeps its already-negotiated session and
// the next one is made with the new identity.
type Keeper struct {
	dir string

	mu       sync.RWMutex
	id       *Identity
	cert     *tls.Certificate
	notAfter time.Time
}

// NewKeeper wraps an identity loaded from dir. It parses the material up front:
// an agent that cannot build a keypair from what it has on disk must fail at
// startup with that message, not at its first handshake with a TLS error.
func NewKeeper(dir string, id *Identity) (*Keeper, error) {
	k := &Keeper{dir: dir, id: id}
	if err := k.install(id.CertPEM, id.KeyPEM); err != nil {
		return nil, err
	}
	return k, nil
}

// install parses and swaps in a certificate/key pair. Callers hold no lock.
func (k *Keeper) install(certPEM, keyPEM []byte) error {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("identity: loading agent keypair: %w", err)
	}
	notAfter, err := NotAfter(certPEM)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.cert = &cert
	k.notAfter = notAfter
	k.id.CertPEM = certPEM
	k.id.KeyPEM = keyPEM
	return nil
}

// ServerID is the agent's stable identity — unchanged by renewal.
func (k *Keeper) ServerID() string { return k.id.ServerID }

// NATSURL is the data-plane address the agent dials.
func (k *Keeper) NATSURL() string { return k.id.NATSURL }

// PlaneAddr is the gRPC address the agent enrolled against (relay + renewal).
func (k *Keeper) PlaneAddr() string { return k.id.PlaneAddr }

// CACertPEM is the pinned control-plane CA.
func (k *Keeper) CACertPEM() []byte {
	out := make([]byte, len(k.id.CACertPEM))
	copy(out, k.id.CACertPEM)
	return out
}

// NotAfter is when the current certificate expires.
func (k *Keeper) NotAfter() time.Time {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.notAfter
}

// Certificate returns the certificate to present right now. Its signature is
// what tls.Config.GetClientCertificate takes.
func (k *Keeper) Certificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.cert, nil
}

// CertificatePEM returns the current certificate in PEM form — what a renewal
// request is measured against and what tests assert on.
func (k *Keeper) CertificatePEM() []byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]byte, len(k.id.CertPEM))
	copy(out, k.id.CertPEM)
	return out
}

// Rotate writes freshly-signed material to disk and makes it current.
//
// Disk first, memory second, deliberately: material this process is using but
// has not persisted would vanish on restart, and the agent would come back
// holding a certificate the plane has already moved past. The reverse order —
// persisted but not in use — is merely redundant work on the next boot.
func (k *Keeper) Rotate(certPEM, keyPEM []byte) error {
	if err := Rotate(k.dir, certPEM, keyPEM); err != nil {
		return err
	}
	return k.install(certPEM, keyPEM)
}

// NotAfter parses a PEM certificate's expiry. Exported because the renewal
// loop's whole schedule is derived from it and the agent logs it on boot.
func NotAfter(certPEM []byte) (time.Time, error) {
	_, notAfter, err := Lifetime(certPEM)
	return notAfter, err
}

// Lifetime reports a certificate's validity window. The renewal loop needs both
// ends: "two thirds of the way through" is meaningless without the start.
func Lifetime(certPEM []byte) (notBefore, notAfter time.Time, err error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, time.Time{}, fmt.Errorf("identity: certificate PEM: expected a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("identity: parsing certificate: %w", err)
	}
	return cert.NotBefore, cert.NotAfter, nil
}
