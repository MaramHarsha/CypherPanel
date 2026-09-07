package domain

import "time"

// Status pages (status-pages.md). One page per project, published to people
// who are not signed in and never will be — which is why the public payload is
// assembled by its own mapper into its own type (§2) rather than by narrowing
// a DTO the panel already returns. Reusing an internal DTO and deleting fields
// is how the third field added next year becomes public by accident.

// StatusPage is the operator's configuration. Nothing here is public except
// the title and, when set, the domain.
type StatusPage struct {
	ID            string
	ProjectID     string
	Slug          string
	Title         string
	Enabled       bool
	Domain        string
	HTTPS         bool
	RouteServerID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Component resource kinds — the three Resources in the glossary, and the
// three that report observed status in the same vocabulary.
const (
	StatusResourceApplication  = "application"
	StatusResourceComposeStack = "compose_stack"
	StatusResourceDatabase     = "database"
)

// StatusPageComponent is one row on the page. Label is the ONLY name that
// becomes public: a resource's own name never leaves the panel, because a
// project named after a client publishes a customer list the day somebody
// flips the switch (§2).
type StatusPageComponent struct {
	ID            string
	StatusPageID  string
	ResourceKind  string
	ResourceID    string
	Label         string
	Position      int
	TrackingSince time.Time
}

// The four public state words (§6.1). Internal status is mapped down to these;
// nothing else is ever published.
const (
	PublicOperational = "operational"
	PublicDegraded    = "degraded"
	PublicDown        = "down"
	PublicUnknown     = "unknown"
)

// StatusInterval is a span during which one component held one public state.
// An Incident is not a separate record: it is an interval whose state is
// `down`. Message is the operator's one line of public text on it.
type StatusInterval struct {
	ID          string
	ComponentID string
	State       string
	StartedAt   time.Time
	EndedAt     *time.Time
	Message     string
}

// PublicStatusRank orders the states for the page banner: down beats degraded
// beats unknown beats operational. `unknown` outranks `operational`
// deliberately — "All systems operational" while a component is unreporting is
// the same lie in sentence form (§7).
func PublicStatusRank(state string) int {
	switch state {
	case PublicDown:
		return 3
	case PublicDegraded:
		return 2
	case PublicUnknown:
		return 1
	default:
		return 0
	}
}

// PublicStateFor maps an observed resource status to its public word, given
// whether the resource has ever been seen running and whether its server is
// currently reporting.
//
// The two arguments are the whole honesty of this feature. A stopped resource
// that has never run is `unknown` — the birth state, so adding a component does
// not open an instant permanent incident. A resource whose SERVER has gone
// silent is `unknown` whatever its last stored status said: an application's
// stored status is the last thing an agent said, and reporting "operational"
// for a host that fell off the internet an hour ago is precisely what
// ui-principles §10 exists to forbid, made public.
func PublicStateFor(observed string, everRan, serverReporting bool) string {
	if !serverReporting {
		return PublicUnknown
	}
	switch observed {
	case string(StatusRunning):
		return PublicOperational
	case string(StatusDegraded):
		return PublicDegraded
	case string(StatusError):
		return PublicDown
	case string(StatusStopped):
		if !everRan {
			return PublicUnknown
		}
		// The page describes what a visitor gets, not what the operator
		// intended. A stopped application answers nothing.
		return PublicDown
	case string(StatusDeploying):
		// Not a public state: zero-downtime rollout means the previous
		// revision is serving, so the caller keeps whatever it already
		// recorded. A deploy is not news and it is not the public's business.
		return ""
	default:
		return PublicUnknown
	}
}
