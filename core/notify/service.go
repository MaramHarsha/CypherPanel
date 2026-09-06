package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	netmail "net/mail"
	"net/url"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrProjectNotFound is returned when the addressed project does not exist —
// distinct from store.ErrNotFound (which, from this service, means the notifier
// is missing).
var ErrProjectNotFound = errors.New("notify: project not found")

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Sealer seals channel config at rest (consumer-defined; *secret.Box satisfies
// it).
type Sealer interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
}

// NotifierStore is the persistence the CRUD service needs (consumer-defined).
type NotifierStore interface {
	GetProject(ctx context.Context, id string) (domain.Project, error)
	CreateNotifier(ctx context.Context, n domain.Notifier) (domain.Notifier, error)
	GetNotifier(ctx context.Context, id string) (domain.Notifier, error)
	ListNotifiersByProject(ctx context.Context, projectID string) ([]domain.Notifier, error)
	UpdateNotifier(ctx context.Context, n domain.Notifier) (domain.Notifier, error)
	DeleteNotifier(ctx context.Context, id string) error
}

// Service is the notifier CRUD surface: validates, seals channel config, and
// masks it out of responses (rule 20).
type Service struct {
	store  NotifierStore
	sealer Sealer
}

// NewService wires the notifier CRUD service.
func NewService(st NotifierStore, sealer Sealer) *Service {
	return &Service{store: st, sealer: sealer}
}

// CreateInput is a notifier create request. Config is the raw channel config
// JSON (shape per channel, notifications.md §2); it is validated, canonicalised,
// and sealed before storage.
type CreateInput struct {
	Name    string
	Channel string
	Config  json.RawMessage
	Events  []string
	Enabled bool
}

