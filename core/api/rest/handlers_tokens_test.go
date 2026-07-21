package rest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A created token is returned once with its raw secret, then works as a bearer
// credential in its own right — authenticating as its owning user.
func TestAPITokenCreateAndAuthenticate(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, `{"name":"ci-deploy"}`)
	if status != http.StatusCreated {
		t.Fatalf("create token: status %d body %s", status, body)
	}
	var created struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Name != "ci-deploy" || created.ID == "" {
		t.Fatalf("unexpected token metadata: %s", body)
	}
	if !strings.HasPrefix(created.Token, "cyp_") {
		t.Fatalf("raw token missing prefix: %q", created.Token)
	}

	// The raw token authenticates like a session on a protected route.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/auth/me", created.Token, "")
	if status != http.StatusOK {
		t.Fatalf("auth/me with token: status %d body %s", status, body)
	}
}

// The secret is shown only at creation; listing never returns it.
func TestAPITokenListOmitsSecret(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, `{"name":"one"}`)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/tokens", session, "")
	if status != http.StatusOK {
		t.Fatalf("list: status %d body %s", status, body)
	}
	if strings.Contains(string(body), "cyp_") || strings.Contains(string(body), "token") {
		t.Fatalf("listing leaked a secret: %s", body)
	}
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil || len(list) != 1 {
		t.Fatalf("expected 1 token, got %s (%v)", body, err)
	}
}

// Revoking a token makes it stop authenticating.
func TestAPITokenRevoke(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	_, _, body := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, `{"name":"tmp"}`)
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	json.Unmarshal(body, &created)

	status, _, _ := doJSON(t, "DELETE", ts.URL+"/api/v1/tokens/"+created.ID, session, "")
	if status != http.StatusNoContent {
		t.Fatalf("revoke: status %d", status)
	}
	// The revoked token no longer authenticates.
	status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/auth/me", created.Token, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: status %d", status)
	}
	// Revoking an unknown id is 404.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/tokens/tok_missing", session, "")
	if status != http.StatusNotFound {
		t.Fatalf("delete missing token: status %d, want 404", status)
	}
}

func TestAPITokenCreateValidation(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	session := login(t, ts)
	for name, payload := range map[string]string{
		"empty name":      `{"name":""}`,
		"negative expiry": `{"name":"x","expires_in_days":-1}`,
		"huge expiry":     `{"name":"x","expires_in_days":100000}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/tokens", session, payload)
			if status != http.StatusBadRequest {
				t.Fatalf("payload %s: status %d, want 400", payload, status)
			}
		})
	}
}

// A token requires authentication to be created — no session, no token.
func TestAPITokenRequiresAuth(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/tokens", "", `{"name":"x"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: status %d, want 401", status)
	}
}
