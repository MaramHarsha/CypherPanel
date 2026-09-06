package domain

// Compose Stacks (compose-stacks.md): the third Resource in an Environment,
// beside Application and Managed Database.
//
// The compose FILE is the desired state (ADR-005). The plane stores it, ships
// it, and never sends a command; the agent's own fixed `docker compose up -d`
// is a convergence, not an imperative verb.

import "time"

// ComposeStack is a multi-container Resource defined by a compose file the
// operator supplies and the panel runs as-is.
type ComposeStack struct {
	ID            string
	EnvironmentID string
	Name          string
	ServerID      string
	// DesiredRevisionID is nil until the first deploy; rollback re-points it.
	// There is no Deployment row: a stack has no build and no distribute stage,
	// so the pipeline's three-stage machinery would be ceremony around one
	// converge (spec §2).
	DesiredRevisionID *string
	Route             ComposeRoute
	// Status is observed, in the same six-word vocabulary an Application uses
	// (ui-principles §5). 'stopped' is the birth state.
	Status               string
	StatusDetail         string
	ObservedRevisionID   string
	StatusObservedAt     *time.Time
	CreatedAt, UpdatedAt time.Time
}

// ComposeRoute is which service the Proxy publishes, and where.
//
// A compose file's own Traefik labels cannot work: the managed Proxy runs the
// file provider only and no docker provider (ADR-004), so the stack names a
// service and the plane emits the same fragment an Application gets. An empty
// Domain means the stack publishes nothing through the Proxy.
type ComposeRoute struct {
	Domain  string
	Service string
	Port    int
	HTTPS   bool
}

// ComposeRevision is one immutable version of a stack's file: what rollback
// restores, rather than whatever the current row happens to say.
type ComposeRevision struct {
	ID          string
	StackID     string
	ComposeYAML string
	CreatedAt   time.Time
}

// ComposeEnvVar is a sealed variable for one stack. Compose interpolates
// ${KEY} from an env file the agent writes and removes, so a secret never has
// to live in the compose file the plane stores.
type ComposeEnvVar struct {
	Key        string
	ValueCT    []byte
	ValueNonce []byte
}
