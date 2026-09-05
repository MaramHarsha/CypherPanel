package rest

// HTTP tests for the email-change routes' throttling
// (control-plane-hardening.md §5; panel-mail.md §5 has promised it since it
// shipped). Both routes are brute-force surfaces — one guesses the current
// password, the other the confirmation secret — so both answer 429 with a
// countdown, exactly as sign-in does.

import (
	"bytes"
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

// ─── pending email change: read it back, and abandon it ─────────────────────

// TestPendingEmailChangeLifecycleOverHTTP walks the three answers the profile
// screen needs: nothing pending, something pending, and nothing again after the
// person says "this wasn't me". The token never appears in any of them — a
// session may start or abandon a move, never complete one.
func TestPendingEmailChangeLifecycleOverHTTP(t *testing.T) {
	ts := newEmailChangeServer(t, 50)
	token := login(t, ts)

	// Nothing pending is the ordinary answer, and it is a 404 rather than an
	// empty object so the client is not left comparing zero values.
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/auth/email/change", token, ""); status != http.StatusNotFound {
		t.Fatalf("with nothing pending = %d, want 404 (body %s)", status, body)
	}

	// Cancelling nothing is honest about having cancelled nothing.
	status, _, body := doJSON(t, "DELETE", ts.URL+"/api/v1/auth/email/change", token, "")
	if status != http.StatusOK {
		t.Fatalf("cancel with nothing pending = %d, want 200 (body %s)", status, body)
	}
	var cancelled struct {
		Cancelled int64 `json:"cancelled"`
	}
	if err := json.Unmarshal(body, &cancelled); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if cancelled.Cancelled != 0 {
		t.Fatalf("cancelled = %d with nothing pending, want 0", cancelled.Cancelled)
	}

	// Ask for a real move.
	req := `{"new_email":"moved@example.com","current_password":"` + testPassword + `"}`
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/change", token, req); status != http.StatusAccepted {
		t.Fatalf("requesting the change = %d, want 202 (body %s)", status, body)
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/auth/email/change", token, "")
	if status != http.StatusOK {
		t.Fatalf("with a change pending = %d, want 200 (body %s)", status, body)
	}
	var pending pendingEmailChangeDTO
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if pending.NewEmail != "moved@example.com" {
		t.Fatalf("new_email = %q, want moved@example.com", pending.NewEmail)
	}
	if !pending.ExpiresAt.After(pending.RequestedAt) {
		t.Fatalf("expires_at %v is not after requested_at %v", pending.ExpiresAt, pending.RequestedAt)
	}
	// The wire token is the one thing this route must never carry.
	if bytes.Contains(body, []byte("token")) {
		t.Fatalf("the pending-change body mentions a token: %s", body)
	}

	// "This wasn't me" spends it, and the next read is 404 again.
	status, _, body = doJSON(t, "DELETE", ts.URL+"/api/v1/auth/email/change", token, "")
	if status != http.StatusOK {
		t.Fatalf("cancel = %d, want 200 (body %s)", status, body)
	}
	if err := json.Unmarshal(body, &cancelled); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if cancelled.Cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled.Cancelled)
	}
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/auth/email/change", token, ""); status != http.StatusNotFound {
		t.Fatalf("after cancelling = %d, want 404 (body %s)", status, body)
	}
}

// TestCancelEmailChangeKillsEveryOutstandingLink: a second request supersedes
// the first in the UI, but both links are live until they expire. Cancelling
// must kill both, or "this wasn't me" leaves the attacker's second link working.
func TestCancelEmailChangeKillsEveryOutstandingLink(t *testing.T) {
	ts := newEmailChangeServer(t, 50)
	token := login(t, ts)

	for _, addr := range []string{"first@example.com", "second@example.com"} {
		body := `{"new_email":"` + addr + `","current_password":"` + testPassword + `"}`
		if status, _, b := doJSON(t, "POST", ts.URL+"/api/v1/auth/email/change", token, body); status != http.StatusAccepted {
			t.Fatalf("requesting %s = %d, want 202 (body %s)", addr, status, b)
		}
	}

	// Newest wins in the read-back.
	_, _, body := doJSON(t, "GET", ts.URL+"/api/v1/auth/email/change", token, "")
	var pending pendingEmailChangeDTO
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if pending.NewEmail != "second@example.com" {
		t.Fatalf("new_email = %q, want the newest request", pending.NewEmail)
	}

	_, _, body = doJSON(t, "DELETE", ts.URL+"/api/v1/auth/email/change", token, "")
	var cancelled struct {
		Cancelled int64 `json:"cancelled"`
	}
	if err := json.Unmarshal(body, &cancelled); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if cancelled.Cancelled != 2 {
		t.Fatalf("cancelled = %d, want both outstanding links", cancelled.Cancelled)
	}
}
