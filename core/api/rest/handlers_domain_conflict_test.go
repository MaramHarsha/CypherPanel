package rest

// Two applications cannot serve one hostname on one server.
//
// THE FAILURE THIS EXISTS TO STOP, reported from a real panel: an operator ran
// a site on their apex domain, then installed a second application in the same
// project, and the first domain started answering 404. Nothing in the panel
// refused the collision and nothing in Traefik does either — it ends up with
// two routers whose rules are both `Host(`example.com`)`, serves one, and the
// other silently stops. A successful deploy and a dead site, with no error
// anywhere to read.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func TestASecondApplicationCannotClaimAServedDomain(t *testing.T) {
	ts, apps := newTestServerApps(t)
	defer ts.Close()
	token := login(t, ts)

	// Somebody already serves it, on the same server, in a team the caller is
	// in — so the refusal may name it.
	apps.domainClaims = []domain.DomainClaim{{
		ApplicationID: "app_first", ApplicationName: "landingpage",
		ServerID: "srv_test", ProjectID: "prj_test", TeamID: "tm_default",
	}}

	body := `{"name":"second","source":{"kind":"github","repo":"https://github.com/acme/web","branch":"main"},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusConflict {
		t.Fatalf("a second claim on a served domain should be 409, got %d: %s", status, resp)
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(resp, &e)
	// The refusal must name the remedy, or it is a dead end (ui-principles §11).
	if !strings.Contains(e.Error, "subdomain") {
		t.Errorf("the refusal does not offer a way forward: %s", e.Error)
	}
	if !strings.Contains(e.Error, "landingpage") {
		t.Errorf("the caller is in the owning team, so the refusal should name it: %s", e.Error)
	}
}

// A claim in another team still refuses, because the collision is physical.
//
// Whether it is NAMED depends on the caller: a panel owner is a member of every
// team by design (core/teams RoleInTeam's owner bypass) and sees it, while a
// member of no relevant team does not — a create dialog must not become a way
// to enumerate other teams' hostnames. The harness signs in as a panel owner,
// so this asserts the refusal happens and that the owner is told what it is;
// the non-member half is enforced by domainConflictMessage requiring a
// NON-EMPTY role, because RoleInTeam reports a non-member as ("", nil) and
// checking only the error named the application to everyone.
func TestADomainConflictInAnotherTeamStillRefuses(t *testing.T) {
	ts, apps := newTestServerApps(t)
	defer ts.Close()
	token := login(t, ts)

	apps.domainClaims = []domain.DomainClaim{{
		ApplicationID: "app_other", ApplicationName: "someone-elses-app",
		ServerID: "srv_test", ProjectID: "prj_other", TeamID: "tm_not_mine",
	}}

	body := `{"name":"second","source":{"kind":"github","repo":"https://github.com/acme/web","branch":"main"},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusConflict {
		t.Fatalf("the collision is physical and must refuse regardless of team, got %d: %s", status, resp)
	}
	if !strings.Contains(string(resp), "subdomain") {
		t.Errorf("the refusal does not offer a way forward: %s", resp)
	}
}

// A claim on a DIFFERENT server is not a conflict: one node, one Traefik, one
// rule table. Serving the same hostname from two hosts is how a migration or a
// blue/green cutover works, and refusing it would forbid something real.
func TestTheSameDomainOnAnotherServerIsAllowed(t *testing.T) {
	ts, apps := newTestServerApps(t)
	defer ts.Close()
	token := login(t, ts)

	apps.domainClaims = []domain.DomainClaim{{
		ApplicationID: "app_elsewhere", ApplicationName: "old-host",
		ServerID: "srv_somewhere_else", ProjectID: "prj_test", TeamID: "tm_default",
	}}

	body := `{"name":"cutover","source":{"kind":"github","repo":"https://github.com/acme/web","branch":"main"},` +
		`"build":{"kind":"dockerfile","dockerfile_path":"./Dockerfile","context":"."},` +
		`"runtime":{"server_id":"srv_test","port":8080},"route":{"domain":"example.com"}}`
	status, _, resp := doJSON(t, "POST", ts.URL+"/api/v1/environments/env_test/applications", token, body)
	if status != http.StatusCreated {
		t.Fatalf("the same domain on another server must be allowed, got %d: %s", status, resp)
	}
}
