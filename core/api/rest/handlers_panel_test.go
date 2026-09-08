package rest

// HTTP tests for the panel diagnostics routes (control-plane-hardening.md
// §§3–4): what build is running, whether a newer one exists, and a bounded
// tail of the panel's own log. The version is readable by any authenticated
// principal; the log is panel-owner and interactive-session only, because it
// names hosts, resources and users.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/deploykeys"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/logring"
	"github.com/MaramHarsha/cypherpanel/core/updates"
)

// fakePanelInfo is a PanelInfo with a fixed build and a settable latest.
type fakePanelInfo struct {
	info   updates.Info
	latest *updates.Release
}

func (f *fakePanelInfo) Current() updates.Info    { return f.info }
func (f *fakePanelInfo) Latest() *updates.Release { return f.latest }

var testBuiltAt = time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

func newPanelInfo() *fakePanelInfo {
	return &fakePanelInfo{info: updates.Info{
		Version: "v0.4.0", Commit: "0c7a08b", BuiltAt: testBuiltAt, GoVersion: "go1.25.12",
	}}
}

// newPanelServer builds an API whose logger feeds the same ring the /panel/logs
// route serves — the production wiring, so what the test reads back is exactly
// what an operator would.
func newPanelServer(t *testing.T, role string, panel PanelInfo, ring *logring.Ring) (*httptest.Server, *fakeDeployKeysStore) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: role},
		sessions: map[string]domain.User{},
	}
	box := testBox(t)
	dkStore := newFakeDeployKeysStore()
	var handler slog.Handler = slog.NewTextHandler(io.Discard, nil)
	if ring != nil {
		handler = ring.Handler(&slog.HandlerOptions{Level: slog.LevelInfo})
	}
	var logs PanelLogTail
	if ring != nil {
		logs = ring
	}
	api := New(Deps{
		Auth:       auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		DeployKeys: deploykeys.NewService(dkStore, box),
		Teams:      newFakeTeams(),
		Opener:     box,
		Pinger:     okPinger{},
		CACertPEM:  []byte("x"),
		Panel:      panel,
		PanelLogs:  logs,
		Log:        slog.New(handler),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, dkStore
}

// TestPanelVersionReportsTheBuildAndAnyNewerRelease: the three build stamps,
// the toolchain, and `latest` — null when there is nothing to say, and the
// classified release when there is (canvases 14j, 16a).
func TestPanelVersionReportsTheBuildAndAnyNewerRelease(t *testing.T) {
	panel := newPanelInfo()
	ts, _ := newPanelServer(t, domain.RoleMember, panel, nil)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/version", token, "")
	if status != http.StatusOK {
		t.Fatalf("version as a member = %d, body %s", status, body)
	}
	var got struct {
		Version   string  `json:"version"`
		Commit    string  `json:"commit"`
		BuiltAt   *string `json:"built_at"`
		GoVersion string  `json:"go_version"`
		Latest    *struct {
			Version     string  `json:"version"`
			Kind        string  `json:"kind"`
			NotesURL    string  `json:"notes_url"`
			PublishedAt *string `json:"published_at"`
		} `json:"latest"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.Version != "v0.4.0" || got.Commit != "0c7a08b" || got.GoVersion != "go1.25.12" {
		t.Fatalf("build = %+v, want the stamped values", got)
	}
	if got.BuiltAt == nil || *got.BuiltAt != "2026-09-05T10:00:00Z" {
		t.Fatalf("built_at = %v, want the stamped RFC3339 instant", got.BuiltAt)
	}
	if got.Latest != nil {
		t.Fatalf("latest = %+v, want null when there is nothing newer", *got.Latest)
	}
	// The key must be present and explicitly null, not omitted: the UI
	// distinguishes "nothing newer" from "the panel did not say".
	if !strings.Contains(string(body), `"latest":null`) {
		t.Errorf("body %s does not carry an explicit null latest", body)
	}

	published := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	panel.latest = &updates.Release{Version: "v1.0.0", Kind: "major", NotesURL: "https://example.test/r/v1.0.0", PublishedAt: published}
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/panel/version", token, "")
	if status != http.StatusOK {
		t.Fatalf("version = %d, body %s", status, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if got.Latest == nil || got.Latest.Version != "v1.0.0" || got.Latest.Kind != "major" ||
		got.Latest.NotesURL != "https://example.test/r/v1.0.0" ||
		got.Latest.PublishedAt == nil || *got.Latest.PublishedAt != "2026-09-04T09:00:00Z" {
		t.Fatalf("latest = %+v, want the newer release with its class and notes", got.Latest)
	}
}

// Unauthenticated callers get 401; a panel with no version source says so
// rather than inventing one.
func TestPanelVersionRequiresAuthAndReportsUnavailable(t *testing.T) {
	ts, _ := newPanelServer(t, domain.RoleMember, newPanelInfo(), nil)
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/panel/version", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", status)
	}
	bare, _ := newPanelServer(t, domain.RoleOwner, nil, nil)
	token := login(t, bare)
	if status, _, body := doJSON(t, "GET", bare.URL+"/api/v1/panel/version", token, ""); status != http.StatusServiceUnavailable {
		t.Fatalf("with no version source = %d, want 503 (body %s)", status, body)
	}
}

// TestPanelLogsAreOwnerAndSessionOnly: a member is refused, an owner is served,
// and an API token — even an owner's, even with every ability — never reaches
// the route (§9).
func TestPanelLogsAreOwnerAndSessionOnly(t *testing.T) {
	member, _ := newPanelServer(t, domain.RoleMember, newPanelInfo(), logring.New(50))
	memberToken := login(t, member)
	if status, _, body := doJSON(t, "GET", member.URL+"/api/v1/panel/logs", memberToken, ""); status != http.StatusForbidden {
		t.Fatalf("as a member = %d, want 403 (body %s)", status, body)
	}

	ts, _ := newPanelServer(t, domain.RoleOwner, newPanelInfo(), logring.New(50))
	token := login(t, ts)
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/logs", token, ""); status != http.StatusOK {
		t.Fatalf("as an owner = %d, want 200 (body %s)", status, body)
	}
	if status, _, _ := doJSON(t, "GET", ts.URL+"/api/v1/panel/logs", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", status)
	}

	// A personal access token holds the read ability, and is still refused:
	// the log must not be liftable by a credential that can live in CI.
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/tokens", token, `{"name":"ci","abilities":["read","write","deploy"]}`)
	if status != http.StatusCreated {
		t.Fatalf("minting a token = %d, body %s", status, body)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &minted); err != nil || minted.Token == "" {
		t.Fatalf("token response %s: %v", body, err)
	}
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/logs", minted.Token, ""); status != http.StatusForbidden {
		t.Fatalf("with an API token = %d, want 403 (body %s)", status, body)
	}
	// The version, by contrast, is exactly what a token holder reporting a bug
	// needs, so it stays available to them.
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/version", minted.Token, ""); status != http.StatusOK {
		t.Fatalf("version with an API token = %d, want 200 (body %s)", status, body)
	}
}

// TestPanelLogsBoundTheTail: the default, the explicit bound, and the refusals
// for anything outside 1..500.
func TestPanelLogsBoundTheTail(t *testing.T) {
	ring := logring.New(panelLogsMaxTail)
	ts, _ := newPanelServer(t, domain.RoleOwner, newPanelInfo(), ring)
	token := login(t, ts)
	// Every request logs a line, so a handful of calls fills the tail.
	for range 12 {
		doJSON(t, "GET", ts.URL+"/api/v1/panel/version", token, "")
	}

	read := func(query string) (int, panelLogsResponse) {
		t.Helper()
		status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/panel/logs"+query, token, "")
		var out panelLogsResponse
		if status == http.StatusOK {
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}
		}
		return status, out
	}

	status, all := read("")
	if status != http.StatusOK || all.Capacity != panelLogsMaxTail {
		t.Fatalf("default tail = %d, capacity %d", status, all.Capacity)
	}
	if len(all.Lines) == 0 || len(all.Lines) > panelLogsDefaultTail {
		t.Fatalf("default returned %d lines, want 1..%d", len(all.Lines), panelLogsDefaultTail)
	}
	status, three := read("?tail=3")
	if status != http.StatusOK || len(three.Lines) != 3 {
		t.Fatalf("tail=3 = %d with %d lines, want 200 with 3", status, len(three.Lines))
	}
	// Oldest first: every read is itself logged, so the windows differ between
	// calls; what must hold inside one answer is that the timestamps ascend.
	for i := 1; i < len(all.Lines); i++ {
		if logLineTime(t, all.Lines[i-1]) > logLineTime(t, all.Lines[i]) {
			t.Fatalf("line %d is older than line %d; the tail is not oldest-first:\n%s\n%s", i, i-1, all.Lines[i-1], all.Lines[i])
		}
	}
	// The narrow window is the tail of the wide one: same lines, fewer of them.
	if three.Lines[0] != all.Lines[len(all.Lines)-2] {
		t.Errorf("tail=3 does not start where the default window's last lines do:\n%q\n%q", three.Lines[0], all.Lines[len(all.Lines)-2])
	}
	for _, bad := range []string{"?tail=0", "?tail=-1", "?tail=501", "?tail=abc", "?tail=1e3"} {
		if status, _ := read(bad); status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, status)
		}
	}
	if status, max := read("?tail=" + strconv.Itoa(panelLogsMaxTail)); status != http.StatusOK || len(max.Lines) > panelLogsMaxTail {
		t.Errorf("tail=%d = %d with %d lines", panelLogsMaxTail, status, len(max.Lines))
	}
}

// TestPanelLogsNeverCarrySealedValues: a deploy key's private half is sealed
// through the API; neither its plaintext PEM nor the ciphertext the store
// holds may appear in the tail an owner can read (ENGINEERING rule 20 —
// asserted, not assumed).
func TestPanelLogsNeverCarrySealedValues(t *testing.T) {
	ring := logring.New(panelLogsMaxTail)
	ts, dkStore := newPanelServer(t, domain.RoleOwner, newPanelInfo(), ring)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deploy-keys", token, `{"name":"ci-key"}`)
	if status != http.StatusCreated {
		t.Fatalf("create deploy key = %d, body %s", status, body)
	}
	var created struct {
		DeployKey struct {
			ID string `json:"id"`
		} `json:"deploy_key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	sealed, ok := dkStore.sealedPrivateKey(created.DeployKey.ID)
	if !ok || len(sealed) == 0 {
		t.Fatal("the deploy key was stored without sealed private-key material; the test proves nothing")
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/panel/logs?tail=500", token, "")
	if status != http.StatusOK {
		t.Fatalf("panel logs = %d, body %s", status, body)
	}
	tail := string(body)
	for name, needle := range map[string]string{
		"the sealed ciphertext (raw)":     string(sealed),
		"the sealed ciphertext (base64)":  base64.StdEncoding.EncodeToString(sealed),
		"the sealed ciphertext (hex-ish)": strings.ToLower(base64.RawURLEncoding.EncodeToString(sealed)),
		"the sign-in password":            testPassword,
		"a PEM private key":               "PRIVATE KEY",
	} {
		if strings.Contains(tail, needle) {
			t.Errorf("the panel log tail contains %s", name)
		}
	}
}

// logLineTime extracts the RFC3339 timestamp a slog text record starts with.
func logLineTime(t *testing.T, line string) string {
	t.Helper()
	const prefix = "time="
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("log line %q does not start with a timestamp", line)
	}
	stamp, _, _ := strings.Cut(strings.TrimPrefix(line, prefix), " ")
	return stamp
}
