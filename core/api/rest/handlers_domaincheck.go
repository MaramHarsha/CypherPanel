package rest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// Domain checking: "is this domain actually reaching my app?"
//
// A domain can resolve perfectly and still be answered by something else
// listening on :80 — another web server, a hosting panel's default vhost, a
// stale CDN. Nothing in the panel could see that, so a deploy that "succeeded"
// and a domain that showed someone else's page looked identical, and the
// operator had no way to tell them apart.
//
// The probe runs from the control plane rather than the agent because it has to
// see the domain the way the public does: through whatever DNS and whatever
// front door the internet reaches, not from inside the host that is supposed to
// be serving it.

// servedByHeader mirrors agent/proxy.ServedByHeader. Duplicated rather than
// imported: core does not depend on the agent module, and this is a wire
// constant shared by contract.
const (
	servedByHeader = "X-Served-By"
	servedByValue  = "cypherpanel"
)

type domainCheckResponse struct {
	Domain string `json:"domain"`
	// Verdict is the single machine-readable answer the UI branches on.
	Verdict     string   `json:"verdict"`
	Summary     string   `json:"summary"`
	Remedy      string   `json:"remedy,omitempty"`
	ResolvedIPs []string `json:"resolved_ips"`
	// What answered, when anything did.
	HTTPStatus int    `json:"http_status,omitempty"`
	ServedBy   string `json:"served_by,omitempty"`
}

const (
	verdictOK          = "ok"          // our proxy answered
	verdictNoDomain    = "no_domain"   // the app has none configured
	verdictNoDNS       = "no_dns"      // nothing resolves
	verdictUnreachable = "unreachable" // resolves, nothing answers
	verdictForeign     = "served_by_other"
)

func (a *API) handleCheckApplicationDomain(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForApplication(ctx, r.PathValue("id"))
	}) {
		return
	}
	app, err := a.deps.Applications.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		a.deps.Log.Error("getting application for domain check", "error", err)
		writeError(w, http.StatusInternalServerError, "could not check the domain")
		return
	}

	writeJSON(w, http.StatusOK, checkDomain(r.Context(), strings.TrimSpace(app.Route.Domain), app.Route.HTTPS))
}

// checkDomain is pure enough to test: it takes a domain and reports what the
// public internet gets back.
func checkDomain(ctx context.Context, host string, https bool) domainCheckResponse {
	out := domainCheckResponse{Domain: host, ResolvedIPs: []string{}}
	if host == "" {
		out.Verdict = verdictNoDomain
		out.Summary = "This app has no domain, so it is only reachable inside CypherPanel."
		out.Remedy = "Add a domain in Settings to publish it."
		return out
	}

	// A domain field should hold a bare hostname, but an operator may paste
	// "example.com:8080" — resolve the name, and keep the port for the probe.
	name := host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil && h != "" {
		name = h
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lookupCtx, name)
	if err != nil || len(addrs) == 0 {
		out.Verdict = verdictNoDNS
		out.Summary = host + " does not resolve yet."
		out.Remedy = "Point an A record for this domain at your server's public IP address. DNS changes can take a few minutes to propagate."
		return out
	}
	out.ResolvedIPs = addrs

	scheme := "http"
	if https {
		scheme = "https"
	}
	// Redirects are not followed: the question is who answers THIS name, and a
	// redirect chain would silently move the answer somewhere else.
	client := &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+host+"/", nil)
	resp, err := client.Do(req)
	if err != nil {
		out.Verdict = verdictUnreachable
		out.Summary = host + " resolves to " + strings.Join(addrs, ", ") + ", but nothing answered."
		out.Remedy = "Check that the server is reachable on port 80 and 443, and that no firewall is blocking them."
		return out
	}
	defer func() { _ = resp.Body.Close() }()

	out.HTTPStatus = resp.StatusCode
	if resp.Header.Get(servedByHeader) == servedByValue {
		out.Verdict = verdictOK
		out.Summary = host + " is being served by this application."
		return out
	}

	out.ServedBy = resp.Header.Get("Server")
	out.Verdict = verdictForeign
	who := "something else on this server"
	if out.ServedBy != "" {
		who = out.ServedBy
	}
	out.Summary = host + " resolves, but the response came from " + who + " — not from CypherPanel."
	out.Remedy = "Another program is answering on port 80/443. Free those ports so the CypherPanel proxy can bind them, or point this domain at a server where it can."
	return out
}
