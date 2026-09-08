// Package identity persists an enrolled agent's credentials on the server it
// runs on: the mTLS client certificate, the private key (which never left this
// host — threat-model §5.1), the pinned CA, and the coordinates the agent needs
// to reach the control plane.
//
// Certificates are short-lived and renewed in place (ADR-002; threat-model
// §5.2), so this package also owns the swap: Rotate installs freshly-signed
// material without ever leaving a certificate and a key on disk that do not
// match each other. See keeper.go for the in-memory half and renew.go for the
// loop that decides when.
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	certFile = "agent-cert.pem"
	keyFile  = "agent-key.pem"
	caFile   = "ca.pem"
	metaFile = "identity.json"

	// Alternate slot. Renewal never overwrites the material it is replacing:
	// it writes the new pair into the free slot and then commits by renaming
	// identity.json, which names the pair in use. That rename is the single
	// atomic act of the whole rotation — crash before it and the old identity
	// is intact and still referenced; crash after it and the new one is.
	//
	// Writing cert and key under their existing names instead would need two
	// renames with a window between them in which the cert on disk does not
	// match the key, and an agent that restarted there would be permanently
	// unable to build a TLS keypair: an outage needing manual re-enrollment on
	// every affected host.
	altCertFile = "agent-cert.1.pem"
	altKeyFile  = "agent-key.1.pem"
)

// Identity is an enrolled agent's persisted credentials and coordinates.
type Identity struct {
	ServerID string
	NATSURL  string
	// PlaneAddr is the host:port the agent enrolled against — also the image
	// relay endpoint (builder-role-and-relay.md §3) and where renewal is
	// requested. Empty on identities saved before it existed;
	// CYPHER_PLANE_ADDR overrides at run.
	PlaneAddr string
	CertPEM   []byte
	KeyPEM    []byte
	CACertPEM []byte
}

type meta struct {
	ServerID  string `json:"server_id"`
	NATSURL   string `json:"nats_url"`
	PlaneAddr string `json:"plane_addr,omitempty"`
	// CertFile/KeyFile name the material currently in use. Empty — every
	// identity written before renewal existed — means the original names, so an
	// agent upgraded in place keeps working with no migration step.
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
}

func (m meta) files() (cert, key string) {
	cert, key = m.CertFile, m.KeyFile
	if cert == "" {
		cert = certFile
	}
	if key == "" {
		key = keyFile
	}
	return cert, key
}

// altOf returns the slot NOT currently in use — where a renewal writes.
func altOf(cert, key string) (string, string) {
	if cert == certFile && key == keyFile {
		return altCertFile, altKeyFile
	}
	return certFile, keyFile
}

// Save writes the identity to dir, keeping the private key readable only by the
// owner. It resets the material to the primary slot: enrollment is a fresh
// start, so any half-written renewal from a previous life is discarded.
func Save(dir string, id *Identity) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identity: creating state dir: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, keyFile), id.KeyPEM, 0o600); err != nil {
		return fmt.Errorf("identity: writing key: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, certFile), id.CertPEM, 0o644); err != nil {
		return fmt.Errorf("identity: writing cert: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, caFile), id.CACertPEM, 0o644); err != nil {
		return fmt.Errorf("identity: writing ca: %w", err)
	}
	if err := writeMeta(dir, meta{
		ServerID:  id.ServerID,
		NATSURL:   id.NATSURL,
		PlaneAddr: id.PlaneAddr,
		CertFile:  certFile,
		KeyFile:   keyFile,
	}); err != nil {
		return err
	}
	// Best effort: a leftover alternate slot is unreferenced, but leaving key
	// material lying around that nothing will ever use again is untidy at best.
	_ = os.Remove(filepath.Join(dir, altKeyFile))
	_ = os.Remove(filepath.Join(dir, altCertFile))
	return nil
}

// Rotate installs freshly-signed material for the identity already in dir. The
// server id, plane coordinates and pinned CA are unchanged by a renewal — only
// the certificate and its key are new.
//
// The write is atomic in the only sense that matters to a process that may be
// killed at any instant: at every moment, the pair identity.json points at is a
// complete, matching pair that exists on disk.
func Rotate(dir string, certPEM, keyPEM []byte) error {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return fmt.Errorf("identity: rotate needs both a certificate and a key")
	}
	m, err := readMeta(dir)
	if err != nil {
		return err
	}
	curCert, curKey := m.files()
	newCert, newKey := altOf(curCert, curKey)

	// Written into the free slot, so nothing readable is destroyed if this
	// fails halfway.
	if err := writeFileAtomic(filepath.Join(dir, newKey), keyPEM, 0o600); err != nil {
		return fmt.Errorf("identity: writing renewed key: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, newCert), certPEM, 0o644); err != nil {
		return fmt.Errorf("identity: writing renewed cert: %w", err)
	}

	m.CertFile, m.KeyFile = newCert, newKey
	if err := writeMeta(dir, m); err != nil { // the commit
		return err
	}

	// Past the commit the old slot is unreferenced. Removing it is tidiness,
	// not correctness: the next rotation overwrites it anyway.
	_ = os.Remove(filepath.Join(dir, curKey))
	_ = os.Remove(filepath.Join(dir, curCert))
	return nil
}

// Load reads a previously-saved identity from dir.
func Load(dir string) (*Identity, error) {
	m, err := readMeta(dir)
	if err != nil {
		return nil, err
	}
	certName, keyName := m.files()
	cert, err := os.ReadFile(filepath.Join(dir, certName))
	if err != nil {
		return nil, fmt.Errorf("identity: reading cert: %w", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, keyName))
	if err != nil {
		return nil, fmt.Errorf("identity: reading key: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, caFile))
	if err != nil {
		return nil, fmt.Errorf("identity: reading ca: %w", err)
	}
	return &Identity{
		ServerID:  m.ServerID,
		NATSURL:   m.NATSURL,
		PlaneAddr: m.PlaneAddr,
		CertPEM:   cert,
		KeyPEM:    key,
		CACertPEM: ca,
	}, nil
}

// Exists reports whether an identity has already been saved to dir.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, metaFile))
	return err == nil
}

func readMeta(dir string) (meta, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return meta{}, fmt.Errorf("identity: reading meta (run `cypher-agent enroll` first?): %w", err)
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return meta{}, fmt.Errorf("identity: parsing meta: %w", err)
	}
	return m, nil
}

func writeMeta(dir string, m meta) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("identity: marshaling meta: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, metaFile), raw, 0o644); err != nil {
		return fmt.Errorf("identity: writing meta: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a temporary file in the same directory, syncs
// it, and renames it over path. The sync matters here more than in most places:
// a rename that lands before the data does would leave identity.json pointing
// at an empty certificate after a power loss.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// os.OpenFile honours the umask; the key's 0600 must not depend on it.
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
