// Package inbox is the panel's own record of what happened to what you own
// (notification-inbox.md). It adds no event source: it is a second AUDIENCE on
// the one place an observed outcome becomes news — notify.Manager.dispatch —
// durable and per-user instead of best-effort and per-channel (spec §1).
//
// The two rules that shape everything here:
//
//   - severity `error` is immediate and individual; severity `info` is digested
//     into one row per (user, project, kind, UTC day), so a hundred green
//     deploys are one unread rather than a hundred (spec §3);
//   - a user must never hold an item for a team they do not belong to.
//     Recipients come from explicit team_members rows, and tenancy on the read
//     side is a column, not a resolver (spec §4, §5).
package inbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// Paging bounds for the feed (spec §6). Keyset, not offset: a feed gains rows
// at the head while you page it, and offsets then skip rows.
const (
	defaultLimit = 20
	maxLimit     = 100
)

// ValidationError marks bad input (surfaced as HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Store is the persistence the service needs (consumer-defined; *store.Store
// satisfies it — ENGINEERING rule 6).
type Store interface {
	ListInboxRecipients(ctx context.Context, projectID, kind string) ([]string, error)
	ListPanelInboxRecipients(ctx context.Context, kind string) ([]string, error)
	// Deploy protection addresses two narrower audiences than "the team"
	// (deploy-protection.md §9): the members who could act on a parked deploy,
	// and the one person who asked for it.
	ListApprovalInboxRecipients(ctx context.Context, projectID, kind, minRole string) ([]string, error)
	ListInboxRecipientIfMember(ctx context.Context, projectID, kind, userID string) ([]string, error)
	// Team invitations and access requests address a TEAM rather than a
	// project (invitations-and-access-requests.md §6): its members at or above
	// a rank, or one named member of it.
	ListTeamInboxRecipients(ctx context.Context, teamID, kind, minRole string) ([]string, error)
	ListTeamInboxRecipientIfMember(ctx context.Context, teamID, kind, userID string) ([]string, error)
	InsertInboxItems(ctx context.Context, f store.InboxFanout) error
	InsertTeamInboxItems(ctx context.Context, f store.InboxFanout) error
	InsertPanelInboxItems(ctx context.Context, f store.InboxFanout) error
	UpsertInboxDigests(ctx context.Context, f store.InboxFanout) error
	BumpInboxDigestTotals(ctx context.Context, dedupeKey, focusID string) error
	PruneInboxItems(ctx context.Context, userIDs []string, keep int64) error

	ListInboxItems(ctx context.Context, userID string, unreadOnly bool, limit int32) ([]domain.InboxItem, error)
	ListInboxItemsBefore(ctx context.Context, userID string, unreadOnly bool, before string, limit int32) ([]domain.InboxItem, error)
	CountUnreadInboxItems(ctx context.Context, userID string) (int64, error)
	MarkInboxItemRead(ctx context.Context, userID, itemID string) (domain.InboxItem, error)
	MarkAllInboxItemsRead(ctx context.Context, userID string) (int64, error)

	GetInboxPreferences(ctx context.Context, userID string) (domain.InboxPreferences, error)
	SetInboxPreferences(ctx context.Context, userID string, muted []string) (domain.InboxPreferences, error)
}

// Service records observed outcomes as per-user items and serves the caller's
// own inbox. Construct with New.
type Service struct {
	store Store
	log   *slog.Logger
	// now is injected so the UTC-day digest window is deterministic in tests
	// (ENGINEERING rule 9).
	now func() time.Time
}

// New wires the service.
func New(st Store, log *slog.Logger) *Service {
	return &Service{store: st, log: log, now: time.Now}
}

// ─── Recording (spec §4) ────────────────────────────────────────────────────

