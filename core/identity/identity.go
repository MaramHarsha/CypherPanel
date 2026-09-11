// Package identity bootstraps the control plane's cryptographic identity: its
// certificate authority (asset A2, threat-model §5.1). On first boot it creates
// the CA and persists it with the private key sealed at rest; on later boots it
// loads and decrypts it. The CA is then shared by the enrollment service (signs
// agent certs) and the embedded NATS listener (its own server cert).
package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/secret"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
)

// CAStore is the persistence CA bootstrap needs (consumer-defined).
type CAStore interface {
	GetPlaneCA(ctx context.Context) (store.PlaneCA, error)
	InsertPlaneCA(ctx context.Context, ca store.PlaneCA) error
}

// LoadOrCreateCA returns the control-plane CA, generating and persisting it on
// first boot. The private key is sealed with box before storage; only the
// public certificate is stored in the clear.
func LoadOrCreateCA(ctx context.Context, s CAStore, box *secret.Box, now time.Time) (*pki.CA, error) {
	stored, err := s.GetPlaneCA(ctx)
	switch {
	case err == nil:
		keyPEM, derr := box.Open(stored.EncryptedKey, stored.KeyNonce)
		if derr != nil {
			return nil, fmt.Errorf("identity: decrypting CA key (wrong master key?): %w", derr)
		}
		ca, lerr := pki.Load(stored.CertPEM, keyPEM)
		if lerr != nil {
			return nil, fmt.Errorf("identity: loading CA: %w", lerr)
		}
		return ca, nil
	case errors.Is(err, store.ErrNotFound):
		return createAndPersistCA(ctx, s, box, now)
	default:
		return nil, fmt.Errorf("identity: reading CA: %w", err)
	}
}

func createAndPersistCA(ctx context.Context, s CAStore, box *secret.Box, now time.Time) (*pki.CA, error) {
	ca, err := pki.NewCA(now)
	if err != nil {
		return nil, fmt.Errorf("identity: creating CA: %w", err)
	}
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		return nil, fmt.Errorf("identity: marshaling CA key: %w", err)
	}
	enc, nonce, err := box.Seal(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("identity: sealing CA key: %w", err)
	}
	if err := s.InsertPlaneCA(ctx, store.PlaneCA{
		CertPEM:      ca.CertPEM(),
		EncryptedKey: enc,
		KeyNonce:     nonce,
	}); err != nil {
		return nil, fmt.Errorf("identity: persisting CA: %w", err)
	}
	return ca, nil
}
