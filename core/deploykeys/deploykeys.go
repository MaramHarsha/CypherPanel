// Package deploykeys manages SSH deploy keys for private Git repositories.
// Key material is generated server-side (Ed25519) and sealed at rest.
// The private key is never returned in API responses.
package deploykeys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Store defines persistence for deploy keys.
type Store interface {
	CreateDeployKey(ctx context.Context, dk domain.DeployKey) (domain.DeployKey, error)
	GetDeployKey(ctx context.Context, id string) (domain.DeployKey, error)
	ListDeployKeys(ctx context.Context) ([]domain.DeployKey, error)
	DeleteDeployKey(ctx context.Context, id string) error
}

// Sealer seals plaintext for storage at rest (e.g. secret.Box).
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// Service provides deploy key lifecycle operations.
type Service struct {
	store  Store
	sealer Sealer
}

// NewService wires the service.
func NewService(store Store, sealer Sealer) *Service {
	return &Service{store: store, sealer: sealer}
}

// ValidationError is returned for client-caused input errors.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "deploykeys: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// maxNameLen matches the CreateDeployKeyRequest schema in openapi.yaml — the
// spec is the source of truth (ENGINEERING rule 19).
const maxNameLen = 100

// Create generates a new Ed25519 SSH keypair, seals the private key, and stores it.
func (s *Service) Create(ctx context.Context, name string) (domain.DeployKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.DeployKey{}, invalid("name is required")
	}
	if len(name) > maxNameLen {
		return domain.DeployKey{}, invalid("name must be 100 characters or fewer")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.DeployKey{}, fmt.Errorf("deploykeys: generating keypair: %w", err)
	}

	privPEM, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return domain.DeployKey{}, fmt.Errorf("deploykeys: marshaling private key: %w", err)
	}
	privBytes := pem.EncodeToMemory(privPEM)

	pubKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		return domain.DeployKey{}, fmt.Errorf("deploykeys: marshaling public key: %w", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(pubKey)

	// SHA-256 fingerprint (OpenSSH standard).
	fingerprint := ssh.FingerprintSHA256(pubKey)

	ct, nonce, err := s.sealer.Seal(privBytes)
	if err != nil {
		return domain.DeployKey{}, fmt.Errorf("deploykeys: sealing private key: %w", err)
	}

	dk := domain.DeployKey{
		ID:              ids.New(ids.PrefixDeployKey),
		Name:            name,
		PublicKey:       strings.TrimSpace(string(pubBytes)),
		Fingerprint:     fingerprint,
		PrivateKeyCT:    ct,
		PrivateKeyNonce: nonce,
	}

	return s.store.CreateDeployKey(ctx, dk)
}

func (s *Service) Get(ctx context.Context, id string) (domain.DeployKey, error) {
	return s.store.GetDeployKey(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]domain.DeployKey, error) {
	return s.store.ListDeployKeys(ctx)
}

// Delete removes a deploy key. Deleting an unknown id reports the store's
// not-found error (the API contract promises 404); a key still referenced by
// an application surfaces the RESTRICT violation as store.ErrInUse.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.store.GetDeployKey(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteDeployKey(ctx, id)
}