// Record persists one observed outcome as an item for every member of the team
// owning the event's project who has not muted its kind. It is called from
// notify.Manager.dispatch, which already runs detached with its own timeout, so
// the database work here is off the scheduler's path and cannot slow a deploy.
//
// Failure is immediate and individual; success rolls into the day's digest, and
// a failure additionally raises that digest's denominator so "Backups: 2/3
// succeeded" sits honestly beside the item explaining the missing third.
func (s *Service) Record(ctx context.Context, ev domain.NotifyEvent) error {
	// An event with no project cannot be fanned out to a team, and an event key
	// outside the taxonomy is not something a preference list can name. Both
	// are silent no-ops: the outcome is already recorded and logged elsewhere.
	if ev.ProjectID == "" || !domain.ValidEventType(ev.Type) {
		return nil
	}

	recipients, err := s.store.ListInboxRecipients(ctx, ev.ProjectID, ev.Type)
	if err != nil {
		return fmt.Errorf("inbox: resolving recipients for %s: %w", ev.ProjectID, err)
	}
	if len(recipients) == 0 {
		return nil
	}

	if ev.Level == domain.NotifyError {
		if err := s.recordImmediate(ctx, ev, recipients); err != nil {
			return err
		}
	} else if err := s.recordDigest(ctx, ev, recipients); err != nil {
		return err
	}

	if err := s.store.PruneInboxItems(ctx, recipients, domain.InboxRetention); err != nil {
		return fmt.Errorf("inbox: pruning: %w", err)
	}
	return nil
}

// PanelUpdate is what the update check tells the inbox when it first sees a
// release newer than the running one (control-plane-hardening.md §3).
type PanelUpdate struct {
	Current  string // the running version, e.g. v0.4.0
	Latest   string // the newer release, e.g. v0.5.0
	Kind     string // patch | minor | major
	NotesURL string // where to read about it; carried in the body, never as a link
}

// RecordPanelUpdate writes one panel.update_available item to every panel
// owner and team owner who has not muted the kind — once per version: the
// dedupe key is the version, so seeing the same release again (a restart, the
// next poll) writes nothing. Severity info, but immediate rather than digested:
// a rollup of "releases seen today" would say nothing useful. The item has no
// project and no link — there is no in-panel changelog route yet — so the
// notes URL rides in the body.
func (s *Service) RecordPanelUpdate(ctx context.Context, u PanelUpdate) error {
	if u.Latest == "" {
		return nil
	}
	recipients, err := s.store.ListPanelInboxRecipients(ctx, domain.InboxKindPanelUpdateAvailable)
	if err != nil {
		return fmt.Errorf("inbox: resolving panel recipients: %w", err)
	}
	if len(recipients) == 0 {
		return nil
	}
	f := store.InboxFanout{
		IDs:       mintIDs(len(recipients)),
		UserIDs:   recipients,
		Kind:      domain.InboxKindPanelUpdateAvailable,
		Severity:  string(domain.NotifyInfo),
		Title:     fmt.Sprintf("CypherPanel %s is available", u.Latest),
		Body:      clampBody(panelUpdateBody(u)),
		DedupeKey: domain.InboxKindPanelUpdateAvailable + ":" + u.Latest,
	}
	if err := s.store.InsertPanelInboxItems(ctx, f); err != nil {
		return fmt.Errorf("inbox: inserting panel items: %w", err)
	}
	if err := s.store.PruneInboxItems(ctx, recipients, domain.InboxRetention); err != nil {
		return fmt.Errorf("inbox: pruning: %w", err)
	}
	return nil
}

// ─── Deploy protection (deploy-protection.md §9) ────────────────────────────

// DeployNotice is what deploy protection tells the inbox about one parked
// deployment or one decision on it. Every field is already on screen elsewhere,
// so nothing here is a secret; the item is DENORMALISED like every other one —
// it states what happened, it does not point at current state (spec §2).
type DeployNotice struct {
	ProjectID     string
	ApplicationID string
	// ApplicationName and Commit make the line readable without a lookup:
	// "web · c99d2e1". Commit may be empty (an image-source app has no commit).
	ApplicationName string
	Commit          string
	DeploymentID    string
	// RequiredRole addresses the awaiting-approval item: only members at or
	// above it can act, so only they are told.
	RequiredRole string
	// RequestedBy is the user the DECISION items are addressed to. Empty for a
	// webhook deploy, in which case there is nobody to tell and the write is a
	// no-op.
	RequestedBy string
	// RequesterEmail names that person in the awaiting-approval body, so an
	// approver reads "requested by alex@acme.com" rather than a user id.
	RequesterEmail string
	// ActorEmail is who decided, named in the body of a decision item.
	ActorEmail string
	// Reason is the rejecter's sentence, carried verbatim into the body.
	Reason string
}

