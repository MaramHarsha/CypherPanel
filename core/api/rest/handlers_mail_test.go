package rest

// HTTP tests for the email-change routes' throttling
// (control-plane-hardening.md §5; panel-mail.md §5 has promised it since it
// shipped). Both routes are brute-force surfaces — one guesses the current
// password, the other the confirmation secret — so both answer 429 with a
// countdown, exactly as sign-in does.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/mail"
)

// configuredMail is a MailService that is set up and accepts everything, so a
// test reaches the throttle rather than the "mail is not configured" refusal.
type configuredMail struct{ sent int }

func (m *configuredMail) Get(context.Context) (mail.Settings, error) {
	return mail.Settings{Configured: true, Hint: "smtp.example.test → ops@example.test"}, nil
}
func (m *configuredMail) Set(context.Context, mail.Config) (mail.Settings, error) {
	return mail.Settings{Configured: true}, nil
}
func (m *configuredMail) Delete(context.Context) error { return nil }
func (m *configuredMail) Send(_ context.Context, _ []string, _, _ string) error {
	m.sent++
	return nil
}
func (m *configuredMail) Test(context.Context) error { return nil }

func newEmailChangeServer(t *testing.T, budget int) *httptest.Server {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(budget, time.Minute), time.Hour),
		Teams:      newFakeTeams(),
		Mail:       &configuredMail{},
		Pinger:     okPinger{},
		CACertPEM:  []byte("x"),
		ConsoleURL: "https://panel.example.test",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// assertThrottled checks the 429 contract: the header, the body field, and the
// two agreeing.
func assertThrottled(t *testing.T, header http.Header, body []byte, what string) {
	t.Helper()
	secs, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || secs < 1 {
		t.Fatalf("%s: Retry-After = %q, want whole seconds", what, header.Get("Retry-After"))
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("%s: unmarshal %s: %v", what, body, err)
	}
	if e.RetryAfterSeconds != secs {
		t.Fatalf("%s: retry_after_seconds = %d, Retry-After = %d", what, e.RetryAfterSeconds, secs)
	}
	if e.TraceID != header.Get(TraceIDHeader) {
		t.Fatalf("%s: the 429 body trace_id %q != header %q", what, e.TraceID, header.Get(TraceIDHeader))
	}
}

// TestEmailChangeRequestIsThrottledOverHTTP: repeated wrong current passwords
// stop being answered, with a countdown instead of a shrug.
func TestEmailChangeRequestIsThrottledOverHTTP(t *testing.T) {
	// The budget is spent by sign-in and the email-change route together —
	// they share the client-address key — so log in first, then burn it.
	ts := newEmailChangeServer(t, 3)
	token := login(t, ts)
	wrong := `{"new_email":"new@example.com","current_password":"nope"}`
	for i := range 3 {
		status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/change", token, wrong)
		if status != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401 (body %s)", i, status, body)
		}
	}
	status, header, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/change", token, wrong)
	if status != http.StatusTooManyRequests {
		t.Fatalf("after three wrong passwords = %d, want 429 (body %s)", status, body)
	}
	assertThrottled(t, header, body, "email-change request")

	// Even the right password is refused while the throttle stands: the
	// throttle is on the attempt, not on the outcome.
	right := `{"new_email":"new@example.com","current_password":"` + testPassword + `"}`
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/change", token, right); status != http.StatusTooManyRequests {
		t.Fatalf("the right password while throttled = %d, want 429 (body %s)", status, body)
	}
}

// TestEmailChangeConfirmIsThrottledOverHTTP: guessing confirmation tokens is
// bounded the same way, and the refusal comes before the token is even parsed,
// so a valid one is not spent by a throttled call.
func TestEmailChangeConfirmIsThrottledOverHTTP(t *testing.T) {
	ts := newEmailChangeServer(t, 3)
	token := login(t, ts)
	guess := `{"token":"ec_guess.wrong-secret"}`
	for i := range 3 {
		status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/confirm", token, guess)
		if status != http.StatusBadRequest {
			t.Fatalf("guess %d = %d, want 400 (body %s)", i, status, body)
		}
	}
	status, header, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/confirm", token, guess)
	if status != http.StatusTooManyRequests {
		t.Fatalf("after three guesses = %d, want 429 (body %s)", status, body)
	}
	assertThrottled(t, header, body, "email-change confirm")
}
