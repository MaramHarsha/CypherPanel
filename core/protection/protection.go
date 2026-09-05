package protection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ValidationError is a client-caused input error; handlers map it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "protection: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// ErrAlreadyDecided refuses a second decision on one gate. Decisions are
// once-only (§5); handlers map it to 409.
var ErrAlreadyDecided = errors.New("protection: this deploy has already been decided")

// ErrSelfApproval refuses a requester approving their own deploy while another
// qualifying approver exists (§5). Handlers map it to 403.
//
// The escape is deliberate and narrow: without it a solo operator (personas
// P1/P4) would wedge their own panel the moment they enabled protection, so the
// honest description of the rule is "two-person rule where two people exist".
var ErrSelfApproval = errors.New("protection: you cannot approve your own deploy while another approver exists")

// ErrPreviewProtection refuses protecting a preview environment (§9): previews
// are ordinary environments with a TTL, and freezing them would strand every
// open PR. Handlers map it to 409.
var ErrPreviewProtection = errors.New("protection: preview environments cannot be protected")

// breakGlassHistory bounds the grant listing a screen shows: active plus
// recent, not a year of incidents (§6).
const breakGlassHistory = 20

// approvalHistory bounds one environment's approval listing. The pending queue
// is short by construction — a parked deploy holds nobody's attention for long
// — but `state=all` asks for a decision history that grows for as long as the
// environment exists, and an unbounded answer is an answer that eventually
// stops arriving.
const approvalHistory = 100

// announceTimeout bounds one detached announcement.
const announceTimeout = 10 * time.Second

// Store is the persistence the service needs (consumer-defined; *store.Store
// satisfies it — ENGINEERING rule 6).
type Store interface {
	GetEnvironment(ctx context.Context, id string) (domain.Environment, error)
	GetApplication(ctx context.Context, id string) (domain.Application, error)
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	GetRevision(ctx context.Context, id string) (domain.Revision, error)
	GetUserByID(ctx context.Context, id string) (domain.User, error)

	GetEnvironmentProtection(ctx context.Context, envID string) (domain.EnvironmentProtection, error)
	SetEnvironmentProtection(ctx context.Context, p domain.EnvironmentProtection) (domain.EnvironmentProtection, error)

	CreateDeployApproval(ctx context.Context, deploymentID, envID, requestedBy, requiredRole string) (domain.DeployApproval, error)
	GetDeployApproval(ctx context.Context, deploymentID string) (domain.DeployApproval, error)
	ListDeployApprovalsByEnvironment(ctx context.Context, envID, state string, limit int32) ([]domain.DeployApproval, error)
	ListDeployApprovalsByApplication(ctx context.Context, appID string, deploymentIDs []string) (map[string]domain.DeployApproval, error)
	DecideDeployApproval(ctx context.Context, deploymentID, state, decidedBy, reason string) (domain.DeployApproval, error)
	CountQualifiedApprovers(ctx context.Context, projectID, minRole, excludeUserID string) (int64, error)

	CreateBreakGlassGrant(ctx context.Context, g domain.BreakGlassGrant) (domain.BreakGlassGrant, error)
	BreakGlassOpen(ctx context.Context, envID string, now time.Time) (bool, error)
	ListBreakGlassGrants(ctx context.Context, envID string, limit int32) ([]domain.BreakGlassGrant, error)
}

// Pipeline is the deploy pipeline seen from the gate (consumer-defined;
// *scheduler.Scheduler satisfies it). The transition a decision drives belongs
// to the scheduler — it owns the queue and its lock — so protection asks rather
// than reaches in (§4).
type Pipeline interface {
	ApproveDeployment(ctx context.Context, deploymentID string) (domain.Deployment, error)
	RejectDeployment(ctx context.Context, deploymentID, detail string) (domain.Deployment, error)
}

// Announcer writes the inbox items a parked deploy and its decision produce
// (consumer-defined; *inbox.Service satisfies it — §9). Optional: nil means the
// approvals are still reachable from the environment's approval queue and the
// deployment drawer, just unannounced.
type Announcer interface {
	RecordDeployAwaitingApproval(ctx context.Context, n inbox.DeployNotice) error
	RecordDeployApproved(ctx context.Context, n inbox.DeployNotice) error
	RecordDeployRejected(ctx context.Context, n inbox.DeployNotice) error
}

