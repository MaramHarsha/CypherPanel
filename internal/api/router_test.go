package api

import (
	"testing"

	"github.com/MaramHarsha/CypherPanel/internal/config"
)

// NewRouter registers a mix of static and parameterised segments under the same
// prefixes (/webhooks/deliveries vs /webhooks/:hookid, /backup/destinations vs
// /accounts/:id). Gin panics at registration on an unresolvable conflict, so
// simply building the router is the assertion — and it catches the mistake at
// test time instead of at process start on a production box.
func TestNewRouterRegistersWithoutConflicts(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()

	r := NewRouter(Deps{
		Config:      config.Core{Env: config.EnvDevelopment},
		Auth:        &AuthHandler{},
		Tasks:       &TasksHandler{},
		Servers:     &ServersHandler{},
		Packages:    &PackagesHandler{},
		Accounts:    &AccountsHandler{},
		Plugins:     &PluginsHandler{},
		Resellers:   &ResellersHandler{},
		Databases:   &DatabasesHandler{},
		FTP:         &FTPHandler{},
		FileManager: &FileManagerHandler{},
		DNS:         &DNSHandler{},
		Metrics:     &MetricsHandler{},
		AuditLog:    &AuditHandler{},
		Cron:        &CronHandler{},
		Mail:        &MailHandler{},
		Terminal:    &TerminalHandler{},
		Backups:     &BackupsHandler{},
		Webhooks:    &WebhooksHandler{},
	})

	routes := r.Routes()
	if len(routes) == 0 {
		t.Fatal("expected routes to be registered")
	}

	// Spot-check that the surfaces most at risk of being swallowed by a
	// wildcard are actually reachable as distinct routes.
	want := map[string]string{
		"GET":  "/api/v1/admin/webhooks/deliveries",
		"POST": "/api/v1/admin/accounts/:id/backups",
	}
	for method, path := range want {
		found := false
		for _, rt := range routes {
			if rt.Method == method && rt.Path == path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("route %s %s was not registered", method, path)
		}
	}
}
