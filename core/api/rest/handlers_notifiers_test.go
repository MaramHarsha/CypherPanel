package rest

// HTTP tests for the two "test this connection" routes (panel-mail.md's sibling
// pattern; design canvas 13aj, "one connection pattern, always testable").
//
// The contract both share: the request succeeding and the connection working
// are different facts. A reachable far end that refuses the message is a 200
// with ok:false, because the caller needs to tell "I could not ask" apart from
// "I asked and the answer was no". A configuration the panel would refuse to
// store is a 400 — there is nothing to retry until the form changes.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/notify"
	"github.com/MaramHarsha/cypherpanel/core/projects"
)

// fakeDelivery records what it was asked to send and answers with whatever the
// test set up. It never talks to a network.
type fakeDelivery struct {
	testConfigErr error
	deliverErr    error

	channels []string          // channels TestConfig was called with
	configs  []json.RawMessage // and the configs, so a test can prove they are not stored
	delivers int
}

func (f *fakeDelivery) Deliver(_ context.Context, _ domain.Notifier, _ domain.NotifyEvent) error {
	f.delivers++
	return f.deliverErr
}

func (f *fakeDelivery) TestConfig(_ context.Context, channel string, cfg json.RawMessage) error {
	f.channels = append(f.channels, channel)
	f.configs = append(f.configs, cfg)
	return f.testConfigErr
}

// newNotifierTestServer wires a server whose user is a member of prj_test.
// delivery may be nil, which is how a panel with notifications switched off
// behaves.
func newNotifierTestServer(t *testing.T, delivery NotifierDelivery) *httptest.Server {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ft := newFakeTeams()
	ft.projectRoles["usr_test"] = map[string]string{"prj_test": domain.RoleMember}
	ft.teams = nil

	deps := Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Projects:  projects.NewService(newFakeProjectsStore()),
		Teams:     ft,
		Pinger:    okPinger{},
		CACertPEM: []byte("x"),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if delivery != nil {
		deps.NotifyDelivery = delivery
	}
	ts := httptest.NewServer(New(deps).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func decodeConnectionTest(t *testing.T, body []byte) connectionTestDTO {
	t.Helper()
	var got connectionTestDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return got
}

const slackConfig = `{"channel":"slack","config":{"webhook_url":"https://hooks.slack.test/services/x"}}`

// TestTestNotifierConfigReportsSuccess: the happy path answers a verdict, and
// the configuration reaches the delivery seam unchanged.
func TestTestNotifierConfigReportsSuccess(t *testing.T) {
	d := &fakeDelivery{}
	ts := newNotifierTestServer(t, d)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/notifiers/test", token, slackConfig)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	got := decodeConnectionTest(t, body)
	if !got.OK {
		t.Fatalf("ok = false on a successful test: %+v", got)
	}
	if got.Detail == "" {
		t.Fatal("a successful test said nothing about what it did")
	}
	if len(d.channels) != 1 || d.channels[0] != "slack" {
		t.Fatalf("channels reaching delivery = %v, want [slack]", d.channels)
	}
}

// TestTestNotifierConfigSurfacesAChannelFailure: the far end refusing is the
// answer this route exists to give, and it is not an error status — the request
// itself worked.
func TestTestNotifierConfigSurfacesAChannelFailure(t *testing.T) {
	d := &fakeDelivery{testConfigErr: errors.New("connection refused")}
	ts := newNotifierTestServer(t, d)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/notifiers/test", token, slackConfig)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failed test is still a completed request (body %s)", status, body)
	}
	got := decodeConnectionTest(t, body)
	if got.OK {
		t.Fatal("ok = true although the channel failed")
	}
	// Verbatim: paraphrasing "connection refused" makes an operator guess.
	if got.Detail != "connection refused" {
		t.Fatalf("detail = %q, want the far end's own words", got.Detail)
	}
}

// TestTestNotifierConfigRejectsAnUnstorableConfig: a config create would refuse
// is a 400, not a failed test, because retrying it cannot change the answer.
func TestTestNotifierConfigRejectsAnUnstorableConfig(t *testing.T) {
	d := &fakeDelivery{testConfigErr: &notify.ValidationError{Msg: "telegram requires bot_token and chat_id"}}
	ts := newNotifierTestServer(t, d)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/notifiers/test", token,
		`{"channel":"telegram","config":{"bot_token":""}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", status, body)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if e.Error == "" {
		t.Fatal("the refusal did not say what was wrong")
	}
}

// TestTestNotifierConfigNeedsProjectMembership: a project you cannot see does
// not exist, so this route answers 404 rather than 403 — no tenancy probing,
// and no free reachability check against an arbitrary URL.
func TestTestNotifierConfigNeedsProjectMembership(t *testing.T) {
	d := &fakeDelivery{}
	ts := newNotifierTestServer(t, d)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_other/notifiers/test", token, slackConfig)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a project the caller is not in (body %s)", status, body)
	}
	if len(d.channels) != 0 {
		t.Fatalf("delivery was reached for an unauthorized project: %v", d.channels)
	}
}

// TestTestNotifierConfigOnAPanelWithoutNotifications: the route is honest about
// being switched off rather than pretending the test passed.
func TestTestNotifierConfigOnAPanelWithoutNotifications(t *testing.T) {
	ts := newNotifierTestServer(t, nil)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/projects/prj_test/notifiers/test", token, slackConfig)
	if status != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", status, body)
	}
}