// RecordDeployAwaitingApproval tells the people who can act — the project's
// team members at or above RequiredRole — that a deploy is parked.
//
// Severity is info, not error: a deploy waiting for a person is the control
// working, not a fault. It is written immediately rather than digested, because
// a rollup of "3 deploys awaited approval today" is unactionable, and the
// digest windows are deliberately only defined for the two terminal outcome
// families (notification-inbox.md §3).
func (s *Service) RecordDeployAwaitingApproval(ctx context.Context, n DeployNotice) error {
	if n.ProjectID == "" || n.DeploymentID == "" {
		return nil
	}
	role := n.RequiredRole
	if !domain.ValidRole(role) {
		// An unknown rank must never widen the audience: fall back to the
		// narrowest one the role set has.
		role = domain.RoleOwner
	}
	recipients, err := s.store.ListApprovalInboxRecipients(ctx, n.ProjectID,
		domain.InboxKindDeployAwaitingApproval, role)
	if err != nil {
		return fmt.Errorf("inbox: resolving approval recipients for %s: %w", n.ProjectID, err)
	}
	return s.writeDeployItems(ctx, recipients, n, domain.InboxKindDeployAwaitingApproval,
		"Deploy awaits approval — "+shortID(n.DeploymentID), awaitingBody(n))
}

// RecordDeployApproved tells the requester their deploy was let through.
func (s *Service) RecordDeployApproved(ctx context.Context, n DeployNotice) error {
	return s.recordDeployDecision(ctx, n, domain.InboxKindDeployApproved,
		"Deploy approved — "+shortID(n.DeploymentID), decisionBody(n, "Approved"))
}

// RecordDeployRejected tells the requester their deploy was refused, and by
// whom. Still severity info: a governance decision is not an infrastructure
// fault, and the deploy's own failure is recorded on the deployment row.
func (s *Service) RecordDeployRejected(ctx context.Context, n DeployNotice) error {
	return s.recordDeployDecision(ctx, n, domain.InboxKindDeployRejected,
		"Deploy rejected — "+shortID(n.DeploymentID), decisionBody(n, "Rejected"))
}

// recordDeployDecision writes one item to the requester, if there is one and
// they are still a member who has not muted the kind.
func (s *Service) recordDeployDecision(ctx context.Context, n DeployNotice, kind, title, body string) error {
	if n.ProjectID == "" || n.DeploymentID == "" || n.RequestedBy == "" {
		// A webhook deploy has nobody to tell: the push, not a person, asked
		// for it.
		return nil
	}
	recipients, err := s.store.ListInboxRecipientIfMember(ctx, n.ProjectID, kind, n.RequestedBy)
	if err != nil {
		return fmt.Errorf("inbox: resolving requester %s: %w", n.RequestedBy, err)
	}
	return s.writeDeployItems(ctx, recipients, n, kind, title, body)
}

// writeDeployItems is the shared fan-out: one immediate item per recipient,
// deduped on (user, deployment, kind) so a redelivered decision is a no-op
// (ENGINEERING rule 12), then the same prune every other write runs.
func (s *Service) writeDeployItems(ctx context.Context, recipients []string, n DeployNotice, kind, title, body string) error {
	if len(recipients) == 0 {
		return nil
	}
	link, label := deploymentLink(n)
	f := store.InboxFanout{
		IDs:       mintIDs(len(recipients)),
		UserIDs:   recipients,
		ProjectID: n.ProjectID,
		Kind:      kind,
		Severity:  string(domain.NotifyInfo),
		Title:     title,
		Body:      clampBody(body),
		Link:      link,
		LinkLabel: label,
		DedupeKey: kind + ":" + n.DeploymentID,
		FocusID:   n.DeploymentID,
	}
	if err := s.store.InsertInboxItems(ctx, f); err != nil {
		return fmt.Errorf("inbox: inserting deploy items: %w", err)
	}
	if err := s.store.PruneInboxItems(ctx, recipients, domain.InboxRetention); err != nil {
		return fmt.Errorf("inbox: pruning: %w", err)
	}
	return nil
}

// awaitingBody is the line an approver reads: what is waiting, on which app,
// at which commit, and who asked. Composed here so a CLI prints the same
// sentence the drawer does (CLAUDE.md rule 4).
func awaitingBody(n DeployNotice) string {
	body := describeDeploy(n)
	if n.RequestedByEmailOrPush() == "" {
		return body
	}
	return body + " · " + n.RequestedByEmailOrPush()
}

