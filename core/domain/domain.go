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

// User is an account that can sign in to the control plane. Phase 1 bootstraps
// exactly one owner. The account model supports TOTP (threat-model §8 req 7):
// the sealed seed stays in the store; the domain only surfaces whether it is
// enrolled.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Role         string
	TOTPEnabled  bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
