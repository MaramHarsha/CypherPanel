package rest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The deploy-key endpoints expose only the public half of the key: the sealed
// private key must never cross the API boundary (ENGINEERING rule 20;
// deploy-key-private-repos.md §1).
func TestDeployKeyLifecycle(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deploy-keys", token, `{"name":"ci-key"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, body %s", status, body)
	}
	var created struct {
		DeployKey map[string]any `json:"deploy_key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	allowed := map[string]bool{"id": true, "name": true, "public_key": true, "fingerprint": true, "created_at": true}
	for k := range created.DeployKey {
		if !allowed[k] {
			t.Errorf("create response exposes unexpected field %q (private key material must stay sealed)", k)
		}
	}
	id, _ := created.DeployKey["id"].(string)
	if !strings.HasPrefix(id, "dk_") {
		t.Fatalf("id = %q, want dk_ prefix", id)
	}
	if pub, _ := created.DeployKey["public_key"].(string); !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("public_key = %q, want an ssh-ed25519 authorized_keys line", pub)
	}
	if fp, _ := created.DeployKey["fingerprint"].(string); !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("fingerprint = %q, want SHA256: prefix", fp)
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/deploy-keys/"+id, token, "")
	if status != http.StatusOK {
		t.Fatalf("get = %d, body %s", status, body)
	}

	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/deploy-keys", token, "")
	if status != http.StatusOK {
		t.Fatalf("list = %d, body %s", status, body)
	}
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal list response (want a bare array per openapi.yaml): %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	if status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/deploy-keys/"+id, token, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", status)
	}
	if status, _, _ = doJSON(t, "GET", ts.URL+"/api/v1/deploy-keys/"+id, token, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", status)
	}
	if status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/deploy-keys/"+id, token, ""); status != http.StatusNotFound {
		t.Fatalf("delete of missing key = %d, want 404", status)
	}
}

func TestDeployKeyCreateValidation(t *testing.T) {
	ts := newTestServer(t)
	token := login(t, ts)

	for name, body := range map[string]string{
		"empty name":      `{"name":""}`,
		"whitespace name": `{"name":"   "}`,
		"name too long":   `{"name":"` + strings.Repeat("x", 101) + `"}`,
		"not json":        `{`,
	} {
		if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/deploy-keys", token, body); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
}

func TestDeployKeyDeleteInUseConflicts(t *testing.T) {
	ts, _, _, dkStore := newTestServerFull(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/deploy-keys", token, `{"name":"in-use"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, body %s", status, body)
	}
	var created struct {
		DeployKey struct {
			ID string `json:"id"`
		} `json:"deploy_key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	dkStore.markInUse(created.DeployKey.ID)

	if status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/deploy-keys/"+created.DeployKey.ID, token, ""); status != http.StatusConflict {
		t.Fatalf("delete of in-use key = %d, want 409", status)
	}
}
