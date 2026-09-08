package rest

// The join command must be self-sufficient: a fresh host that has never seen a
// cypher-agent binary has to be able to run the pasted line and end up enrolled
// (agent-identity-and-tls.md §6). A release panel pins the agent to its own
// version; a development build names none and the installer falls back to the
// project's latest release.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/servers"
	"github.com/MaramHarsha/cypherpanel/core/updates"
)

func newJoinServer(t *testing.T, panel PanelInfo) *httptest.Server {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleOwner},
		sessions: map[string]domain.User{},
	}
	box := testBox(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := New(Deps{
		Auth:       auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Servers:    servers.NewService(&fakeServersStore{inUse: map[string]bool{}}, noopAgentBus{}, 15*time.Minute, log),
		Teams:      newFakeTeams(),
		Opener:     box,
		Pinger:     okPinger{},
		CACertPEM:  []byte("-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n"),
		EnrollAddr: "plane.example.com:8443",
		NATSURL:    "tls://plane.example.com:4222",
		ConsoleURL: "http://plane.example.com:8080",
		Panel:      panel,
		Log:        log,
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func joinInstallCommand(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	token := login(t, ts)
	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/servers", token, `{"name":"fresh-vm"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d body %s", status, body)
	}
	var created struct {
		Join struct {
			InstallCommand string `json:"install_command"`
		} `json:"join"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.Join.InstallCommand
}

// A release panel pins the agent binary to its own version, so a joining server
// runs the agent that matches the plane.
func TestInstallCommandPinsTheAgentToThePanelVersion(t *testing.T) {
	ts := newJoinServer(t, &fakePanelInfo{info: updates.Info{Version: "v1.4.2"}})
	cmd := joinInstallCommand(t, ts)

	want := "CYPHER_AGENT_URL='https://github.com/MaramHarsha/CypherPanel/releases/download/v1.4.2/cypher-agent-linux-{arch}'"
	if !strings.Contains(cmd, want) {
		t.Fatalf("install_command does not pin the agent binary:\n%s", cmd)
	}
	// Still a single pasteable pipeline, and the env assignments must precede
	// the `sh` they apply to.
	if !strings.HasSuffix(cmd, " sh") {
		t.Fatalf("install_command does not end in the shell it feeds:\n%s", cmd)
	}
	if !strings.Contains(cmd, "curl -fsSL http://plane.example.com:8080/install/agent.sh | ") {
		t.Fatalf("install_command lost its script fetch:\n%s", cmd)
	}
}

// A development build has no release asset to name, so it names none: the
// installer's own GitHub-latest default takes over.
func TestInstallCommandOmitsTheURLOnADevelopmentBuild(t *testing.T) {
	for _, version := range []string{"dev", "main", "0c7a08b"} {
		t.Run(version, func(t *testing.T) {
			ts := newJoinServer(t, &fakePanelInfo{info: updates.Info{Version: version}})
			if cmd := joinInstallCommand(t, ts); strings.Contains(cmd, "CYPHER_AGENT_URL") {
				t.Fatalf("development build pinned a binary URL:\n%s", cmd)
			}
		})
	}
}

// A panel with no version information at all still produces a usable command.
func TestInstallCommandWithoutPanelInfo(t *testing.T) {
	ts := newJoinServer(t, nil)
	cmd := joinInstallCommand(t, ts)
	if strings.Contains(cmd, "CYPHER_AGENT_URL") {
		t.Fatalf("named a binary URL with no version information:\n%s", cmd)
	}
	for _, part := range []string{"/install/agent.sh", "CYPHER_PLANE=plane.example.com:8443", "CYPHER_CA_FINGERPRINT="} {
		if !strings.Contains(cmd, part) {
			t.Fatalf("install_command missing %q:\n%s", part, cmd)
		}
	}
}
