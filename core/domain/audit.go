package domain

import "time"

// The audit log (audit-log.md §2): one immutable row per sensitive action —
// who did what to which resource, from where, and whether it worked.
//
// Every field that names something outside this table is a SNAPSHOT taken when
// the action happened, never a live reference. An audit row must outlive the
// resource it describes: renaming an application cannot rewrite history, and
// deleting one cannot delete the record of the deletion.

// Audit actor kinds — the closed set of ways an action can be attributed.
const (
	// AuditActorUser is an interactive session: a person, signed in.
	AuditActorUser = "user"
	// AuditActorToken is a personal access token acting for its owner. The
	// owning user is recorded too — a token is a way to act, not a separate
	// identity — and the token id says which credential to revoke.
	AuditActorToken = "token"
	// AuditActorAgent is an enrolled agent acting for its server (enrollment,
	// certificate renewal). There is no user behind it.
	AuditActorAgent = "agent"
	// AuditActorSystem is the panel itself: a signed inbound webhook, a
	// sweeper, a template installer. Attributable to a mechanism, not a person.
	AuditActorSystem = "system"
	// AuditActorAnonymous is an unauthenticated caller — the failed sign-in
	// that never became a session. The label carries what it claimed to be.
	AuditActorAnonymous = "anonymous"
)

// Audit outcomes. Failure is an OUTCOME, not a separate verb, so "show me
// everything that was refused" stays a single predicate over the whole
// vocabulary (canvas 13t: "every failure is in the audit log").
const (
	AuditSuccess = "success"
	AuditFailure = "failure"
)

// AuditActor is who performed the action.
//
// Label is the email as it read at the time (or the agent/system description).
// It is deliberately a copy: an entry stays attributable by name after the
// account is deleted, which is what "audit entries stay" means (canvas 14k).
type AuditActor struct {
	Kind string
	// UserID is empty for agent, system and anonymous actors.
	UserID string
	// TokenID is set only for AuditActorToken — the credential to revoke.
	TokenID string
	Label   string
}

// AuditResource is what the action was performed on. Kind is a glossary noun
// ("application", "server", "shared_variable"); Name is a snapshot for the same
// reason Actor.Label is.
type AuditResource struct {
	Kind string
	ID   string
	Name string
}

// AuditEvent is one recorded action.
//
// TeamID/ProjectID/EnvironmentID are the ownership chain as it stood, resolved
// at write time from whichever link the caller knew. An empty TeamID means the
// action was panel-level — a server, a user, the mail settings — and only panel
// admins may read it (§5).
type AuditEvent struct {
	ID            string
	At            time.Time
	Action        string
	Outcome       string
	Actor         AuditActor
	Resource      AuditResource
	TeamID        string
	ProjectID     string
	EnvironmentID string
	// Detail carries structured extras: identifiers, key NAMES, counts, the
	// reason a decision was made. Never a secret value (§6).
	Detail map[string]any
	// TraceID is the X-Request-Id of the request that performed the action, so
	// a trace id from a screenshot finds what it did as well as what it logged.
	TraceID string
	// ClientIP is the address the panel attributed the request to — the same
	// value the login throttle counts against.
	ClientIP string
}