// Service is deploy protection: the policy document, the gate, the decisions
// and the recorded overrides. Construct with New.
type Service struct {
	store     Store
	pipeline  Pipeline
	announcer Announcer
	log       *slog.Logger
	// now is injected so freeze evaluation and grant expiry are deterministic
	// in tests (ENGINEERING rule 9). A freeze window is a statement about wall
	// clock; a test that cannot choose the wall clock cannot test one.
	now func() time.Time
	// detach runs an announcement away from the caller. Injected for the same
	// reason as the clock: a goroutine a test cannot schedule is a goroutine it
	// cannot assert on.
	detach func(ctx context.Context, fn func(context.Context))
}

// New wires the service. announcer may be nil; store, pipeline and log are
// required.
func New(st Store, p Pipeline, a Announcer, log *slog.Logger) *Service {
	return &Service{store: st, pipeline: p, announcer: a, log: log, now: time.Now, detach: detachGo}
}

// detachGo runs an announcement on a context that outlives the caller's, under
// a bounded timeout, and is the only goroutine this package starts.
//
// It exists because both call sites are on a hot path that must not wait for
// it: Park runs inside the scheduler's pipeline lock, so an inbox fan-out there
// would hold every other deploy behind a handful of writes; and a decision
// announcement runs on a request context that dies the moment the response is
// written. The announcement is best-effort either way — the approval is
// recorded and reachable from the environment's queue and the deployment
// drawer whether or not anyone is told — so the goroutine's contract is the
// same one notify.Manager.dispatch has (ENGINEERING rule 7): a lifetime capped
// by announceTimeout, and a failure that is logged rather than swallowed.
func detachGo(ctx context.Context, fn func(context.Context)) {
	base := context.WithoutCancel(ctx)
	go func() {
		c, cancel := context.WithTimeout(base, announceTimeout)
		defer cancel()
		fn(c)
	}()
}

// SetClock replaces the injected clock. Test-only in practice, but exported
// because the scheduler tests in another package need it too.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// ─── The protection document (§6) ───────────────────────────────────────────

// Get returns an environment's policy, or the DEFAULT document when it has
// never been protected. Not a 404: "not protected" is an answer, and a 404
// would make the UI invent one (§6).
func (s *Service) Get(ctx context.Context, envID string) (domain.EnvironmentProtection, error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		return domain.EnvironmentProtection{}, err
	}
	p, err := s.store.GetEnvironmentProtection(ctx, envID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.DefaultProtection(envID), nil
		}
		return domain.EnvironmentProtection{}, fmt.Errorf("protection: getting protection for %s: %w", envID, err)
	}
	return p, nil
}

// Document is a whole desired protection policy: the flags plus the COMPLETE
// window list. There is no partial-update path, because flags and windows
// describing different intents is exactly the state an operator cannot reason
// about (§6).
type Document struct {
	RequireApproval bool
	MinApproverRole string
	FreezeEnabled   bool
	Windows         []WindowInput
}

// WindowInput is one declared window. Ids are minted here, not supplied: a
// wholesale replace has no stable identity to preserve across it.
type WindowInput struct {
	StartDOW    time.Weekday
	StartMinute int
	EndDOW      time.Weekday
	EndMinute   int
	Timezone    string
}

// Set replaces an environment's whole protection document.
func (s *Service) Set(ctx context.Context, envID string, in Document) (domain.EnvironmentProtection, error) {
	env, err := s.store.GetEnvironment(ctx, envID)
	if err != nil {
		return domain.EnvironmentProtection{}, err
	}
	if env.Kind == domain.EnvPreview {
		return domain.EnvironmentProtection{}, ErrPreviewProtection
	}
	role := in.MinApproverRole
	if role == "" {
		role = domain.RoleOwner
	}
	if !domain.ValidRole(role) {
		return domain.EnvironmentProtection{}, invalid("min_approver_role must be one of member, admin, owner")
	}
	if len(in.Windows) > domain.MaxFreezeWindows {
		return domain.EnvironmentProtection{}, invalid(fmt.Sprintf(
			"at most %d freeze windows per environment", domain.MaxFreezeWindows))
	}
	windows := make([]domain.FreezeWindow, 0, len(in.Windows))
	for i, w := range in.Windows {
		if err := validateWindow(i, w); err != nil {
			return domain.EnvironmentProtection{}, err
		}
		windows = append(windows, domain.FreezeWindow{
			ID:            ids.New(ids.PrefixFreezeWindow),
			EnvironmentID: envID,
			StartDOW:      w.StartDOW,
			StartMinute:   w.StartMinute,
			EndDOW:        w.EndDOW,
			EndMinute:     w.EndMinute,
			Timezone:      w.Timezone,
		})
	}
	saved, err := s.store.SetEnvironmentProtection(ctx, domain.EnvironmentProtection{
		EnvironmentID:   envID,
		RequireApproval: in.RequireApproval,
		MinApproverRole: role,
		FreezeEnabled:   in.FreezeEnabled,
		Windows:         windows,
	})
	if err != nil {
		return domain.EnvironmentProtection{}, err
	}
	s.log.Info("environment protection set", "environment_id", envID,
		"require_approval", saved.RequireApproval, "min_approver_role", saved.MinApproverRole,
		"freeze_enabled", saved.FreezeEnabled, "windows", len(saved.Windows))
	return saved, nil
}

