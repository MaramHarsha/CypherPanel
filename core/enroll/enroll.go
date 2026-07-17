// Package enroll implements agent enrollment: the moment a new server presents
// a join token and a CSR and receives its mTLS identity (ADR-002).
//
// Security properties enforced here (threat-model §5.3):
//   - The token secret is compared in constant time against a stored hash.
//   - The token is consumed atomically (single-use) and only after the secret
//     verifies, so a wrong guess never burns a valid token.
//   - The plane signs the CSR's public key but sets the certificate identity
//     itself (CN = server ID); the agent's private key never crosses the wire.
package enroll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
)

// ErrInvalidToken is returned for any bad, expired, or already-used token. It
// is deliberately undifferentiated so enrollment failures leak nothing about
// which tokens exist.
var ErrInvalidToken = errors.New("enroll: invalid or expired join token")

// Store is the persistence the enrollment service needs (consumer-defined).
type Store interface {
	GetJoinToken(ctx context.Context, id string) (domain.JoinToken, error)
	ConsumeJoinToken(ctx context.Context, id string) (domain.JoinToken, error)
	MarkServerEnrolled(ctx context.Context, id, hostname, agentVersion string) (domain.Server, error)
}

// Result is what a successful enrollment yields to the agent.
type Result struct {
	ServerID  string
	CertPEM   []byte
	CACertPEM []byte
	NATSURL   string
}

// Service verifies join tokens and issues agent certificates.
type Service struct {
	store   Store
	ca      *pki.CA
	certTTL time.Duration
	natsURL string
	now     func() time.Time
}

// NewService wires the enrollment service. certTTL is the lifetime of issued
// agent certs; natsURL is the data-plane address handed back to the agent.
func NewService(s Store, ca *pki.CA, certTTL time.Duration, natsURL string) *Service {
	return &Service{store: s, ca: ca, certTTL: certTTL, natsURL: natsURL, now: time.Now}
}

// Enroll validates rawToken and csrPEM and, on success, returns a signed client
// certificate and the data-plane address. It is safe to call concurrently: at
// most one caller can consume a given token.
func (s *Service) Enroll(ctx context.Context, rawToken string, csrPEM []byte, hostname, agentVersion string) (Result, error) {
	tokenID, tokenSecret, ok := splitToken(rawToken)
	if !ok {
		return Result{}, ErrInvalidToken
	}

	tok, err := s.store.GetJoinToken(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, ErrInvalidToken
		}
		return Result{}, fmt.Errorf("enroll: reading token: %w", err)
	}

	// Verify the secret in constant time BEFORE consuming, so a wrong guess
	// against a real token id does not burn the token (ENGINEERING rule 21).
	if !auth.ConstantTimeEqual(auth.HashToken(tokenSecret), tok.TokenHash) {
		return Result{}, ErrInvalidToken
	}
	if tok.ConsumedAt != nil || !s.now().Before(tok.ExpiresAt) {
		return Result{}, ErrInvalidToken
	}

	// Atomic single-use consumption is the authoritative guard against races.
	consumed, err := s.store.ConsumeJoinToken(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Result{}, ErrInvalidToken
		}
		return Result{}, fmt.Errorf("enroll: consuming token: %w", err)
	}

	certPEM, err := s.ca.SignAgentCSR(csrPEM, consumed.ServerID, s.certTTL, s.now())
	if err != nil {
		// A malformed CSR is a client error, but the token is already spent; the
		// operator regenerates a join command. Report distinctly from bad tokens.
		return Result{}, fmt.Errorf("enroll: signing certificate: %w", err)
	}
	if _, err := s.store.MarkServerEnrolled(ctx, consumed.ServerID, hostname, agentVersion); err != nil {
		return Result{}, fmt.Errorf("enroll: marking server enrolled: %w", err)
	}

	return Result{
		ServerID:  consumed.ServerID,
		CertPEM:   certPEM,
		CACertPEM: s.ca.CertPEM(),
		NATSURL:   s.natsURL,
	}, nil
}

// FormatToken renders the wire form of a join token: "<id>.<secret>". The id is
// the public lookup key; the secret is compared against a stored hash.
func FormatToken(id, secret string) string { return id + "." + secret }

func splitToken(raw string) (id, secret string, ok bool) {
	id, secret, found := strings.Cut(raw, ".")
	if !found || id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}
