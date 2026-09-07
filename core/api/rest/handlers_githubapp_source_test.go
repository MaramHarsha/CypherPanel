package rest

// The GitHub App as an application's clone credential, over HTTP.
//
// The review that produced this file found the feature half-built: the column,
// the store, the scheduler and the agent all carried
// `github_installation_id`, and the API had no field for it, so nothing an
// operator could reach ever set one. The back half had no test either — the
// credential path was entirely uncovered — which is why the gap survived.
//
// These assert the chain the panel actually exercises: set it on create, read
// it back, change it with a PATCH, and be refused when it names an
// installation the panel does not have.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The one installation fakeAppsStore reports.
const testInstallationID = 4242

func TestApplicationCarriesItsGitHubInstallation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	token := login(t, ts)

	body := `{"name":"app-with-app","source":{"kind":"github","repo":"https://github.com/acme/web","branch":"main","github_installation_id":4242},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"app.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, resp)
	}
	var created struct {
		Application struct {
			ID     string `json:"id"`
			Source struct {
				GitHubInstallationID *int64 `json:"github_installation_id"`
				DeployKeyID          *string
			} `json:"source"`
		} `json:"application"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Read back on the CREATE response. A field that can be set and not read
	// back is a field nobody can verify.
	if created.Application.Source.GitHubInstallationID == nil || *created.Application.Source.GitHubInstallationID != testInstallationID {
		t.Fatalf("create response dropped the installation: %s", resp)
	}

	// And on a plain GET, which is the screen's own source of truth.
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/applications/"+created.Application.ID, token, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d body %s", status, resp)
	}
	if !strings.Contains(string(resp), `"github_installation_id":4242`) {
		t.Errorf("GET does not report the installation, so the settings screen cannot show it: %s", resp)
	}

	// A PATCH that carries source must not silently wipe it — the settings
	// screen sends the whole source object on every save, so a dropped field
	// there un-configures the credential on an unrelated edit.
	patch := `{"source":{"kind":"github","repo":"https://github.com/acme/web","branch":"release","github_installation_id":4242}}`
	status, _, resp = doJSON(t, "PATCH", ts.URL+"/api/v1/applications/"+created.Application.ID, token, patch)
	if status != http.StatusOK {
		t.Fatalf("patch: status %d body %s", status, resp)
	}
	if !strings.Contains(string(resp), `"github_installation_id":4242`) {
		t.Errorf("PATCH dropped the installation: %s", resp)
	}

	// Clearing it is a real edit and must work: moving an application off the
	// App and onto a deploy key is the reverse of the migration this enables.
	patch = `{"source":{"kind":"github","repo":"https://github.com/acme/web","branch":"release","github_installation_id":null}}`
	status, _, resp = doJSON(t, "PATCH", ts.URL+"/api/v1/applications/"+created.Application.ID, token, patch)
	if status != http.StatusOK {
		t.Fatalf("patch clearing: status %d body %s", status, resp)
	}
	if !strings.Contains(string(resp), `"github_installation_id":null`) {
		t.Errorf("the installation could not be cleared: %s", resp)
	}
}

// An installation the panel does not have is refused when it is ATTACHED, not
// discovered when a build cannot mint a token five minutes later.
func TestAnUnknownGitHubInstallationIsRefusedAtSaveTime(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	token := login(t, ts)

	body := `{"name":"app-bad-install","source":{"kind":"github","repo":"https://github.com/acme/web","branch":"main","github_installation_id":999999},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"bad.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusBadRequest {
		t.Fatalf("an unknown installation should be a 400, got %d: %s", status, resp)
	}
	// The refusal has to name the remedy, which is on GitHub and not here.
	if !strings.Contains(string(resp), "install") {
		t.Errorf("the refusal does not say what to do about it: %s", resp)
	}
}

// An image source clones nothing, so a clone credential on one is meaningless
// and is cleared rather than stored to confuse a later reader.
func TestAnImageSourceCarriesNoGitHubInstallation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	token := login(t, ts)

	body := `{"name":"app-image","source":{"kind":"image","image":"ghcr.io/acme/web:1.2","github_installation_id":4242},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"img.example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %s", status, resp)
	}
	if !strings.Contains(string(resp), `"github_installation_id":null`) {
		t.Errorf("an image source kept a clone credential it can never use: %s", resp)
	}
}
