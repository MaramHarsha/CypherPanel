// Package domain holds CypherPanel's core resource model as plain Go types,
// free of persistence and transport concerns. The vocabulary here is the
// glossary's (docs/glossary.md); the status values are ui-principles §5.
//
// Phase 1 covers only Server, User, and JoinToken — the entities the
// control-plane ↔ agent handshake needs. The model grows here as resources
// (Application, Managed Database, …) arrive in later phases.
package domain

import "time"

// ServerStatus is the single status vocabulary used in the DB, the API, and
// the UI (ui-principles §5). The control plane never invents certainty: a
// server whose agent has gone quiet is Unknown, not Running.
type ServerStatus string

const (
	StatusRunning   ServerStatus = "running"
	StatusDeploying ServerStatus = "deploying"
	StatusStopped   ServerStatus = "stopped"
	StatusError     ServerStatus = "error"
	StatusDegraded  ServerStatus = "degraded"
	StatusUnknown   ServerStatus = "unknown"
)

// Server is a host running cypher-agent, identified by its agent certificate
// (CN = ID), never by a stored credential (ADR-002).
type Server struct {
	ID     string
	Name   string
	Status ServerStatus
	Driver string
	// Role is what the agent asserted in its last heartbeat: "all" (default),
	// "builder" (builds only), or "worker" (runs apps, never builds) —
	// builder-role-and-relay.md §1. Routing input, not an authorization.
	Role         string
	AgentVersion string
	Hostname     string
	// EnrolledAt is nil until the agent completes enrollment; it distinguishes
	// "awaiting enrollment" from "enrolled but currently offline".
	EnrolledAt *time.Time
	// LastSeenAt is the time of the most recent heartbeat, nil if never seen.
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Enrolled reports whether an agent has completed enrollment for this server.
func (s Server) Enrolled() bool { return s.EnrolledAt != nil }

// Server role vocabulary (builder-role-and-relay.md §1).
const (
	RoleAll     = "all"     // builds and runs (single-server default)
	RoleBuilder = "builder" // builds only; never runs Applications or the Proxy
	RoleWorker  = "worker"  // runs Applications; build work is never routed here
)

// Builds reports whether this server's role accepts build work. An unset role
// (rows or fakes predating the role column) means "all"; anything outside the
// vocabulary never attracts builds — routing must not trust a value the
// heartbeat path wouldn't persist.
func (s Server) Builds() bool {
	return s.Role == RoleAll || s.Role == RoleBuilder || s.Role == ""
}

// Runs reports whether this server's role accepts Applications and Managed
// Databases. A builder-only agent is deliberately constructed without an
// application driver and rejects rollout work, so placing a resource there
// would create the row and then strand it in a failed deployment. An unset
// role means "all", matching Builds.
func (s Server) Runs() bool {
	return s.Role != RoleBuilder
}

// User is an account that can sign in to the control plane. Phase 1 bootstraps
// exactly one owner. The account model supports TOTP (threat-model §8 req 7):
// the sealed seed stays in the store; the domain only surfaces whether it is
// enrolled.
// EmailChange is a pending move of an account to a new sign-in address. It is
// spent once and expires; the address only moves when the new mailbox proves it
// can receive mail (docs/features/panel-mail.md §3).
type EmailChange struct {
	ID         string
	UserID     string
	NewEmail   string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// Avatar is a profile photo: small, already-encoded image bytes plus the type
// they were recognised as. Never the type the uploader claimed.
type Avatar struct {
	ContentType string
	Bytes       []byte
	ETag        string
	UpdatedAt   time.Time
}

type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	// DisplayName is what teammates see; empty means the panel has no name for
	// this person yet and falls back to the address.
	DisplayName string
	// Timezone is an IANA name the UI reads timestamps in. Empty means UTC,
	// which is what the panel printed before anyone could choose.
	Timezone    string
	TOTPEnabled bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// JoinToken is a single-use, short-lived enrollment credential bound to one
// pending server (threat-model §5.3). Only the hash of the secret half is ever
// persisted.
type JoinToken struct {
	ID         string
	ServerID   string
	TokenHash  []byte
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// APIToken is a personal access token: a credential that authenticates as its
// owning user, inheriting that user's role and team memberships, narrowed
// further by its abilities. Only the hash is persisted; the raw token is shown
// exactly once at creation.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	Abilities  []Ability
	LastUsedAt *time.Time
	ExpiresAt  *time.Time // nil = never expires
	CreatedAt  time.Time
}

// Ability scopes what a personal access token may do (feature-matrix V1). A
// token's authority is the intersection of its owner's role and these: an
// ability never grants access to a resource the user cannot already reach.
type Ability string

const (
	// AbilityRead permits safe, non-mutating requests (GET/HEAD).
	AbilityRead Ability = "read"
	// AbilityWrite permits creating, changing, and deleting resources.
	AbilityWrite Ability = "write"
	// AbilityDeploy permits triggering deploys and rollbacks — separated from
	// write so a CI credential can ship code without also being able to delete
	// the application it deploys.
	AbilityDeploy Ability = "deploy"
)

// ValidAbility reports whether a is a known ability.
func ValidAbility(a Ability) bool {
	switch a {
	case AbilityRead, AbilityWrite, AbilityDeploy:
		return true
	}
	return false
}

// AllAbilities is the full set — what a session holds, and the default for a
// token created without an explicit choice.
func AllAbilities() []Ability { return []Ability{AbilityRead, AbilityWrite, AbilityDeploy} }

// Has reports whether the set contains want.
func HasAbility(set []Ability, want Ability) bool {
	for _, a := range set {
		if a == want {
			return true
		}
	}
	return false
}

// Session is one live sign-in. The token hash is deliberately absent: nothing
// outside the store ever needs it, and a session list must never carry
// material that could be replayed.
type Session struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}