// validateWindow bounds one window and proves its zone loads. The zone is
// checked at WRITE time as well as at gate time, so an operator learns about a
// typo when they type it rather than when a deploy is refused (§5).
func validateWindow(i int, w WindowInput) error {
	where := fmt.Sprintf("windows[%d]", i)
	if w.StartDOW < time.Sunday || w.StartDOW > time.Saturday ||
		w.EndDOW < time.Sunday || w.EndDOW > time.Saturday {
		return invalid(where + ": start_dow and end_dow must be 0 (Sunday) through 6 (Saturday)")
	}
	if w.StartMinute < 0 || w.StartMinute >= domain.MinutesPerDay ||
		w.EndMinute < 0 || w.EndMinute >= domain.MinutesPerDay {
		return invalid(where + ": start_minute and end_minute must be 0 through 1439")
	}
	if w.StartDOW == w.EndDOW && w.StartMinute == w.EndMinute {
		return invalid(where + ": a window that starts and ends at the same moment is either empty or the whole week — say which")
	}
	if w.Timezone == "" {
		return invalid(where + ": timezone is required (an IANA name such as Europe/Berlin)")
	}
	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return invalid(fmt.Sprintf("%s: %q is not an IANA time zone name — try Europe/Berlin", where, w.Timezone))
	}
	return nil
}

// ─── Approvals (§6) ─────────────────────────────────────────────────────────

// Approvals is an environment's approval queue. An empty state means every
// state; the screens ask for "pending".
func (s *Service) Approvals(ctx context.Context, envID, state string) ([]domain.DeployApproval, error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		return nil, err
	}
	if state != "" && state != domain.ApprovalPending &&
		state != domain.ApprovalApproved && state != domain.ApprovalRejected {
		return nil, invalid("state must be one of pending, approved, rejected")
	}
	return s.store.ListDeployApprovalsByEnvironment(ctx, envID, state, approvalHistory)
}

// ApprovalFor returns one deployment's gate decision. store.ErrNotFound means
// the deployment was never gated, which is the ordinary case.
func (s *Service) ApprovalFor(ctx context.Context, deploymentID string) (domain.DeployApproval, error) {
	return s.store.GetDeployApproval(ctx, deploymentID)
}

// ApprovalsForApplication answers "which of THESE deployments has a gate
// decision" for one page of a Deployments tab in one round trip. The ids are
// the page the caller is decorating: an application deploys forever, so a
// lookup keyed only by the application would grow with its whole history.
func (s *Service) ApprovalsForApplication(ctx context.Context, appID string, deploymentIDs []string) (map[string]domain.DeployApproval, error) {
	return s.store.ListDeployApprovalsByApplication(ctx, appID, deploymentIDs)
}

