package domain

import "time"

// PanelTLS is the panel's ACME account: what every managed Proxy uses to obtain
// certificates for the domains routed to it (agent-identity-and-tls.md §4).
//
// It is desired state, not per-node configuration. One panel, one ACME account,
// carried to every agent inside DesiredState — so a server that joins tomorrow
// serves HTTPS with no extra step on the host, and turning TLS on is one action
// in one place instead of an environment variable on every box.
//
// Nothing here is a secret: the account email is published back by the CA in
// the registration, and the directory URL is well-known. The ACME account key
// — the part that is secret — is generated and held by Traefik on the serving
// node and never reaches the plane (ADR-004).
type PanelTLS struct {
	// ACMEEmail is the account email registered with the CA. Empty means TLS is
	// not configured: no certificate resolver is created on any node.
	ACMEEmail string
	// ACMECAServer overrides the ACME directory URL (Let's Encrypt staging, or
	// a private CA). Empty means Let's Encrypt production.
	ACMECAServer string
	UpdatedAt    time.Time
}

// Configured reports whether the panel has an ACME account, which is the same
// question as "will a node create a certificate resolver?".
func (t PanelTLS) Configured() bool { return t.ACMEEmail != "" }

// Route TLS states, reported on an Application (agent-identity-and-tls.md §5).
// This is a derived read model, never stored: it answers "what is this route
// actually being served as, right now, given what the panel knows", so the UI
// can say "serving over HTTP meanwhile" instead of claiming a certificate that
// was never issued (ui-principles §10: never fake certainty).
const (
	// TLSStateHTTPS: the route asks for HTTPS and the panel has an ACME
	// account, so the node configures a resolver and Traefik issues.
	TLSStateHTTPS = "https"
	// TLSStateHTTPOnlyNoResolver: the route asks for HTTPS but the panel has no
	// ACME account, so there is no resolver to issue with. The route is served
	// over plain HTTP — the deploy is unaffected — until TLS is configured.
	TLSStateHTTPOnlyNoResolver = "http_only_no_resolver"
	// TLSStateHTTPOnly: the route deliberately does not ask for HTTPS. Plain
	// HTTP is what was requested, so nothing is missing.
	TLSStateHTTPOnly = "http_only"
)

// RouteTLSState derives a route's TLS state from the route itself and whether
// the panel has an ACME account. An Application with no domain has no route at
// all and gets "" — the field is then omitted rather than answering a question
// nobody asked.
func RouteTLSState(route AppRoute, acmeConfigured bool) string {
	switch {
	case route.Domain == "":
		return ""
	case !route.HTTPS:
		return TLSStateHTTPOnly
	case acmeConfigured:
		return TLSStateHTTPS
	default:
		return TLSStateHTTPOnlyNoResolver
	}
}
