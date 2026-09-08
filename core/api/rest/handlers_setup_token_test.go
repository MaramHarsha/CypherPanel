package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/onboarding"
)

type fakeOnboarding struct{ created int }

func (f *fakeOnboarding) NeedsSetup(context.Context) (bool, error) { return f.created == 0, nil }
func (f *fakeOnboarding) CreateFirstOwner(_ context.Context, email, _ string) (domain.User, error) {
	f.created++
	return domain.User{ID: "usr_owner", Email: email, Role: domain.RoleOwner}, nil
}
func (f *fakeOnboarding) Progress(context.Context, onboarding.ProgressStore) (onboarding.Progress, error) {
	return onboarding.Progress{}, nil
}

func newSetupServer(t *testing.T, setupToken string) (*httptest.Server, *fakeOnboarding) {
	t.Helper()
	ob := &fakeOnboarding{}
	authStore := &fakeAuthStore{sessions: map[string]domain.User{}}
	api := New(Deps{
		Auth:       auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Onboarding: ob,
		SetupToken: setupToken,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, ob
}

func postSetup(t *testing.T, ts *httptest.Server, body map[string]string) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/api/v1/auth/setup", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /auth/setup: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// The panel's port is open to the internet the moment install.sh finishes, so
// the first-run claim needs the code the installer printed on the host's
// console — the one thing a scanner that reached the port first does not have.
func TestClaimingAFreshPanelNeedsTheInstallersCode(t *testing.T) {
	ts, ob := newSetupServer(t, "the-code-from-the-console")

	resp, err := http.Get(ts.URL + "/api/v1/auth/setup")
	if err != nil {
		t.Fatalf("GET /auth/setup: %v", err)
	}
	var status struct {
		NeedsSetup    bool `json:"needs_setup"`
		RequiresToken bool `json:"requires_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&status)
	_ = resp.Body.Close()
	if !status.NeedsSetup || !status.RequiresToken {
		t.Fatalf("status = %+v, want needs_setup and requires_token", status)
	}

	if code := postSetup(t, ts, map[string]string{"email": "intruder@example.com", "password": "intruder-pass-1"}); code != http.StatusForbidden {
		t.Fatalf("a claim without the code answered %d, want 403", code)
	}
	if code := postSetup(t, ts, map[string]string{"email": "intruder@example.com", "password": "intruder-pass-1", "setup_token": "guess"}); code != http.StatusForbidden {
		t.Fatalf("a claim with a wrong code answered %d, want 403", code)
	}
	if ob.created != 0 {
		t.Fatal("an owner was created without the code")
	}
	if code := postSetup(t, ts, map[string]string{"email": "owner@example.com", "password": "owner-password-1", "setup_token": " the-code-from-the-console "}); code != http.StatusCreated {
		t.Fatalf("the right code answered %d, want 201", code)
	}
	if ob.created != 1 {
		t.Fatalf("created = %d, want 1", ob.created)
	}
}

// A panel with no code configured — a dev panel, docker compose, the env-var
// bootstrap — claims as it always did.
func TestAPanelWithoutACodeClaimsAsBefore(t *testing.T) {
	ts, ob := newSetupServer(t, "")
	if code := postSetup(t, ts, map[string]string{"email": "owner@example.com", "password": "owner-password-1"}); code != http.StatusCreated {
		t.Fatalf("answered %d, want 201", code)
	}
	if ob.created != 1 {
		t.Fatalf("created = %d, want 1", ob.created)
	}
}
