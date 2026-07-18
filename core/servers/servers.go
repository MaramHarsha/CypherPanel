// Package servers is the operator-facing server lifecycle: registering a server
// (which atomically issues its first join token), listing, fetching, and
// deleting. The agent-facing side of the same story is package enroll.
package servers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrInvalidName is returned when a server name is empty or too long.
var ErrInvalidName = errors.New("servers: name must be 1–100 characters")

// Store is the persistence the service needs (consumer-defined).
type Store interface {
	CreateServerWithToken(ctx context.Context, serverID, name, tokenID string, tokenHash []byte, tokenExpiresAt time.Time) (domain.Server, error)
	ListServers(ctx context.Context) ([]domain.Server, error)
	GetServer(ctx context.Context, id string) (domain.Server, error)
	DeleteServer(ctx context.Context, id string) error
}

// AgentBus is what the service needs from the bus (consumer-defined;
// *bus.Bus satisfies it): severing a deleted server's live connection —
// revocation must cut open connections too, not just future ones
// (threat-model §8 req 6) — and owning the lifecycle of the server's durable
// work consumer, which exists from the moment the server does so no work
// item published before the agent's first connect is ever lost.
type AgentBus interface {
	DisconnectAgent(serverID string) error
	EnsureWorkConsumer(ctx context.Context, serverID string) error
	DeleteWorkConsumer(ctx context.Context, serverID string) error
}

// Service manages servers from the operator's side.
type Service struct {
	store    Store
	bus      AgentBus
	tokenTTL time.Duration
	log      *slog.Logger
	now      func() time.Time
}

// NewService wires the service. tokenTTL is how long an issued join token stays
// valid (short — threat-model §5.3).
func NewService(s Store, bus AgentBus, tokenTTL time.Duration, log *slog.Logger) *Service {
	return &Service{store: s, bus: bus, tokenTTL: tokenTTL, log: log, now: time.Now}
}

// Create registers a new server and returns it with the raw join token to hand
// to the agent. The raw token is returned exactly once and is never stored (only
// its hash is), so it cannot be recovered later — the operator regenerates one
// if lost.
func (s *Service) Create(ctx context.Context, name string) (domain.Server, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.Server{}, "", ErrInvalidName
	}
	tokenID := ids.New(ids.PrefixJoinToken)
	tokenSecret := ids.Secret()
	srv, err := s.store.CreateServerWithToken(
		ctx,
		ids.New(ids.PrefixServer),
		name,
		tokenID,
		auth.HashToken(tokenSecret),
		s.now().Add(s.tokenTTL),
	)
	if err != nil {
		return domain.Server{}, "", fmt.Errorf("servers: creating server: %w", err)
	}
	if err := s.bus.EnsureWorkConsumer(ctx, srv.ID); err != nil {
		// The server exists and can enroll; the consumer is re-asserted on
		// every plane boot, so this is degraded, not fatal.
		s.log.Error("ensuring work consumer", "server_id", srv.ID, "error", err)
	}
	return srv, enroll.FormatToken(tokenID, tokenSecret), nil
}

// List returns all servers, newest first.
func (s *Service) List(ctx context.Context) ([]domain.Server, error) {
	list, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("servers: listing: %w", err)
	}
	return list, nil
}

// Get returns one server. The error wraps store.ErrNotFound when absent.
func (s *Service) Get(ctx context.Context, id string) (domain.Server, error) {
	srv, err := s.store.GetServer(ctx, id)
	if err != nil {
		return domain.Server{}, fmt.Errorf("servers: getting server: %w", err)
	}
	return srv, nil
}

// Delete removes a server and (by cascade) its tokens, then severs the
// server's live bus connection so revocation is immediate. The disconnect is
// best-effort once the row is gone: a failure is logged, not returned, because
// the bus refuses the identity on its next connect regardless (threat-model
// §8 req 6).
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.store.DeleteServer(ctx, id); err != nil {
		return fmt.Errorf("servers: deleting server: %w", err)
	}
	if err := s.bus.DisconnectAgent(id); err != nil {
		s.log.Error("disconnecting deleted server's agent", "server_id", id, "error", err)
	}
	if err := s.bus.DeleteWorkConsumer(ctx, id); err != nil {
		s.log.Error("deleting work consumer", "server_id", id, "error", err)
	}
	return nil
}
