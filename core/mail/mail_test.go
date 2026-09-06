package mail

// The panel's own transport, tested where it makes decisions: what it will
// accept, what it will say back, and the one field it must never say back.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore keeps one sealed blob, as the real singleton row does.
type fakeStore struct {
	ct, nonce []byte
	updated   time.Time
	set       int
}

func (f *fakeStore) GetPanelMail(context.Context) ([]byte, []byte, time.Time, error) {
	if f.ct == nil {
		return nil, nil, time.Time{}, store.ErrNotFound
	}
	return f.ct, f.nonce, f.updated, nil
}

func (f *fakeStore) SetPanelMail(_ context.Context, ct, nonce []byte) error {
	f.ct, f.nonce, f.updated, f.set = ct, nonce, time.Now(), f.set+1
	return nil
}

func (f *fakeStore) DeletePanelMail(context.Context) error {
	f.ct, f.nonce = nil, nil
	return nil
}

// plainBox is a SecretBox that does not encrypt, so a test can assert on what
// would have been sealed. Sealing is core/secret's job and is tested there.
type plainBox struct{}

func (plainBox) Seal(pt []byte) ([]byte, []byte, error) { return pt, []byte("n"), nil }
func (plainBox) Open(ct, _ []byte) ([]byte, error)      { return ct, nil }

func validConfig() Config {
	return Config{
		SMTPHost: "smtp.example.test",
		SMTPPort: 587,
		Username: "panel@example.test",
		Password: "hunter2",
		From:     "panel@example.test",
	}
}

// TestSetReadsBackEverythingButThePassword is the whole point of the readback:
// an operator changing the port must not have to retype the host, the username
// and the from address from memory, and the password must still never come out.
func TestSetReadsBackEverythingButThePassword(t *testing.T) {
	st := &fakeStore{}
	svc := New(st, plainBox{})
	ctx := context.Background()

	if _, err := svc.Set(ctx, validConfig()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !got.Configured {
		t.Fatal("Configured = false after Set")
	}
	if got.SMTPHost != "smtp.example.test" || got.SMTPPort != 587 {
		t.Fatalf("host/port read back as %q/%d", got.SMTPHost, got.SMTPPort)
	}
	if got.Username != "panel@example.test" || got.From != "panel@example.test" {
		t.Fatalf("username/from read back as %q/%q", got.Username, got.From)
	}
	// Settings has no password field at all, so the compiler enforces most of
	// this; the sealed blob is where a leak could still hide.
	if strings.Contains(got.Hint, "hunter2") {
		t.Fatalf("the hint carries the password: %q", got.Hint)
	}
}

// TestTLSModeDefaultsToStartTLS: settings saved before the mode existed, and
// requests that omit it, are sent with STARTTLS. Anything else would be a
// silent downgrade on upgrade.
func TestTLSModeDefaultsToStartTLS(t *testing.T) {
	svc := New(&fakeStore{}, plainBox{})
	ctx := context.Background()

	s, err := svc.Set(ctx, validConfig()) // no TLS field
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if s.TLS != notify.TLSStartTLS {
		t.Fatalf("TLS after Set = %q, want %q", s.TLS, notify.TLSStartTLS)
	}
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TLS != notify.TLSStartTLS {
		t.Fatalf("TLS read back = %q, want %q", got.TLS, notify.TLSStartTLS)
	}
}

func TestSetValidatesTLSMode(t *testing.T) {
	svc := New(&fakeStore{}, plainBox{})
	ctx := context.Background()

	for _, mode := range []string{notify.TLSStartTLS, notify.TLSImplicit, notify.TLSNone} {
		c := validConfig()
		c.TLS = mode
		s, err := svc.Set(ctx, c)
		if err != nil {
			t.Fatalf("Set with tls=%q: %v", mode, err)
		}
		if s.TLS != mode {
			t.Fatalf("Set with tls=%q read back %q", mode, s.TLS)
		}
	}

	c := validConfig()
	c.TLS = "ssl" // a plausible guess, and not one of ours
	_, err := svc.Set(ctx, c)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Set with an unknown TLS mode: err = %v, want a ValidationError", err)
	}
	if !strings.Contains(ve.Error(), "starttls") {
		t.Fatalf("the refusal does not name the modes that work: %q", ve.Error())
	}
}

// TestSetRejectsUnusableConfigs keeps the existing guarantees green while the
// shape of Config changes around them.
func TestSetRejectsUnusableConfigs(t *testing.T) {
	svc := New(&fakeStore{}, plainBox{})
	ctx := context.Background()

	cases := map[string]func(*Config){
		"no host":             func(c *Config) { c.SMTPHost = "" },
		"port zero":           func(c *Config) { c.SMTPPort = 0 },
		"port out of range":   func(c *Config) { c.SMTPPort = 70000 },
		"no from":             func(c *Config) { c.From = "" },
		"from not an address": func(c *Config) { c.From = "not-an-address" },
		"header injection":    func(c *Config) { c.From = "ops@example.test\r\nBcc: elsewhere@example.test" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if _, err := svc.Set(ctx, c); err == nil {
				t.Fatal("Set accepted a config it should have refused")
			}
		})
	}
}

// TestGetOnAnEmptyPanelIsNotAnError: "nothing configured" is an answer the
// settings screen renders, not a failure it reports.
func TestGetOnAnEmptyPanelIsNotAnError(t *testing.T) {
	s, err := New(&fakeStore{}, plainBox{}).Get(context.Background())
	if err != nil {
		t.Fatalf("Get on an unconfigured panel: %v", err)
	}
	if s.Configured || s.SMTPHost != "" {
		t.Fatalf("unconfigured settings came back populated: %+v", s)
	}
}

// TestSendWithoutConfigurationIsDistinguishable: callers show the operator a
// route to Settings → Mail for this, and a failure for anything else.
func TestSendWithoutConfigurationIsDistinguishable(t *testing.T) {
	svc := New(&fakeStore{}, plainBox{})
	err := svc.Send(context.Background(), []string{"someone@example.test"}, "s", "b")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Send with no transport = %v, want ErrNotConfigured", err)
	}
}
