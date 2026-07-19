// Package identity persists an enrolled agent's credentials on the server it
// runs on: the mTLS client certificate, the private key (which never left this
// host — threat-model §5.1), the pinned CA, and the coordinates the agent needs
// to reach the control plane.
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
)

// Identity is an enrolled agent's persisted credentials and coordinates.
type Identity struct {
	ServerID string
	NATSURL  string
	// PlaneAddr is the host:port the agent enrolled against — also the image
	// relay endpoint (builder-role-and-relay.md §3). Empty on identities
	// saved before it existed; CYPHER_PLANE_ADDR overrides at run.
	PlaneAddr string
	CertPEM   []byte
	KeyPEM    []byte
	CACertPEM []byte
}

type meta struct {
	ServerID  string `json:"server_id"`
	NATSURL   string `json:"nats_url"`
	PlaneAddr string `json:"plane_addr,omitempty"`
}

// Save writes the identity to dir, keeping the private key readable only by the
// owner.
func Save(dir string, id *Identity) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identity: creating state dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, keyFile), id.KeyPEM, 0o600); err != nil {
		return fmt.Errorf("identity: writing key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, certFile), id.CertPEM, 0o644); err != nil {
		return fmt.Errorf("identity: writing cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, caFile), id.CACertPEM, 0o644); err != nil {
		return fmt.Errorf("identity: writing ca: %w", err)
	}
	m, err := json.Marshal(meta{ServerID: id.ServerID, NATSURL: id.NATSURL, PlaneAddr: id.PlaneAddr})
	if err != nil {
		return fmt.Errorf("identity: marshaling meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, metaFile), m, 0o644); err != nil {
		return fmt.Errorf("identity: writing meta: %w", err)
	}
	return nil
}

// Load reads a previously-saved identity from dir.
func Load(dir string) (*Identity, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil, fmt.Errorf("identity: reading meta (run `cypher-agent enroll` first?): %w", err)
	}
	var mm meta
	if err := json.Unmarshal(raw, &mm); err != nil {
		return nil, fmt.Errorf("identity: parsing meta: %w", err)
	}
	cert, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		return nil, fmt.Errorf("identity: reading cert: %w", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, fmt.Errorf("identity: reading key: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(dir, caFile))
	if err != nil {
		return nil, fmt.Errorf("identity: reading ca: %w", err)
	}
	return &Identity{
		ServerID:  mm.ServerID,
		NATSURL:   mm.NATSURL,
		PlaneAddr: mm.PlaneAddr,
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
