package logdrain

// CRUD, sealing and validation for log drains (log-drains.md §§3, 9).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Sealer seals a drain's config at rest (consumer-defined; *secret.Box
// satisfies it).
type Sealer interface {
	Seal(plaintext []byte) (ct, nonce []byte, err error)
}

// ServiceStore is the CRUD surface.
type ServiceStore interface {
	CreateLogDrain(ctx context.Context, d domain.LogDrain) (domain.LogDrain, error)
	GetLogDrain(ctx context.Context, id string) (domain.LogDrain, error)
	ListLogDrains(ctx context.Context) ([]domain.LogDrain, error)
	UpdateLogDrain(ctx context.Context, d domain.LogDrain) (domain.LogDrain, error)
	SetLogDrainEnabled(ctx context.Context, id string, enabled bool) (domain.LogDrain, error)
	DeleteLogDrain(ctx context.Context, id string) error
}

// Forgetter drops a deleted drain's durable consumer, so the cursor does not
// outlive the drain and hold the stream's ack floor down forever.
type Forgetter interface {
	Forget(ctx context.Context, drainID string)
}

// Service is the CRUD half.
type Service struct {
	store  ServiceStore
	sealer Sealer
	opener Opener
	mgr    Forgetter
}

func NewService(store ServiceStore, sealer Sealer, opener Opener) *Service {
	return &Service{store: store, sealer: sealer, opener: opener}
}

// WatchManager attaches the reconciler, so a deleted drain's consumer goes with
// it. Kept out of NewService so the CRUD half is testable alone.
func (s *Service) WatchManager(m Forgetter) { s.mgr = m }

var drainName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// CreateInput is a drain create request. Config is the raw channel config; it
// is sealed WHOLE before storage, because which half of a Loki config is a
// secret changes per deployment.
type CreateInput struct {
	Name      string
	Kind      string
	ProjectID string
	TargetID  string
	Config    json.RawMessage
	Enabled   bool
}

func (s *Service) validate(in CreateInput) error {
	if !drainName.MatchString(in.Name) {
		return invalid("the name is 3–40 lowercase letters, digits and dashes")
	}
	switch in.Kind {
	case domain.DrainLoki, domain.DrainSyslog:
		if in.TargetID != "" {
			return invalid("only an s3 drain names a backup target")
		}
	case domain.DrainS3:
		if in.TargetID == "" {
			// A second sealed S3 credential inside the config would mean
			// rotating a key in two places, and finding the second one at 02:00
			// on the night the batch fails.
			return invalid("an s3 drain writes to an existing backup target — pick one")
		}
	default:
		return invalid("the kind is one of loki, syslog, s3")
	}
	// The config has to parse as the kind claims, here rather than at the first
	// batch: a drain that only reveals a typo when a line arrives is a drain
	// that looks configured and is not.
	if _, err := defaultSink(domain.LogDrain{Kind: in.Kind, TargetID: in.TargetID},
		in.Config, domain.BackupTarget{ID: in.TargetID}); err != nil {
		return invalid(strings.TrimPrefix(err.Error(), "logdrain: "))
	}
	return nil
}

// validateWithoutConfig is validate minus the config parse, for an update that
// keeps the stored endpoint. Everything else still applies: a rename must not
// be able to move an s3 drain off its backup target.
func (s *Service) validateWithoutConfig(in CreateInput) error {
	if !drainName.MatchString(in.Name) {
		return invalid("the name is 3–40 lowercase letters, digits and dashes")
	}
	switch in.Kind {
	case domain.DrainLoki, domain.DrainSyslog:
		if in.TargetID != "" {
			return invalid("only an s3 drain names a backup target")
		}
	case domain.DrainS3:
		if in.TargetID == "" {
			return invalid("an s3 drain writes to an existing backup target — pick one")
		}
	default:
		return invalid("the kind is one of loki, syslog, s3")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in CreateInput) (domain.LogDrain, error) {
	if err := s.validate(in); err != nil {
		return domain.LogDrain{}, err
	}
	ct, nonce, err := s.sealer.Seal(in.Config)
	if err != nil {
		return domain.LogDrain{}, fmt.Errorf("logdrain: sealing the config: %w", err)
	}
	return s.store.CreateLogDrain(ctx, domain.LogDrain{
		ID: ids.New(ids.PrefixLogDrain), Name: in.Name, Kind: in.Kind,
		ProjectID: in.ProjectID, TargetID: in.TargetID,
		ConfigCT: ct, ConfigNonce: nonce, Enabled: in.Enabled,
	})
}

// Update replaces the config wholesale. There is no partial update: a config is
// one document, and half-changing a Loki endpoint's URL and its headers would
// leave a drain pointed somewhere with credentials for somewhere else.
func (s *Service) Update(ctx context.Context, id string, in CreateInput) (domain.LogDrain, error) {
	existing, err := s.store.GetLogDrain(ctx, id)
	if err != nil {
		return domain.LogDrain{}, err
	}
	in.Kind = existing.Kind // the kind is the drain's identity, not a field
	// AN EMPTY CONFIG KEEPS THE STORED ONE.
	//
	// A drain's endpoint is never read back — `ConfigHint` masks a Loki URL's
	// path deliberately, because it can carry a tenant — so a screen that lets
	// somebody rename a drain cannot show them the endpoint to resubmit with
	// it. Without this, editing the name would blank the destination.
	//
	// It is the same rule the panel's SMTP password already follows: the field
	// you cannot be shown is the field that empty means "leave alone".
	ct, nonce := existing.ConfigCT, existing.ConfigNonce
	if replacing := len(in.Config) > 0 && string(in.Config) != "{}" && string(in.Config) != "null"; replacing {
		if err := s.validate(in); err != nil {
			return domain.LogDrain{}, err
		}
		sealedCT, sealedNonce, err := s.sealer.Seal(in.Config)
		if err != nil {
			return domain.LogDrain{}, fmt.Errorf("logdrain: sealing the config: %w", err)
		}
		ct, nonce = sealedCT, sealedNonce
	} else if err := s.validateWithoutConfig(in); err != nil {
		return domain.LogDrain{}, err
	}
	return s.store.UpdateLogDrain(ctx, domain.LogDrain{
		ID: id, Name: in.Name, ProjectID: in.ProjectID, TargetID: in.TargetID,
		ConfigCT: ct, ConfigNonce: nonce, Enabled: in.Enabled,
	})
}

func (s *Service) Get(ctx context.Context, id string) (domain.LogDrain, error) {
	return s.store.GetLogDrain(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]domain.LogDrain, error) {
	return s.store.ListLogDrains(ctx)
}

func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) (domain.LogDrain, error) {
	return s.store.SetLogDrainEnabled(ctx, id, enabled)
}

// Delete drops the drain AND its cursor. Leaving the durable consumer behind
// would hold the stream's ack floor at the drain's last position forever, which
// is a retention window that stops sliding — the disk fill, arrived at by a
// different road.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.store.DeleteLogDrain(ctx, id); err != nil {
		return err
	}
	if s.mgr != nil {
		s.mgr.Forget(ctx, id)
	}
	return nil
}

// Hint masks a drain's config for the API by unsealing in-process and
// discarding the plaintext.
func (s *Service) Hint(d domain.LogDrain) string {
	cfg, err := s.opener.Open(d.ConfigCT, d.ConfigNonce)
	if err != nil {
		return d.Kind
	}
	return ConfigHint(d.Kind, cfg)
}
