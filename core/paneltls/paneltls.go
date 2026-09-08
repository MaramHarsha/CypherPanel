// Package paneltls owns the panel's ACME account — the one setting that decides
// whether the managed Proxy on every node can obtain certificates
// (docs/features/agent-identity-and-tls.md §4).
//
// It exists because the alternative was a lie: the agent only creates Traefik's
// Let's Encrypt resolver when CYPHER_ACME_EMAIL is set on that host, nothing in
// the join path ever set it, and the proxy wrote `certResolver: le` onto every
// https route regardless — so a fresh install pointed every route at a resolver
// that did not exist. Making the account desired state (ADR-005) means one
// action in one place configures the whole fleet, and every node's Proxy can be
// honest about what it is actually serving.
package paneltls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// Sentinel errors (ENGINEERING rule 3).
var (
	// ErrInvalidEmail: the ACME account email is not an address the CA will
	// accept. Refused before it is stored, because the failure would otherwise
	// surface hours later as an unexplained issuance error on one node.
	ErrInvalidEmail = errors.New("paneltls: acme_email must be a single valid email address")
	// ErrInvalidCAServer: the ACME directory URL is not an absolute http(s) URL.
	ErrInvalidCAServer = errors.New("paneltls: acme_ca_server must be an absolute http(s) URL")
)

// Store is the persistence this service needs (consumer-defined, rule 6).
// *store.Store satisfies it.
type Store interface {
	GetPanelTLS(ctx context.Context) (domain.PanelTLS, error)
	SetPanelTLS(ctx context.Context, t domain.PanelTLS) error
	DeletePanelTLS(ctx context.Context) error
}

// Fleet carries a settings change to the nodes (consumer-defined;
// *scheduler.Scheduler satisfies it). Without it a change would only reach an
// agent on its next reconnect, which can be days: the setting is desired state,
// so the fleet is nudged to re-read it. Nil is allowed — the setting still
// lands, it just propagates lazily.
type Fleet interface {
	// RequestResync asks every enrolled server to re-read its desired state.
	RequestResync(ctx context.Context, reason string) error
}

// Service reads and writes the panel's ACME account.
type Service struct {
	store Store
	fleet Fleet
	log   *slog.Logger
}

// NewService wires the service. fleet may be nil (see Fleet).
func NewService(s Store, fleet Fleet, log *slog.Logger) *Service {
	return &Service{store: s, fleet: fleet, log: log}
}

// Input is a settings write. An empty ACMEEmail clears the account.
type Input struct {
	ACMEEmail    string
	ACMECAServer string
}

// Get returns the current account. An unset account is not an error — it is the
// default state of a fresh panel — so it comes back as a zero PanelTLS.
func (s *Service) Get(ctx context.Context) (domain.PanelTLS, error) {
	t, err := s.store.GetPanelTLS(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return domain.PanelTLS{}, nil
	}
	if err != nil {
		return domain.PanelTLS{}, fmt.Errorf("paneltls: reading settings: %w", err)
	}
	return t, nil
}

// Configured answers the one question the rest of the panel asks: is there an
// ACME account, and therefore a certificate resolver on the nodes? A read
// failure is reported, never silently answered "no" — claiming HTTPS works when
// the database is unreachable is exactly the false certainty this feature
// removes.
func (s *Service) Configured(ctx context.Context) (bool, error) {
	t, err := s.Get(ctx)
	if err != nil {
		return false, err
	}
	return t.Configured(), nil
}

// Set validates and stores the account, then nudges the fleet to re-read its
// desired state. An empty email clears the account (there is no separate
// delete: "no email" and "no ACME" are the same statement).
func (s *Service) Set(ctx context.Context, in Input) (domain.PanelTLS, error) {
	in.ACMEEmail = strings.TrimSpace(in.ACMEEmail)
	in.ACMECAServer = strings.TrimSpace(in.ACMECAServer)

	if in.ACMEEmail == "" {
		if err := s.store.DeletePanelTLS(ctx); err != nil {
			return domain.PanelTLS{}, fmt.Errorf("paneltls: clearing settings: %w", err)
		}
		s.log.Info("panel tls cleared: no ACME account, nodes will serve routed apps over HTTP")
		s.notifyFleet(ctx, "panel tls cleared")
		return domain.PanelTLS{}, nil
	}
	if err := validateEmail(in.ACMEEmail); err != nil {
		return domain.PanelTLS{}, err
	}
	if err := validateCAServer(in.ACMECAServer); err != nil {
		return domain.PanelTLS{}, err
	}

	t := domain.PanelTLS{ACMEEmail: in.ACMEEmail, ACMECAServer: in.ACMECAServer}
	if err := s.store.SetPanelTLS(ctx, t); err != nil {
		return domain.PanelTLS{}, fmt.Errorf("paneltls: saving settings: %w", err)
	}
	saved, err := s.store.GetPanelTLS(ctx)
	if err != nil {
		return domain.PanelTLS{}, fmt.Errorf("paneltls: re-reading settings: %w", err)
	}
	// The email is not a secret (it ends up in the CA's account record), so
	// logging it is deliberate: an operator debugging issuance needs to see
	// which account the fleet was told to use.
	s.log.Info("panel tls set", "acme_email", saved.ACMEEmail, "acme_ca_server", saved.ACMECAServer)
	s.notifyFleet(ctx, "panel tls changed")
	return saved, nil
}

// notifyFleet is best-effort by design: the settings are already stored, which
// is what makes them true (rule 15). A failed nudge costs propagation latency —
// every agent re-reads desired state on its next connect — never correctness,
// so it is logged rather than returned as a failed write.
func (s *Service) notifyFleet(ctx context.Context, reason string) {
	if s.fleet == nil {
		return
	}
	if err := s.fleet.RequestResync(ctx, reason); err != nil {
		s.log.Warn("panel tls: could not nudge the fleet to re-read desired state; agents pick it up on their next connect",
			"reason", reason, "error", err)
	}
}

func validateEmail(addr string) error {
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Address != addr || !strings.Contains(addr, "@") {
		// Only the bare form is accepted: Let's Encrypt registers the address,
		// not a display name, and "Ops <ops@x>" would be stored as something
		// the CA rejects at registration time on some other host.
		return ErrInvalidEmail
	}
	return nil
}

func validateCAServer(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrInvalidCAServer
	}
	return nil
}
