// Package mail owns the panel's own outbound email — one SMTP transport, owned
// by the panel rather than by a project (docs/features/panel-mail.md).
//
// A Notifier tells people about a project's events; Panel Mail is how the panel
// speaks about itself: confirming an address change, warning the address being
// moved away from. The distinction matters because the two have different
// owners, different authorization, and different lifetimes.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// ValidationError marks input the caller can fix; REST maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrNotConfigured is returned when the panel has been asked to send but has no
// transport. Callers surface it as an instruction, not a failure — the operator
// has somewhere to go and something to do.
var ErrNotConfigured = errors.New("mail: the panel has no mail transport configured")

// Config is the panel's SMTP settings. Sealed as one blob, so the field names
// are the wire format and a typo must not silently become an empty setting.
type Config struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	// TLS is starttls (default), implicit or none — see core/notify. Stored so
	// the mode is the operator's decision rather than a guess from the port.
	TLS string `json:"tls,omitempty"`
}

// Store is the persistence this needs (consumer-defined, ENGINEERING rule 6).
type Store interface {
	GetPanelMail(ctx context.Context) (ct, nonce []byte, updatedAt time.Time, err error)
	SetPanelMail(ctx context.Context, ct, nonce []byte) error
	DeletePanelMail(ctx context.Context) error
}

// SecretBox seals and opens the configuration under the master key.
type SecretBox interface {
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	Open(ciphertext, nonce []byte) ([]byte, error)
}

type Service struct {
	store Store
	box   SecretBox
}

func New(st Store, box SecretBox) *Service { return &Service{store: st, box: box} }

// Settings is what the API may say about the configuration: whether one exists,
// a hint naming it, and every field that is not a credential.
//
// Reading the non-secret half back is what makes the settings screen editable:
// without it, changing a port means retyping the host, the username and the
// from address from memory, and an operator who mistypes one has silently
// reconfigured the panel. The password is the one field that never comes back —
// Configured plus Hint say that one is set without saying what it is.
type Settings struct {
	Configured bool
	Hint       string
	UpdatedAt  time.Time

	SMTPHost string
	SMTPPort int
	Username string
	From     string
	TLS      string
}

// Hint renders the non-secret half — "smtp.acme.com → ops@acme.com" — the same
// shape notifiers use, so an operator recognises what is saved without the
// panel ever reading a password back to them.
func Hint(c Config) string {
	host := c.SMTPHost
	if host == "" {
		host = "smtp"
	}
	if c.From == "" {
		return host
	}
	return host + " → " + c.From
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	c, updated, err := s.load(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	return settingsOf(c, updated), nil
}

// settingsOf projects a Config onto what the API may say about it. One place,
// so a field added to Config is either deliberately exposed here or deliberately
// not — never accidentally either way.
func settingsOf(c Config, updated time.Time) Settings {
	return Settings{
		Configured: true,
		Hint:       Hint(c),
		UpdatedAt:  updated,
		SMTPHost:   c.SMTPHost,
		SMTPPort:   c.SMTPPort,
		Username:   c.Username,
		From:       c.From,
		TLS:        tlsOrDefault(c.TLS),
	}
}

// tlsOrDefault names the mode a stored config actually uses. Rows sealed before
// the mode existed carry an empty string and are sent with STARTTLS, so that is
// what the settings screen must show for them.
func tlsOrDefault(v string) string {
	if v == "" {
		return notify.TLSStartTLS
	}
	return v
}

// Set replaces the configuration wholesale. There is no partial update: merging
// half a credential is the bug the sealed-blob convention exists to prevent.
func (s *Service) Set(ctx context.Context, c Config) (Settings, error) {
	c.SMTPHost = strings.TrimSpace(c.SMTPHost)
	c.Username = strings.TrimSpace(c.Username)
	c.From = strings.TrimSpace(c.From)

	if c.SMTPHost == "" {
		return Settings{}, invalid("an SMTP host is required")
	}
	if c.SMTPPort <= 0 || c.SMTPPort > 65535 {
		return Settings{}, invalid("the SMTP port must be between 1 and 65535")
	}
	if c.From == "" {
		return Settings{}, invalid("a from address is required — it is what recipients will reply to")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return Settings{}, invalid(fmt.Sprintf("%q is not a valid email address", c.From))
	}
	// CR/LF in a header value splits the message; the notifier sender sanitises
	// on the way out, and rejecting it here means it never reaches storage.
	if strings.ContainsAny(c.From+c.Username+c.SMTPHost, "\r\n") {
		return Settings{}, invalid("a line break is not allowed in these fields")
	}
	if !notify.ValidMailTLS(c.TLS) {
		return Settings{}, invalid(fmt.Sprintf("%q is not a transport security mode — use starttls, implicit or none", c.TLS))
	}
	c.TLS = tlsOrDefault(c.TLS)

	// Canonical JSON of known fields only, so an unknown key cannot ride along
	// into the sealed blob.
	plaintext, err := json.Marshal(c)
	if err != nil {
		return Settings{}, fmt.Errorf("mail: encoding config: %w", err)
	}
	ct, nonce, err := s.box.Seal(plaintext)
	if err != nil {
		return Settings{}, fmt.Errorf("mail: sealing config: %w", err)
	}
	if err := s.store.SetPanelMail(ctx, ct, nonce); err != nil {
		return Settings{}, err
	}
	return settingsOf(c, time.Now()), nil
}

func (s *Service) Delete(ctx context.Context) error { return s.store.DeletePanelMail(ctx) }

// Send delivers one message as the panel. ErrNotConfigured is a distinct answer
// from a delivery failure: one is something the operator has not done yet, the
// other is something that went wrong.
func (s *Service) Send(ctx context.Context, to []string, subject, body string) error {
	c, _, err := s.load(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotConfigured
	}
	if err != nil {
		return err
	}
	return notify.SendMail(notify.MailConfig{
		SMTPHost: c.SMTPHost, SMTPPort: c.SMTPPort,
		Username: c.Username, From: c.From, Password: c.Password,
		TLS: tlsOrDefault(c.TLS),
	}, to, subject, body)
}

// Test proves the settings work by using them, and reports what the server said
// when they do not. It writes nothing — a test that persisted state would be a
// second way to configure the panel.
func (s *Service) Test(ctx context.Context) error {
	c, _, err := s.load(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotConfigured
	}
	if err != nil {
		return err
	}
	return s.Send(ctx, []string{c.From},
		"CypherPanel test message",
		"If you are reading this, the panel can send mail.\n\nSent by CypherPanel from "+c.SMTPHost+".")
}

func (s *Service) load(ctx context.Context) (Config, time.Time, error) {
	ct, nonce, updated, err := s.store.GetPanelMail(ctx)
	if err != nil {
		return Config{}, time.Time{}, err
	}
	plaintext, err := s.box.Open(ct, nonce)
	if err != nil {
		return Config{}, time.Time{}, fmt.Errorf("mail: opening config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(plaintext, &c); err != nil {
		return Config{}, time.Time{}, fmt.Errorf("mail: decoding config: %w", err)
	}
	return c, updated, nil
}