// Approve lets a parked deploy through and re-enters it in the ordinary queue.
//
// The decision is recorded BEFORE the pipeline is asked to start, so a start
// that fails leaves an approved gate and a failed deployment — a state that
// reads correctly — rather than a running pipeline whose gate still says
// pending.
func (s *Service) Approve(ctx context.Context, deploymentID string, actor domain.User) (domain.Deployment, domain.DeployApproval, error) {
	ap, err := s.store.GetDeployApproval(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	if !ap.Pending() {
		return domain.Deployment{}, domain.DeployApproval{}, ErrAlreadyDecided
	}
	if err := s.guardSelfApproval(ctx, ap, actor); err != nil {
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	decided, err := s.store.DecideDeployApproval(ctx, deploymentID, domain.ApprovalApproved, actor.ID, "")
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return domain.Deployment{}, domain.DeployApproval{}, ErrAlreadyDecided
		}
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	s.log.Info("deploy approved", "deployment_id", deploymentID,
		"environment_id", ap.EnvironmentID, "decided_by", actor.ID, "required_role", ap.RequiredRole)

	dep, perr := s.pipeline.ApproveDeployment(ctx, deploymentID)
	s.announce(ctx, decided, actor, Announcer.RecordDeployApproved)
	if perr != nil {
		return dep, decided, perr
	}
	return dep, decided, nil
}

// Reject ends a parked deploy as failed, with a detail naming the rejecter.
//
// Self-rejection is allowed where self-approval is not: withdrawing your own
// request grants nothing, so the two-person rule has nothing to protect there,
// and refusing it would leave a requester unable to cancel their own deploy.
func (s *Service) Reject(ctx context.Context, deploymentID, reason string, actor domain.User) (domain.Deployment, domain.DeployApproval, error) {
	reason = strings.TrimSpace(reason)
	if err := validateReason(reason, domain.MaxRejectReason, "reason"); err != nil {
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	ap, err := s.store.GetDeployApproval(ctx, deploymentID)
	if err != nil {
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	if !ap.Pending() {
		return domain.Deployment{}, domain.DeployApproval{}, ErrAlreadyDecided
	}
	decided, err := s.store.DecideDeployApproval(ctx, deploymentID, domain.ApprovalRejected, actor.ID, reason)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return domain.Deployment{}, domain.DeployApproval{}, ErrAlreadyDecided
		}
		return domain.Deployment{}, domain.DeployApproval{}, err
	}
	s.log.Info("deploy rejected", "deployment_id", deploymentID,
		"environment_id", ap.EnvironmentID, "decided_by", actor.ID)

	dep, perr := s.pipeline.RejectDeployment(ctx, deploymentID, RejectionDetail(actor.Email, reason))
	s.announce(ctx, decided, actor, Announcer.RecordDeployRejected)
	if perr != nil {
		return dep, decided, perr
	}
	return dep, decided, nil
}

// RejectionDetail is the sentence a rejected deployment carries as its detail:
// "rejected by alex@acme.com: shipping Monday". Composed here so the deployment
// record, the inbox item and any CLI say the same thing (CLAUDE.md rule 4).
func RejectionDetail(rejecterEmail, reason string) string {
	who := rejecterEmail
	if who == "" {
		who = "an approver"
	}
	if reason == "" {
		return "rejected by " + who
	}
	return "rejected by " + who + ": " + reason
}

// guardSelfApproval enforces "two-person rule where two people exist" (§5).
// A requester may approve their own deploy only when nobody else in the
// project's team ranks at or above the snapshotted required role.
func (s *Service) guardSelfApproval(ctx context.Context, ap domain.DeployApproval, actor domain.User) error {
	if ap.RequestedBy == "" || ap.RequestedBy != actor.ID {
		return nil
	}
	env, err := s.store.GetEnvironment(ctx, ap.EnvironmentID)
	if err != nil {
		return fmt.Errorf("protection: getting environment %s: %w", ap.EnvironmentID, err)
	}
	others, err := s.store.CountQualifiedApprovers(ctx, env.ProjectID, ap.RequiredRole, actor.ID)
	if err != nil {
		return err
	}
	if others > 0 {
		return ErrSelfApproval
	}
	s.log.Info("sole qualifying approver decided their own deploy",
		"deployment_id", ap.DeploymentID, "user_id", actor.ID, "required_role", ap.RequiredRole)
	return nil
}

// ─── Break glass (§4) ───────────────────────────────────────────────────────

// OpenBreakGlass records a bounded freeze override. It does not bypass the
// approval gate, and it is never revoked early: the grant lapses on its own,
// which is what keeps "we are in an incident" from quietly becoming the
// operating mode.
func (s *Service) OpenBreakGlass(ctx context.Context, envID string, actor domain.User, reason string) (domain.BreakGlassGrant, error) {
	reason = strings.TrimSpace(reason)
	if err := validateReason(reason, domain.MaxBreakGlassReason, "reason"); err != nil {
		return domain.BreakGlassGrant{}, err
	}
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		return domain.BreakGlassGrant{}, err
	}
	now := s.now()
	g, err := s.store.CreateBreakGlassGrant(ctx, domain.BreakGlassGrant{
		ID:            ids.New(ids.PrefixBreakGlass),
		EnvironmentID: envID,
		OpenedBy:      actor.ID,
		Reason:        reason,
		ExpiresAt:     now.Add(domain.BreakGlassTTL),
	})
	if err != nil {
		return domain.BreakGlassGrant{}, err
	}
	g.OpenedByEmail = actor.Email
	s.log.Warn("break glass opened", "environment_id", envID, "grant_id", g.ID,
		"opened_by", actor.ID, "expires_at", g.ExpiresAt)
	return g, nil
}