// Create validates and stores a notifier under a project.
func (s *Service) Create(ctx context.Context, projectID string, in CreateInput) (domain.Notifier, error) {
	if _, err := s.store.GetProject(ctx, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Notifier{}, ErrProjectNotFound
		}
		return domain.Notifier{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	events, err := validateMeta(in.Name, in.Channel, in.Events)
	if err != nil {
		return domain.Notifier{}, err
	}
	canonical, err := validateConfig(in.Channel, in.Config)
	if err != nil {
		return domain.Notifier{}, err
	}
	ct, nonce, err := s.sealer.Seal(canonical)
	if err != nil {
		return domain.Notifier{}, fmt.Errorf("notify: sealing config: %w", err)
	}
	return s.store.CreateNotifier(ctx, domain.Notifier{
		ID:          ids.New(ids.PrefixNotifier),
		ProjectID:   projectID,
		Name:        in.Name,
		Channel:     in.Channel,
		ConfigCT:    ct,
		ConfigNonce: nonce,
		Events:      events,
		Enabled:     in.Enabled,
	})
}

// UpdateInput carries the mutable fields. Config is optional: nil keeps the
// sealed value, a value reseals wholesale (no partial-secret merge, spec §7).
type UpdateInput struct {
	Name    string
	Events  []string
	Enabled bool
	Config  json.RawMessage
}

// Update mutates a notifier's name/events/enabled and, when Config is supplied,
// reseals the channel config.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (domain.Notifier, error) {
	cur, err := s.store.GetNotifier(ctx, id)
	if err != nil {
		return domain.Notifier{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	events, err := validateMeta(in.Name, cur.Channel, in.Events)
	if err != nil {
		return domain.Notifier{}, err
	}
	cur.Name = in.Name
	cur.Events = events
	cur.Enabled = in.Enabled
	if len(in.Config) > 0 {
		canonical, err := validateConfig(cur.Channel, in.Config)
		if err != nil {
			return domain.Notifier{}, err
		}
		ct, nonce, err := s.sealer.Seal(canonical)
		if err != nil {
			return domain.Notifier{}, fmt.Errorf("notify: sealing config: %w", err)
		}
		cur.ConfigCT, cur.ConfigNonce = ct, nonce
	}
	return s.store.UpdateNotifier(ctx, cur)
}

// Get returns one notifier; the error wraps store.ErrNotFound when absent.
func (s *Service) Get(ctx context.Context, id string) (domain.Notifier, error) {
	return s.store.GetNotifier(ctx, id)
}

// List returns a project's notifiers.
func (s *Service) List(ctx context.Context, projectID string) ([]domain.Notifier, error) {
	return s.store.ListNotifiersByProject(ctx, projectID)
}

// Delete removes a notifier.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteNotifier(ctx, id)
}

// validateMeta checks name, channel, and the event selection, returning the
// deduplicated events.
func validateMeta(name, channel string, events []string) ([]string, error) {
	if name == "" {
		return nil, invalid("name is required")
	}
	switch channel {
	case domain.NotifyChannelEmail, domain.NotifyChannelDiscord, domain.NotifyChannelSlack, domain.NotifyChannelTelegram:
	default:
		return nil, invalid("channel must be one of email, discord, slack, telegram")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(events))
	for _, e := range events {
		// One vocabulary in one place: the domain taxonomy notifiers, webhook
		// endpoints and the inbox all validate against (notifications.md §3).
		if !domain.ValidEventType(e) {
			return nil, invalid("unknown event: " + e)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, invalid("at least one event is required")
	}
	return out, nil
}

// validateConfig checks the channel config and returns its canonical JSON (only
// known fields, so nothing extra is sealed).
func validateConfig(channel string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, invalid("config is required")
	}
	switch channel {
	case domain.NotifyChannelEmail:
		var c emailConfig
		if err := strictUnmarshal(raw, &c); err != nil {
			return nil, invalid("invalid email config: " + err.Error())
		}
		if c.SMTPHost == "" || c.SMTPPort <= 0 {
			return nil, invalid("email requires smtp_host and a positive smtp_port")
		}
		if c.From == "" || strings.TrimSpace(c.To) == "" {
			return nil, invalid("email requires from and to")
		}
		// Parsed, not merely non-empty. A well-formed address cannot contain a
		// line break, so this closes header injection structurally rather than
		// relying on scrubbing at send time (buildMessage still sanitises —
		// belt and braces is cheap on a header).
		if _, err := netmail.ParseAddress(c.From); err != nil {
			return nil, invalid(fmt.Sprintf("%q is not a valid email address", c.From))
		}
		recipients := splitRecipients(c.To)
		if len(recipients) == 0 {
			return nil, invalid("email requires at least one recipient")
		}
		for _, r := range recipients {
			if _, err := netmail.ParseAddress(r); err != nil {
				return nil, invalid(fmt.Sprintf("%q is not a valid email address", r))
			}
		}
		return json.Marshal(c)
	case domain.NotifyChannelDiscord, domain.NotifyChannelSlack:
		var c webhookConfig
		if err := strictUnmarshal(raw, &c); err != nil {
			return nil, invalid("invalid webhook config: " + err.Error())
		}
		if err := validateHTTPURL(c.WebhookURL); err != nil {
			return nil, err
		}
		return json.Marshal(c)
	case domain.NotifyChannelTelegram:
		var c telegramConfig
		if err := strictUnmarshal(raw, &c); err != nil {
			return nil, invalid("invalid telegram config: " + err.Error())
		}
		if c.BotToken == "" || c.ChatID == "" {
			return nil, invalid("telegram requires bot_token and chat_id")
		}
		return json.Marshal(c)
	default:
		return nil, invalid("channel must be one of email, discord, slack, telegram")
	}
}

// strictUnmarshal rejects unknown fields so a typo in a config key is a 400,
// not a silently-dropped setting.
func strictUnmarshal(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// validateHTTPURL requires an http(s) webhook endpoint (spec §6): no other
// scheme (file://, gopher://) is accepted.
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("webhook_url must be an http(s) URL")
	}
	return nil
}

// ConfigHint returns a masked, channel-appropriate summary of a notifier's
// config for API responses — enough to tell notifiers apart, never the secret
// (spec §7). It never unseals: the hint is derived from non-secret fields only,
// so it needs the unsealed config passed in by the caller that already has it,
// or falls back to the channel name. Here we take the unsealed config bytes.
func ConfigHint(channel string, cfg []byte) string {
	switch channel {
	case domain.NotifyChannelEmail:
		var c emailConfig
		_ = json.Unmarshal(cfg, &c)
		return fmt.Sprintf("%s → %s", c.SMTPHost, c.To)
	case domain.NotifyChannelDiscord, domain.NotifyChannelSlack:
		var c webhookConfig
		_ = json.Unmarshal(cfg, &c)
		return "webhook •••" + tail(c.WebhookURL, 4)
	case domain.NotifyChannelTelegram:
		var c telegramConfig
		_ = json.Unmarshal(cfg, &c)
		return "chat " + c.ChatID
	default:
		return channel
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
