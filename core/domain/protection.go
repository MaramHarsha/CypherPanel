package domain

import "time"

// Deploy protection (deploy-protection.md §2): desired state about DEPLOYING.
// An Environment may declare who must approve a deploy there and when deploys
// are not allowed at all; the plane enforces both at the single point where a
// Deployment is born, before any work item reaches an agent.
//
// Protection is off by default: an environment with no EnvironmentProtection
// row behaves exactly as it did before the feature existed (ENGINEERING rule
// 17), which is why DefaultProtection is a value and not a stored row.

// Approval states. A gate decision is made exactly once (§5).
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// BreakGlassTTL is how long a break-glass grant suspends an environment's
// freeze. A constant, not a setting (§4): long enough for an incident — a
// rollback, a hotfix, a second hotfix — and short enough that it cannot quietly
// become the operating mode.
const BreakGlassTTL = 30 * time.Minute

// MaxFreezeWindows bounds the windows one environment may declare (§5). A
// weekly calendar nobody can read is not a control.
const MaxFreezeWindows = 8

// MaxBreakGlassReason bounds a grant's reason, in characters (§5).
const MaxBreakGlassReason = 500

// MaxRejectReason bounds a rejection's reason, in characters. The same bound
// as a break-glass reason, for the same purpose: it is a sentence a person
// reads, not a document.
const MaxRejectReason = 500

// MinutesPerDay and MinutesPerWeek are the units freeze evaluation counts in:
// a window is a half-open interval of minute-of-week, in its own zone (§4).
const (
	MinutesPerDay  = 24 * 60
	MinutesPerWeek = 7 * MinutesPerDay
)

// EnvironmentProtection is one environment's policy. Keyed by the environment,
// so "at most one policy per environment" is an invariant the database holds.
type EnvironmentProtection struct {
	EnvironmentID string
	// RequireApproval parks a deploy until someone at or above
	// MinApproverRole approves it.
	RequireApproval bool
	// MinApproverRole is a role from the ranked set (RoleRank).
	MinApproverRole string
	// FreezeEnabled is the master switch over Windows. Turning it off keeps
	// the declared windows, so an operator can re-arm them without retyping.
	FreezeEnabled bool
	// Windows is the complete freeze calendar. Empty means no freeze even when
	// FreezeEnabled is true — the switch and the calendar are both required.
	Windows   []FreezeWindow
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DefaultProtection is what an environment that has never been protected
// answers with: not protected, no windows. Returned instead of a 404 because
// "not protected" is an answer, and a 404 would make a screen invent one (§6).
func DefaultProtection(environmentID string) EnvironmentProtection {
	return EnvironmentProtection{
		EnvironmentID:   environmentID,
		RequireApproval: false,
		MinApproverRole: RoleOwner,
		FreezeEnabled:   false,
		Windows:         []FreezeWindow{},
	}
}

// FreezeWindow is a weekly recurring interval during which deploys are refused.
// It is stored and evaluated as WALL CLOCK in its own zone, so a DST change
// makes it an hour shorter or longer in absolute terms twice a year — which is
// what "no deploys after six on Friday" means to whoever wrote it (§4).
//
// It may WRAP the week: StartDOW=Friday 18:00 → EndDOW=Monday 08:00 is one
// window, not two.
type FreezeWindow struct {
	ID            string
	EnvironmentID string
	// StartDOW / EndDOW are time.Weekday values (0=Sunday … 6=Saturday).
	StartDOW time.Weekday
	// StartMinute / EndMinute are minutes past local midnight, 0–1439.
	StartMinute int
	EndDOW      time.Weekday
	EndMinute   int
	// Timezone is an IANA name, e.g. "Europe/Berlin".
	Timezone  string
	CreatedAt time.Time
}

// StartOfWeekMinute and EndOfWeekMinute project a window onto minute-of-week,
// the single coordinate freeze evaluation compares in (§4).
func (w FreezeWindow) StartOfWeekMinute() int {
	return int(w.StartDOW)*MinutesPerDay + w.StartMinute
}

func (w FreezeWindow) EndOfWeekMinute() int {
	return int(w.EndDOW)*MinutesPerDay + w.EndMinute
}

// Wraps reports whether the window crosses the week boundary (Sunday 00:00).
func (w FreezeWindow) Wraps() bool { return w.StartOfWeekMinute() > w.EndOfWeekMinute() }

// DeployApproval is the one gate decision a parked Deployment carries. Keyed by
// the deployment, so "at most one decision per deployment" is a database
// invariant rather than a rule a service must remember (§2).
type DeployApproval struct {
	DeploymentID  string
	EnvironmentID string
	// RequestedBy is empty for a webhook deploy: a push has no panel user
	// behind it. It also empties if that user is later deleted.
	RequestedBy string
	// RequestedByEmail is joined for display; empty when not loaded or when
	// there is no requester.
	RequestedByEmail string
	// RequiredRole is a SNAPSHOT of the policy's MinApproverRole at park time:
	// relaxing the policy while a deploy is parked must not relax the deploy
	// already parked (§2).
	RequiredRole string
	State        string
	DecidedBy    string
	// DecidedByEmail is joined for display; empty when not loaded or undecided.
	DecidedByEmail string
	DecidedAt      *time.Time
	// Reason is the rejecter's sentence, stored verbatim and rendered as text.
	// Empty on an approval — approving needs no justification, refusing does.
	Reason    string
	CreatedAt time.Time
}

// Pending reports whether the gate is still open on this deployment.
func (a DeployApproval) Pending() bool { return a.State == ApprovalPending }

// BreakGlassGrant is a bounded, recorded override of an environment's freeze.
// It does not bypass the approval gate — two independent controls (§4).
type BreakGlassGrant struct {
	ID            string
	EnvironmentID string
	OpenedBy      string
	// OpenedByEmail is joined for display; empty when not loaded.
	OpenedByEmail string
	Reason        string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// Active reports whether the grant still suspends the freeze at now.
func (g BreakGlassGrant) Active(now time.Time) bool { return g.ExpiresAt.After(now) }

// DeployAdmission is the gate's whole answer, handed to the scheduler as a
// plain value (§4). A zero DeployAdmission is "clear", which is what an
// unprotected environment — and a plane with no gate wired — produces.
type DeployAdmission struct {
	// Frozen refuses the deploy outright: nothing is written, so no orphan
	// Revision is left behind. A hard "not now" is more useful than parking
	// something that would have to be refused later anyway.
	Frozen bool
	// FreezeDetail names the window and when it lifts, e.g.
	// "production is frozen until Mon 08:00 Europe/Berlin". Carried into the
	// 409 body so a failed GitHub delivery is diagnosable from the response
	// alone.
	FreezeDetail string
	// NeedsApproval parks the deploy: the Revision and Deployment are created,
	// the deployment is set to DeployAwaitingApproval, and no work item is
	// published.
	NeedsApproval bool
	// RequiredRole is the rank that must approve, snapshotted onto the
	// approval row at park time.
	RequiredRole string
}

// Clear reports whether the deploy may proceed immediately.
func (a DeployAdmission) Clear() bool { return !a.Frozen && !a.NeedsApproval }
