package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// assertPublicDatabaseFields validates that the database DTO exposes only
// public details (rule 20).
func assertPublicDatabaseFields(t *testing.T, db map[string]any) {
	t.Helper()
	allowed := map[string]bool{
		"id":                   true,
		"environment_id":       true,
		"name":                 true,
		"engine":               true,
		"version":              true,
		"server_id":            true,
		"cpu_limit":            true,
		"memory_limit_mb":      true,
		"volume_name":          true,
		"expose_port":          true,
		"network":              true,
		"root_user":            true,
		"root_password":        true, // masked in list/get, plaintext on create/reset once
		"require_password":     true,
		"status":               true,
		"status_detail":        true,
		"desired_state":        true, // intent, distinct from observed status
		"desired_revision_id":  true,
		"observed_revision_id": true,
		"created_at":           true,
		"updated_at":           true,
	}
	for k := range db {
		if !allowed[k] {
			t.Errorf("database response exposes unexpected field %q", k)
		}
	}
}

func TestDatabaseLifecycle(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	token := login(t, ts)

	// Create database.
	body := `{"name":"prod-db","engine":"postgresql","version":"16","server_id":"srv_test"}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/databases", token, body)
	if status != http.StatusCreated {
		t.Fatalf("create database status = %d, response %s", status, resp)
	}

	var created struct {
		Database     map[string]any `json:"database"`
		RootPassword string         `json:"root_password"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("unmarshal create database response: %v", err)
	}

	assertPublicDatabaseFields(t, created.Database)
	dbID, _ := created.Database["id"].(string)
	if !strings.HasPrefix(dbID, "db_") {
		t.Fatalf("id = %q, want db_ prefix", dbID)
	}
	if created.RootPassword == "" {
		t.Fatalf("expected generated plaintext password on create, got empty")
	}

	// Retrieve database.
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/databases/"+dbID, token, "")
	if status != http.StatusOK {
		t.Fatalf("get database = %d, response %s", status, resp)
	}
	var fetched map[string]any
	if err := json.Unmarshal(resp, &fetched); err != nil {
		t.Fatalf("unmarshal get database: %v", err)
	}
	assertPublicDatabaseFields(t, fetched)
	if pwd, ok := fetched["root_password"].(string); !ok || pwd != "[sealed]" {
		t.Errorf("fetched root_password = %q, want '[sealed]' mask (rule 20)", pwd)
	}

	// List databases.
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/environments/env_test/databases", token, "")
	if status != http.StatusOK {
		t.Fatalf("list databases = %d, response %s", status, resp)
	}
	var list []map[string]any
	if err := json.Unmarshal(resp, &list); err != nil {
		t.Fatalf("unmarshal list databases: %v", err)
	}
	if len(list) != 1 || list[0]["id"] != dbID {
		t.Fatalf("list = %v, want 1 db with id %s", list, dbID)
	}

	// Connection info.
	status, _, resp = doJSON(t, "GET", ts.URL+"/api/v1/databases/"+dbID+"/connection-info", token, "")
	if status != http.StatusOK {
		t.Fatalf("get connection info = %d, response %s", status, resp)
	}
	var connInfo map[string]any
	if err := json.Unmarshal(resp, &connInfo); err != nil {
		t.Fatalf("unmarshal connection info: %v", err)
	}
	if connInfo["internal_host"] != "cypher-db-"+dbID+".cypher-env_test" {
		t.Errorf("internal_host = %q, want expected FQDN", connInfo["internal_host"])
	}
	if int(connInfo["port"].(float64)) != 5432 {
		t.Errorf("port = %v, want 5432 (default pg port)", connInfo["port"])
	}

	// Reset password.
	status, _, resp = doJSON(t, "POST", ts.URL+"/api/v1/databases/"+dbID+"/reset-password", token, "")
	if status != http.StatusOK {
		t.Fatalf("reset password = %d, response %s", status, resp)
	}
	var reset struct {
		RootPassword string `json:"root_password"`
	}
	if err := json.Unmarshal(resp, &reset); err != nil {
		t.Fatalf("unmarshal reset response: %v", err)
	}
	if reset.RootPassword == "" || reset.RootPassword == created.RootPassword {
		t.Errorf("expected fresh generated password, got %q", reset.RootPassword)
	}

	// Delete.
	status, _, _ = doJSON(t, "DELETE", ts.URL+"/api/v1/databases/"+dbID, token, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", status)
	}
}

func TestDatabaseCreateValidation(t *testing.T) {
	ts, _, _, _ := newTestServerFull(t)
	token := login(t, ts)

	cases := []struct {
		name string
		body string
	}{
		{"empty name", `{"name":"","engine":"postgresql","server_id":"srv_test"}`},
		{"unsupported engine", `{"name":"db","engine":"oracle","server_id":"srv_test"}`},
		{"empty server_id", `{"name":"db","engine":"mysql","server_id":""}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/databases", token, c.body)
			if status != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request, got %d", status)
			}
		})
	}
}

// TestConnectionInfoExposedHostIsTheServerAddress: with a port exposed, `host`
// is an address an operator can actually dial — the server's public address,
// falling back to the hostname the agent reported. It used to be the server
// id, which was not an address at all (control-plane-hardening.md §8).
func TestConnectionInfoExposedHostIsTheServerAddress(t *testing.T) {
	ts, srvStore, _, _ := newTestServerFull(t)
	token := login(t, ts)
	srvStore.list = append(srvStore.list, domain.Server{
		ID: "srv_test", Name: "prod-1", Hostname: "prod-1.internal", PublicAddress: "198.51.100.10",
	})

	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/databases", token,
		`{"name":"exposed-db","engine":"postgresql","version":"16","server_id":"srv_test"}`)
	if status != http.StatusCreated {
		t.Fatalf("create database = %d, response %s", status, resp)
	}
	var created struct {
		Database struct {
			ID string `json:"id"`
		} `json:"database"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	dbID := created.Database.ID

	// Without an exposed port the answer is the in-network address only.
	info := connectionInfo(t, ts, token, dbID)
	if info.Host != "" {
		t.Errorf("host = %q with no exposed port, want empty (there is nothing to dial from outside)", info.Host)
	}

	if status, _, resp = doJSON(t, "PATCH", ts.URL+"/api/v1/databases/"+dbID, token, `{"expose_port":5433}`); status != http.StatusOK {
		t.Fatalf("exposing a port = %d, response %s", status, resp)
	}
	info = connectionInfo(t, ts, token, dbID)
	if info.Host != "198.51.100.10" || info.Port != 5433 {
		t.Fatalf("connection info = %+v, want the server's public address and the exposed port", info)
	}
	if info.Host == "srv_test" {
		t.Fatal("host is the server id, which is not an address")
	}

	// No public address set: the hostname the agent reported is the next best
	// answer, and still an address rather than an id.
	srvStore.list[len(srvStore.list)-1].PublicAddress = ""
	if info = connectionInfo(t, ts, token, dbID); info.Host != "prod-1.internal" {
		t.Fatalf("host = %q with no public address, want the reported hostname", info.Host)
	}
}

func connectionInfo(t *testing.T, ts *httptest.Server, token, dbID string) connectionInfoResponse {
	t.Helper()
	status, _, resp := doJSON(t, "GET", ts.URL+"/api/v1/databases/"+dbID+"/connection-info", token, "")
	if status != http.StatusOK {
		t.Fatalf("connection info = %d, response %s", status, resp)
	}
	var out connectionInfoResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal connection info %s: %v", resp, err)
	}
	return out
}
