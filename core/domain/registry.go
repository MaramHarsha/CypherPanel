package domain

import "time"

// Registry is a container registry the panel can authenticate to (ADR-008 path
// 3, registries.md).
//
// Optional by construction: a single-server build keeps its image in the local
// daemon and a multi-server one travels over the mTLS relay, so a registry is
// only for pulling a private base image or pushing builds somewhere the
// operator already runs. Team-scoped, because credentials for one customer's
// registry have no business being reachable by another team's applications.
type Registry struct {
	ID     string
	TeamID string
	Name   string
	// URL is a host, optionally with a namespace: ghcr.io, ghcr.io/acme,
	// registry.internal:5000. No scheme — a registry reference does not carry
	// one, and accepting https:// here would produce image names nothing can
	// pull.
	URL      string
	Username string
	// TokenCT/TokenNonce hold the sealed credential. It is opened to
	// authenticate a pull or a push and never returned by any route.
	TokenCT    []byte
	TokenNonce []byte
	CanPull    bool
	CanPush    bool

	LastTestAt     *time.Time
	LastTestOK     bool
	LastTestDetail string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RegistryUse is one application that depends on a registry, and how. Named
// rather than counted: "3 applications" is not something an operator can act
// on, and this is what a delete refusal has to say.
type RegistryUse struct {
	ApplicationID   string
	ApplicationName string
	EnvironmentName string
	ProjectName     string
	Pulls           bool
	Pushes          bool
}