// decisionBody names the verdict, the decider and — for a rejection — why.
func decisionBody(n DeployNotice, verdict string) string {
	body := verdict
	if n.ActorEmail != "" {
		body += " by " + n.ActorEmail
	}
	body += ". " + describeDeploy(n)
	if n.Reason != "" {
		body += "\nReason: " + n.Reason
	}
	return body
}

// describeDeploy renders "web · c99d2e1", degrading to whichever half is known.
func describeDeploy(n DeployNotice) string {
	switch {
	case n.ApplicationName != "" && n.Commit != "":
		return n.ApplicationName + " · " + shortCommit(n.Commit)
	case n.ApplicationName != "":
		return n.ApplicationName
	default:
		return shortID(n.DeploymentID)
	}
}

// RequestedByEmailOrPush names who asked for the deploy, or says a push did.
// A method on the notice so the awaiting item and the screens agree on the
// wording for the same absent requester.
func (n DeployNotice) RequestedByEmailOrPush() string {
	if n.RequesterEmail != "" {
		return "requested by " + n.RequesterEmail
	}
	return "pushed via webhook"
}

// deploymentLink is the in-panel path the item opens — the same shape
// deepLink builds for a deploy outcome, validated the same way (spec §5).
func deploymentLink(n DeployNotice) (path, label string) {
	if n.ProjectID == "" || n.ApplicationID == "" || n.DeploymentID == "" {
		return "", ""
	}
	path = "/projects/" + n.ProjectID + "/applications/" + n.ApplicationID +
		"/deployments?dep=" + n.DeploymentID
	if !validPath(path) {
		return "", ""
	}
	return path, "View deployment"
}

// shortID is the handle a screen shows instead of a full prefixed id: the
// prefix plus seven characters of the random part, e.g. "dep_9f2abcd". Ids
// carry ~130 bits, so seven base32 characters are ample to recognise one in a
// list — and the full id is one click away.
func shortID(id string) string {
	const keep = 7
	if i := strings.IndexByte(id, '_'); i >= 0 && len(id) > i+1+keep {
		return id[:i+1+keep]
	}
	return id
}

