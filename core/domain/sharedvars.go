package domain

import "time"

// Project shared variables (shared-variables.md §2): one sealed value defined
// once for a whole Project — or narrowed to a single Environment of it — and
// referenced from any application's environment variables as {{shared.KEY}}.
//
// The value is sealed with the same secret.Box as an application's own env vars
// and is write-only: no read path unseals it, and no response carries it or a
// masked hint of it (§6, ENGINEERING rule 20).

// SharedVariable is one shared key/value in a project.
//
// EnvironmentID is the whole scope model: nil means the variable applies to
// every environment of the project, and a non-nil value narrows it to that one
// environment, shadowing a project-scoped row of the same key. Scope is a
// property of the VARIABLE, not of the reference, which is why promoting a
// value from project to environment scope needs no edit in any referencing
// application (§3).
// SharedVariableKey is a shared variable's name and scope without its sealed
// value, for callers that must be unable to reach one (project-export.md §4).
type SharedVariableKey struct {
	Key string
	// EnvironmentID is nil for project scope.
	EnvironmentID *string
}

type SharedVariable struct {
	ID        string
	ProjectID string
	// EnvironmentID is nil for project scope; otherwise an environment of
	// ProjectID (the service rejects one belonging to another project — the
	// foreign-key pair cannot express that, §2).
	EnvironmentID *string
	// Key matches the portable env-var charset [A-Za-z_][A-Za-z0-9_]* — the
	// same alphabet an application's own env keys use.
	Key string
	// ValueCT / ValueNonce are the sealed value (AES-256-GCM, secret.Box).
	ValueCT    []byte
	ValueNonce []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SharedVariableUsage is one application that references a shared variable —
// the used-by listing behind `GET /shared-variables/{id}/used-by` (§7).
//
// It is scope-accurate: an application whose own environment defines a
// shadowing row of the same key does not use the project-scoped variable and
// therefore never appears in its usage.
//
// RedeployPending is derived, never stored (§5): the variable changed after the
// environment this application is actually running was frozen onto the wire.
type SharedVariableUsage struct {
	ApplicationID   string
	ApplicationName string
	EnvironmentName string
	RedeployPending bool
}