// BreakGlassGrants lists an environment's grants, newest first — who broke
// glass, why, and when it lapses.
func (s *Service) BreakGlassGrants(ctx context.Context, envID string) ([]domain.BreakGlassGrant, error) {
	if _, err := s.store.GetEnvironment(ctx, envID); err != nil {
		return nil, err
	}
	return s.store.ListBreakGlassGrants(ctx, envID, breakGlassHistory)
}

// Now exposes the service's clock so a handler can say whether a grant is still
// active without reading a second time source.
func (s *Service) Now() time.Time { return s.now() }

// ─── helpers ────────────────────────────────────────────────────────────────

// validateReason bounds a required, single-line justification. Stored verbatim
// and rendered as text (§5): control characters are refused rather than
// stripped, so what is stored is what was typed.
func validateReason(reason string, max int, field string) error {
	if reason == "" {
		return invalid(field + " is required")
	}
	if utf8.RuneCountInString(reason) > max {
		return invalid(fmt.Sprintf("%s must be at most %d characters", field, max))
	}
	if strings.ContainsFunc(reason, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return invalid(field + " must be a single line")
	}
	return nil
}

// announce composes the notice for a decided approval and hands it to the
// inbox, detached. Best-effort: the decision is already recorded, so a failure
// to announce it is logged rather than returned (§9).
//
// record is a METHOD EXPRESSION on the Announcer interface — `Announcer.
// RecordDeployApproved`, taking the receiver as its first argument — not a
// method value bound at the call site. That is the whole point: a method value
// read off a nil interface dereferences it there and then, so `s.announcer.
// RecordDeployApproved` would panic in the caller before the nil check below
// could run, and this guard would be decoration. New documents a nil announcer
// as supported, so it has to actually be supported.
func (s *Service) announce(ctx context.Context, ap domain.DeployApproval, actor domain.User,
	record func(Announcer, context.Context, inbox.DeployNotice) error) {
	if s.announcer == nil || ap.RequestedBy == "" {
		return // a webhook deploy has nobody to tell: a push asked for it
	}
	s.detach(ctx, func(c context.Context) {
		dep, err := s.store.GetDeployment(c, ap.DeploymentID)
		if err != nil {
			s.log.Warn("composing decision notice", "deployment_id", ap.DeploymentID, "error", err)
			return
		}
		n, err := s.noticeFor(c, dep, ap.EnvironmentID)
		if err != nil {
			s.log.Warn("composing decision notice", "deployment_id", ap.DeploymentID, "error", err)
			return
		}
		n.RequestedBy = ap.RequestedBy
		n.RequesterEmail = ap.RequestedByEmail
		n.ActorEmail = actor.Email
		n.Reason = ap.Reason
		if err := record(s.announcer, c, n); err != nil {
			s.log.Error("recording decision inbox item", "deployment_id", ap.DeploymentID, "error", err)
		}
	})
}

// noticeFor gathers the denormalised fields an inbox item carries: the project
// it belongs to, and the application and commit that make the line readable
// without a lookup.
func (s *Service) noticeFor(ctx context.Context, dep domain.Deployment, environmentID string) (inbox.DeployNotice, error) {
	env, err := s.store.GetEnvironment(ctx, environmentID)
	if err != nil {
		return inbox.DeployNotice{}, fmt.Errorf("protection: getting environment %s: %w", environmentID, err)
	}
	n := inbox.DeployNotice{
		ProjectID:     env.ProjectID,
		ApplicationID: dep.ApplicationID,
		DeploymentID:  dep.ID,
	}
	if app, err := s.store.GetApplication(ctx, dep.ApplicationID); err == nil {
		n.ApplicationName = app.Name
	}
	if rev, err := s.store.GetRevision(ctx, dep.RevisionID); err == nil {
		n.Commit = rev.SourceCommit
	}
	return n, nil
}

// emailOf resolves a user id to an address for display, best-effort: an empty
// address renders as "pushed via webhook", which is the right reading for the
// one case that legitimately has no user.
func (s *Service) emailOf(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	u, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		s.log.Warn("resolving requester email", "user_id", userID, "error", err)
		return ""
	}
	return u.Email
}