// shortCommit is the seven-character SHA prefix operators actually read. A ref
// that is not a SHA (a branch name, an image reference) is left alone.
func shortCommit(c string) string {
	if len(c) >= 40 && isHex(c) {
		return c[:7]
	}
	return c
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// panelUpdateBody composes the update item's body: what you run, what the
// delta means (canvas 16a's badge legend, in words), and where the notes are.
// Composed here so a CLI prints the same sentence the drawer does.
func panelUpdateBody(u PanelUpdate) string {
	meaning := map[string]string{
		"patch": "fixes only — update anytime",
		"minor": "new features — read the notes",
		"major": "breaking changes — plan a window",
	}[u.Kind]
	body := "You're on " + u.Current + "."
	if meaning != "" {
		body += " " + strings.ToUpper(u.Kind) + " release: " + meaning + "."
	}
	if u.NotesURL != "" {
		body += "\nRelease notes: " + u.NotesURL
	}
	return body
}

// ─── Team access (invitations-and-access-requests.md §6) ────────────────────

// AccessNotice is what the access feature tells the inbox about one request or
// one decision on it. Every field is already on screen for the people who
// receive it; the item is DENORMALISED like every other one — it states what
// happened rather than pointing at current state (notification-inbox.md §2).
type AccessNotice struct {
	TeamID   string
	TeamName string
	// RequestID is the dedupe token: one item per (user, request, kind), so a
	// redelivered decision is a no-op (ENGINEERING rule 12).
	RequestID string
	// RequesterID is the user a DECISION is addressed to; RequesterEmail names
	// them in the body an owner reads.
	RequesterID    string
	RequesterEmail string
	CurrentRole    string
	RequestedRole  string
	// Message is the requester's own sentence, carried verbatim.
	Message string
	// ActorEmail is who decided; Reason is a denial's explanation.
	ActorEmail string
	Reason     string
}

// RecordAccessRequested tells the people who can act — the team's OWNERS, the
// only rank that may decide a request (spec §3) — that someone has asked.
//
// Severity is info, not error: a person asking for access is the control
// working, not a fault. Immediate rather than digested, for the same reason a
// parked deploy is: a rollup of "2 people asked today" is unactionable, and the
// digest windows are defined only for the terminal outcome families
// (notification-inbox.md §3).
func (s *Service) RecordAccessRequested(ctx context.Context, n AccessNotice) error {
	if n.TeamID == "" || n.RequestID == "" {
		return nil
	}
	recipients, err := s.store.ListTeamInboxRecipients(ctx, n.TeamID,
		domain.InboxKindAccessRequested, domain.RoleOwner)
	if err != nil {
		return fmt.Errorf("inbox: resolving access recipients for %s: %w", n.TeamID, err)
	}
	return s.writeTeamItems(ctx, recipients, n.TeamID, domain.InboxKindAccessRequested,
		domain.InboxKindAccessRequested+":"+n.RequestID,
		"Access requested — "+describeTeam(n), accessRequestBody(n))
}

// RecordAccessGranted tells the requester they were let in, and by whom.
func (s *Service) RecordAccessGranted(ctx context.Context, n AccessNotice) error {
	return s.recordAccessDecision(ctx, n, domain.InboxKindAccessGranted,
		"Access granted — "+describeTeam(n), accessDecisionBody(n, "Granted "+n.RequestedRole))
}

// RecordAccessDenied tells the requester they were refused, and why if a reason
// was given. Still severity info: a governance decision is not a fault.
func (s *Service) RecordAccessDenied(ctx context.Context, n AccessNotice) error {
	return s.recordAccessDecision(ctx, n, domain.InboxKindAccessDenied,
		"Access denied — "+describeTeam(n), accessDecisionBody(n, "Denied"))
}

// recordAccessDecision writes one item to the requester, if they are still a
// member who has not muted the kind. A denial does not remove them from the
// team, so the membership join is the right filter for both verbs.
func (s *Service) recordAccessDecision(ctx context.Context, n AccessNotice, kind, title, body string) error {
	if n.TeamID == "" || n.RequestID == "" || n.RequesterID == "" {
		return nil
	}
	recipients, err := s.store.ListTeamInboxRecipientIfMember(ctx, n.TeamID, kind, n.RequesterID)
	if err != nil {
		return fmt.Errorf("inbox: resolving requester %s: %w", n.RequesterID, err)
	}
	return s.writeTeamItems(ctx, recipients, n.TeamID, kind, kind+":"+n.RequestID, title, body)
}

// InviteNotice is what an accepted invitation tells the inbox. The audience is
// the one person who sent it: everybody else on the team learns from the member
// list, and an invitation each admin issues is each admin's own business.
type InviteNotice struct {
	TeamID   string
	TeamName string
	InviteID string
	// InviterID is who sent the invitation; empty for one issued by an account
	// that has since been deleted, in which case there is nobody to tell.
	InviterID string
	// Email and Role describe who joined, and as what.
	Email string
	Role  string
}

// RecordInviteAccepted tells the inviter that their link was used.
func (s *Service) RecordInviteAccepted(ctx context.Context, n InviteNotice) error {
	if n.TeamID == "" || n.InviteID == "" || n.InviterID == "" {
		return nil
	}
	recipients, err := s.store.ListTeamInboxRecipientIfMember(ctx, n.TeamID,
		domain.InboxKindInviteAccepted, n.InviterID)
	if err != nil {
		return fmt.Errorf("inbox: resolving inviter %s: %w", n.InviterID, err)
	}
	team := n.TeamName
	if team == "" {
		team = shortID(n.TeamID)
	}
	return s.writeTeamItems(ctx, recipients, n.TeamID, domain.InboxKindInviteAccepted,
		domain.InboxKindInviteAccepted+":"+n.InviteID,
		"Invitation accepted — "+team,
		n.Email+" joined "+team+" as "+n.Role+".")
}

// writeTeamItems is the team-scoped fan-out: one immediate item per recipient,
// deduped on (user, kind, subject), then the same prune every other write runs.
// The link is the team settings screen — the one place both halves of this
// feature are acted on — validated exactly like every other stored link
// (notification-inbox.md §5).
func (s *Service) writeTeamItems(ctx context.Context, recipients []string, teamID, kind, dedupe, title, body string) error {
	if len(recipients) == 0 {
		return nil
	}
	link, label := teamSettingsLink()
	f := store.InboxFanout{
		IDs:       mintIDs(len(recipients)),
		UserIDs:   recipients,
		TeamID:    teamID,
		Kind:      kind,
		Severity:  string(domain.NotifyInfo),
		Title:     title,
		Body:      clampBody(body),
		Link:      link,
		LinkLabel: label,
		DedupeKey: dedupe,
	}
	if err := s.store.InsertTeamInboxItems(ctx, f); err != nil {
		return fmt.Errorf("inbox: inserting team items: %w", err)
	}
	if err := s.store.PruneInboxItems(ctx, recipients, domain.InboxRetention); err != nil {
		return fmt.Errorf("inbox: pruning: %w", err)
	}
	return nil
}

// teamSettingsLink is where both halves of team access are acted on. It carries
// no id: the teams screen lists every team the reader belongs to, and a path
// built from an id the reader may no longer be able to see would be a link into
// a 404.
func teamSettingsLink() (path, label string) {
	path = "/settings/teams"
	if !validPath(path) {
		return "", ""
	}
	return path, "Open team settings"
}

// describeTeam names the team a team-access item is about, falling back to a
// short id when the name was not carried.
func describeTeam(n AccessNotice) string {
	if n.TeamName != "" {
		return n.TeamName
	}
	return shortID(n.TeamID)
}

// accessRequestBody is the line an owner reads: who asked, for what, and their
// own words. Composed here so a CLI prints the same sentence the drawer does
// (CLAUDE.md rule 4).
func accessRequestBody(n AccessNotice) string {
	who := n.RequesterEmail
	if who == "" {
		who = shortID(n.RequesterID)
	}
	from := n.CurrentRole
	if from == "" {
		from = "no role"
	}
	body := who + " requests " + from + " → " + n.RequestedRole + " on " + describeTeam(n) + "."
	if n.Message != "" {
		body += "\n" + n.Message
	}
	return body
}

// accessDecisionBody names the verdict, the decider and — for a denial — why.
func accessDecisionBody(n AccessNotice, verdict string) string {
	body := verdict
	if n.ActorEmail != "" {
		body += " by " + n.ActorEmail
	}
	body += " on " + describeTeam(n) + "."
	if n.Reason != "" {
		body += "\nReason: " + n.Reason
	}
	return body
}

// recordImmediate writes one row per recipient and then raises the day's digest
// denominator for the matching success kind. Never creating that digest is
// deliberate (spec §3): a day with only failures shows the failures, not a
// "0/2 succeeded" row nobody asked for.
func (s *Service) recordImmediate(ctx context.Context, ev domain.NotifyEvent, recipients []string) error {
	link, label := deepLink(ev)
	f := store.InboxFanout{
		IDs:       mintIDs(len(recipients)),
		UserIDs:   recipients,
		ProjectID: ev.ProjectID,
		Kind:      ev.Type,
		Severity:  string(ev.Level),
		Title:     ev.Title,
		Body:      clampBody(ev.Body),
		Link:      link,
		LinkLabel: label,
		DedupeKey: ev.Type + ":" + ev.FocusID,
		FocusID:   ev.FocusID,
	}
	if err := s.store.InsertInboxItems(ctx, f); err != nil {
		return fmt.Errorf("inbox: inserting items: %w", err)
	}
	if key := s.digestKey(ev); key != "" && ev.FocusID != "" {
		if err := s.store.BumpInboxDigestTotals(ctx, key, ev.FocusID); err != nil {
			return err
		}
	}
	return nil
}

// recordDigest creates or increments each recipient's rollup for the window.
func (s *Service) recordDigest(ctx context.Context, ev domain.NotifyEvent, recipients []string) error {
	key := s.digestKey(ev)
	if key == "" {
		return nil
	}
	f := store.InboxFanout{
		IDs:       mintIDs(len(recipients)),
		UserIDs:   recipients,
		ProjectID: ev.ProjectID,
		Kind:      digestKind(ev.Type),
		Severity:  string(domain.NotifyInfo),
		// The stored title is the LABEL; the line the reader sees is composed
		// from the counters at read time by DigestTitle, so a counter that moves
		// after the row is written never rewrites stored copy (spec §3).
		Title:     DigestLabel(digestKind(ev.Type)),
		DedupeKey: key,
		FocusID:   ev.FocusID,
	}
	if err := s.store.UpsertInboxDigests(ctx, f); err != nil {
		return fmt.Errorf("inbox: upserting digests: %w", err)
	}
	return nil
}

// digestKey is the window a kind's events roll into: one per (kind family,
// project, UTC calendar day). The window is UTC because a bucket boundary is
// storage, not display — a per-user local window would put one event in
// different windows for different readers, and would move under a profile edit
// (spec §3).
func (s *Service) digestKey(ev domain.NotifyEvent) string {
	k := digestKind(ev.Type)
	if k == "" {
		return ""
	}
	day := s.now().UTC().Format("2006-01-02")
	return "digest:" + k + ":" + ev.ProjectID + ":" + day
}

// digestKind maps any event key onto the key whose daily digest counts it, so a
// backup.failed raises the same "Backups" denominator a backup.succeeded fills.
func digestKind(kind string) string {
	switch kind {
	case domain.EventDeploySucceeded, domain.EventDeployFailed:
		return domain.EventDeploySucceeded
	case domain.EventBackupSucceeded, domain.EventBackupFailed:
		return domain.EventBackupSucceeded
	}
	return ""
}

// DigestLabel is the noun a digest row is filed under — the stored title.
func DigestLabel(digested string) string {
	switch digested {
	case domain.EventDeploySucceeded:
		return "Deploys"
	case domain.EventBackupSucceeded:
		return "Backups"
	}
	return "Activity"
}

// DigestTitle composes a rollup's line from its counters: "Backups: 3/3
// succeeded". Composed rather than stored so the row can be incremented without
// rewriting copy, and composed HERE rather than in a client so a CLI prints the
// same words the drawer does (CLAUDE.md rule 4).
//
// The board's "Nightly backups: 3/3 succeeded, verified" is trimmed to what we
// can prove (spec §3): the plane does not know a schedule's cadence, and
// nothing verifies a backup.
func DigestTitle(digested string, ok, total int) string {
	return fmt.Sprintf("%s: %d/%d succeeded", DigestLabel(digested), ok, total)
}

// DisplayTitle is the title an API response carries: a digest's composed line,
// or an immediate item's stored title verbatim.
func DisplayTitle(it domain.InboxItem) string {
	if it.Digest {
		return DigestTitle(it.Kind, it.CountOK, it.CountTotal)
	}
	return it.Title
}

// deepLink renders the in-panel path an item opens, server-side (spec §3).
// Digests carry none — a rollup of three backups has no single thing to open.
// A link that does not pass validPath is dropped rather than stored: a
// free-text link rendered inside the authenticated shell is a stored open
// redirect (spec §5).
func deepLink(ev domain.NotifyEvent) (path, label string) {
	if ev.ProjectID == "" || ev.ResourceID == "" {
		return "", ""
	}
	switch ev.ResourceKind {
	case domain.WebhookResourceApplication:
		if ev.FocusID == "" {
			return "", ""
		}
		path = "/projects/" + ev.ProjectID + "/applications/" + ev.ResourceID + "/deployments?dep=" + ev.FocusID
		label = "View deployment"
	case domain.WebhookResourceDatabase:
		path = "/projects/" + ev.ProjectID + "/databases/" + ev.ResourceID + "/backups"
		label = "View backups"
	default:
		return "", ""
	}
	if !validPath(path) {
		return "", ""
	}
	return path, label
}

// validPath accepts only an in-panel absolute path: one leading slash, no
// scheme, no host, no protocol-relative "//" anywhere, and no control
// characters or whitespace that could break out of an href.
func validPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return false
	}
	if strings.Contains(p, "//") || strings.Contains(p, ":") {
		return false
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// clampBody truncates a body to the stored cap. The cut is on a rune boundary
// so a multi-byte character is never split into mojibake.
func clampBody(body string) string {
	if len(body) <= domain.InboxBodyMax {
		return body
	}
	cut := domain.InboxBodyMax
	for cut > 0 && !utf8Start(body[cut]) {
		cut--
	}
	return body[:cut]
}

// utf8Start reports whether b starts a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xc0 != 0x80 }

// mintIDs allocates one id per recipient. Ids are minted in the service layer,
// never in SQL.
func mintIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = ids.New(ids.PrefixInboxItem)
	}
	return out
}
