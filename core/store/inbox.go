package store

// Notification inbox persistence (notification-inbox.md §2, §4). Domain types
// in, domain types out; pgx/pgtype stays inside this package.
//
// Every read here takes the caller's user id as an argument rather than
// filtering later: tenancy in this feature is a column, and the only way to
// keep that guarantee syntactic is to make it impossible to ask a question
// that is not already scoped (spec §5).

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// ─── Fan-out (spec §4) ──────────────────────────────────────────────────────

// ListInboxRecipients returns the members of the team owning projectID who have
// not muted kind. The empty result is the common case on a single-user panel
// with no team rows for the project, and means "nobody" — never "everybody".
func (s *Store) ListInboxRecipients(ctx context.Context, projectID, kind string) ([]string, error) {
	out, err := s.q.ListInboxRecipients(ctx, db.ListInboxRecipientsParams{
		ProjectID: projectID,
		Kind:      kind,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing inbox recipients: %w", err)
	}
	return out, nil
}

// ListPanelInboxRecipients returns the users a panel-level kind reaches: every
// panel owner and every team owner who has not muted kind
// (control-plane-hardening.md §3). Distinct — one person, one item.
func (s *Store) ListPanelInboxRecipients(ctx context.Context, kind string) ([]string, error) {
	out, err := s.q.ListPanelInboxRecipients(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("store: listing panel inbox recipients: %w", err)
	}
	return out, nil
}

// ListApprovalInboxRecipients is ListInboxRecipients narrowed by RANK: the
// members of the project's team who could actually act on a parked deploy
// (deploy-protection.md §9). Addressing everyone would put an item in front of
// people who cannot act on it; the "never hold an item for a team you do not
// belong to" rule still holds, because membership is still the join.
func (s *Store) ListApprovalInboxRecipients(ctx context.Context, projectID, kind, minRole string) ([]string, error) {
	out, err := s.q.ListApprovalInboxRecipients(ctx, db.ListApprovalInboxRecipientsParams{
		ProjectID: projectID,
		Kind:      kind,
		MinRole:   minRole,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing approval inbox recipients: %w", err)
	}
	return out, nil
}

// ListInboxRecipientIfMember resolves ONE named user to a recipient list of
// zero or one — a decision on a parked deploy is news for the person who asked
// for it and nobody else (deploy-protection.md §9). It is a query rather than a
// bare id so a requester who has since left the team, or muted the kind, is
// filtered by the same rule every other fan-out obeys.
func (s *Store) ListInboxRecipientIfMember(ctx context.Context, projectID, kind, userID string) ([]string, error) {
	out, err := s.q.ListInboxRecipientIfMember(ctx, db.ListInboxRecipientIfMemberParams{
		ProjectID: projectID,
		Kind:      kind,
		UserID:    userID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing inbox recipient: %w", err)
	}
	return out, nil
}

// InsertPanelInboxItems writes one immediate, project-less item per recipient.
// The (user_id, dedupe_key) conflict is dropped, which is what makes "once per
// version" hold across restarts. ProjectID, Link and LinkLabel on f are
// ignored: a panel-level item has none.
func (s *Store) InsertPanelInboxItems(ctx context.Context, f InboxFanout) error {
	if len(f.IDs) == 0 {
		return nil
	}
	if err := s.q.InsertPanelInboxItems(ctx, db.InsertPanelInboxItemsParams{
		Ids:       f.IDs,
		UserIds:   f.UserIDs,
		Kind:      f.Kind,
		Severity:  f.Severity,
		Title:     f.Title,
		Body:      f.Body,
		DedupeKey: f.DedupeKey,
	}); err != nil {
		return wrapCreate("inserting panel inbox items", err)
	}
	return nil
}

// InboxFanout is the shared half of one fan-out write: the columns every
// recipient's row holds identically. IDs and UserIDs are positional pairs —
// IDs[i] is the id minted for UserIDs[i].
type InboxFanout struct {
	IDs     []string
	UserIDs []string
	// ProjectID and TeamID are the item's scope, and exactly one of them is set
	// (a panel-level item sets neither).
	ProjectID string
	TeamID    string
	Kind      string
	Severity  string
	Title     string
	Body      string
	Link      string
	LinkLabel string
	DedupeKey string
	// FocusID identifies the single observation behind this write. It is the
	// digest's de-duplication token; immediate items carry it in the dedupe key
	// instead.
	FocusID string
}

// InsertInboxItems writes one immediate item per recipient in a single
// statement. A repeat of the same observation conflicts on
// (user_id, dedupe_key) and is dropped, which is what makes redelivery a no-op
// (ENGINEERING rule 12).
func (s *Store) InsertInboxItems(ctx context.Context, f InboxFanout) error {
	if len(f.IDs) == 0 {
		return nil
	}
	if err := s.q.InsertInboxItems(ctx, db.InsertInboxItemsParams{
		Ids:       f.IDs,
		UserIds:   f.UserIDs,
		ProjectID: f.ProjectID,
		Kind:      f.Kind,
		Severity:  f.Severity,
		Title:     f.Title,
		Body:      f.Body,
		Link:      f.Link,
		LinkLabel: f.LinkLabel,
		DedupeKey: f.DedupeKey,
	}); err != nil {
		return wrapCreate("inserting inbox items", err)
	}
	return nil
}

// UpsertInboxDigests creates each recipient's digest for the window or, if it
// already exists, increments both counters and records the source. A source
// already rolled in leaves the row untouched.
func (s *Store) UpsertInboxDigests(ctx context.Context, f InboxFanout) error {
	if len(f.IDs) == 0 {
		return nil
	}
	if err := s.q.UpsertInboxDigests(ctx, db.UpsertInboxDigestsParams{
		Ids:       f.IDs,
		UserIds:   f.UserIDs,
		ProjectID: f.ProjectID,
		Kind:      f.Kind,
		Severity:  f.Severity,
		Title:     f.Title,
		FocusID:   f.FocusID,
		DedupeKey: f.DedupeKey,
	}); err != nil {
		return wrapCreate("upserting inbox digests", err)
	}
	return nil
}

// BumpInboxDigestTotals raises the denominator on every existing digest for one
// window without ever creating one — the failure's contribution to
// "2/3 succeeded" (spec §3). The dedupe key names the kind, the project and the
// day, so it needs no recipient list to stay scoped.
func (s *Store) BumpInboxDigestTotals(ctx context.Context, dedupeKey, focusID string) error {
	if err := s.q.BumpInboxDigestTotals(ctx, db.BumpInboxDigestTotalsParams{
		DedupeKey: dedupeKey,
		FocusID:   focusID,
	}); err != nil {
		return fmt.Errorf("store: bumping inbox digest totals: %w", err)
	}
	return nil
}

// PruneInboxItems trims each named user's inbox to the most recent keep rows,
// in one statement.
func (s *Store) PruneInboxItems(ctx context.Context, userIDs []string, keep int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	if err := s.q.PruneInboxItems(ctx, db.PruneInboxItemsParams{
		UserIds:  userIDs,
		KeepRows: keep,
	}); err != nil {
		return wrapDelete("pruning inbox items", err)
	}
	return nil
}

// ─── Reads (spec §6) ────────────────────────────────────────────────────────

// ListInboxItems returns the newest page of a user's inbox, optionally filtered
// to unread.
func (s *Store) ListInboxItems(ctx context.Context, userID string, unreadOnly bool, limit int32) ([]domain.InboxItem, error) {
	rows, err := s.q.ListInboxItems(ctx, db.ListInboxItemsParams{
		UserID:     userID,
		UnreadOnly: unreadOnly,
		RowLimit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing inbox items: %w", err)
	}
	return inboxItemsFromRows(rows), nil
}

// ListInboxItemsBefore returns the page strictly older than the cursor item on
// (created_at, id) DESC. A cursor that is not the caller's own item — or has
// been pruned — yields an empty page rather than restarting at the newest row.
func (s *Store) ListInboxItemsBefore(ctx context.Context, userID string, unreadOnly bool, before string, limit int32) ([]domain.InboxItem, error) {
	rows, err := s.q.ListInboxItemsBefore(ctx, db.ListInboxItemsBeforeParams{
		UserID:     userID,
		UnreadOnly: unreadOnly,
		Before:     before,
		RowLimit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing inbox items before cursor: %w", err)
	}
	return inboxItemsFromRows(rows), nil
}

// CountUnreadInboxItems is the bell's number.
func (s *Store) CountUnreadInboxItems(ctx context.Context, userID string) (int64, error) {
	n, err := s.q.CountUnreadInboxItems(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("store: counting unread inbox items: %w", err)
	}
	return n, nil
}

// MarkInboxItemRead stamps read_at once and is idempotent thereafter. An item
// that is not the caller's own comes back as ErrNotFound — the same answer as
// one that never existed.
func (s *Store) MarkInboxItemRead(ctx context.Context, userID, itemID string) (domain.InboxItem, error) {
	row, err := s.q.MarkInboxItemRead(ctx, db.MarkInboxItemReadParams{
		ID:     itemID,
		UserID: userID,
	})
	if err != nil {
		return domain.InboxItem{}, wrap("marking inbox item read", err)
	}
	return inboxItemFromRow(row), nil
}

// MarkAllInboxItemsRead clears the caller's unread count and reports how many
// items it actually changed.
func (s *Store) MarkAllInboxItemsRead(ctx context.Context, userID string) (int64, error) {
	n, err := s.q.MarkAllInboxItemsRead(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("store: marking all inbox items read: %w", err)
	}
	return n, nil
}

// ListTeamInboxRecipients resolves a TEAM-scoped kind to the members at or
// above minRole who have not muted it (invitations-and-access-requests.md §6).
// The inbox's third scope after a project's and the panel's; membership is
// still the join, so "never hold an item for a team you do not belong to"
// holds by construction.
func (s *Store) ListTeamInboxRecipients(ctx context.Context, teamID, kind, minRole string) ([]string, error) {
	out, err := s.q.ListTeamInboxRecipients(ctx, db.ListTeamInboxRecipientsParams{
		TeamID:  teamID,
		Kind:    kind,
		MinRole: minRole,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing team inbox recipients: %w", err)
	}
	return out, nil
}

// ListTeamInboxRecipientIfMember resolves ONE named user to a recipient list of
// zero or one — the requester of an access request, the sender of an accepted
// invitation — filtered by the same membership and mute rules as every other
// fan-out.
func (s *Store) ListTeamInboxRecipientIfMember(ctx context.Context, teamID, kind, userID string) ([]string, error) {
	out, err := s.q.ListTeamInboxRecipientIfMember(ctx, db.ListTeamInboxRecipientIfMemberParams{
		TeamID: teamID,
		Kind:   kind,
		UserID: userID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing team inbox recipient: %w", err)
	}
	return out, nil
}

// InsertTeamInboxItems writes one immediate, team-scoped item per recipient.
// ProjectID on f is ignored: a team-scoped item has none.
func (s *Store) InsertTeamInboxItems(ctx context.Context, f InboxFanout) error {
	if len(f.IDs) == 0 {
		return nil
	}
	if err := s.q.InsertTeamInboxItems(ctx, db.InsertTeamInboxItemsParams{
		Ids:       f.IDs,
		UserIds:   f.UserIDs,
		TeamID:    f.TeamID,
		Kind:      f.Kind,
		Severity:  f.Severity,
		Title:     f.Title,
		Body:      f.Body,
		Link:      f.Link,
		LinkLabel: f.LinkLabel,
		DedupeKey: f.DedupeKey,
	}); err != nil {
		return wrapCreate("inserting team inbox items", err)
	}
	return nil
}

// DeleteInboxItemsForTeamMember removes every item a user holds for one team —
// its projects' items AND the team-scoped ones — which is what leaving a team
// costs (spec §4, invitations-and-access-requests.md §6).
func (s *Store) DeleteInboxItemsForTeamMember(ctx context.Context, teamID, userID string) error {
	if err := s.q.DeleteInboxItemsForTeamMember(ctx, db.DeleteInboxItemsForTeamMemberParams{
		UserID: userID,
		// pgtype.Text because it is compared against the nullable team_id
		// column too; a team id is never empty, so it is always Valid here.
		TeamID: pgtype.Text{String: teamID, Valid: true},
	}); err != nil {
		return wrapDelete("deleting inbox items for team member", err)
	}
	return nil
}

// ─── Preferences (spec §2) ──────────────────────────────────────────────────

// GetInboxPreferences returns a user's mutes. An absent row is not an error: it
// means "everything on", so it comes back as an empty set.
func (s *Store) GetInboxPreferences(ctx context.Context, userID string) (domain.InboxPreferences, error) {
	row, err := s.q.GetInboxPreferences(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.InboxPreferences{UserID: userID, MutedKinds: []string{}}, nil
		}
		return domain.InboxPreferences{}, fmt.Errorf("store: getting inbox preferences: %w", err)
	}
	return inboxPreferencesFromRow(row), nil
}

// SetInboxPreferences replaces a user's muted set wholesale.
func (s *Store) SetInboxPreferences(ctx context.Context, userID string, muted []string) (domain.InboxPreferences, error) {
	if muted == nil {
		muted = []string{}
	}
	row, err := s.q.UpsertInboxPreferences(ctx, db.UpsertInboxPreferencesParams{
		UserID:     userID,
		MutedKinds: muted,
	})
	if err != nil {
		return domain.InboxPreferences{}, wrapCreate("setting inbox preferences", err)
	}
	return inboxPreferencesFromRow(row), nil
}

// ─── row mappers ────────────────────────────────────────────────────────────

func inboxItemsFromRows(rows []db.InboxItem) []domain.InboxItem {
	out := make([]domain.InboxItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, inboxItemFromRow(r))
	}
	return out
}

func inboxItemFromRow(r db.InboxItem) domain.InboxItem {
	return domain.InboxItem{
		ID:         r.ID,
		UserID:     r.UserID,
		ProjectID:  r.ProjectID.String, // NULL for a panel-level kind (0028)
		TeamID:     r.TeamID.String,    // set only for the team-access kinds (0033)
		Kind:       r.Kind,
		Severity:   domain.NotifyLevel(r.Severity),
		Digest:     r.Digest,
		Title:      r.Title,
		Body:       r.Body,
		Link:       r.Link,
		LinkLabel:  r.LinkLabel,
		CountOK:    int(r.CountOk),
		CountTotal: int(r.CountTotal),
		Sources:    r.Sources,
		DedupeKey:  r.DedupeKey,
		ReadAt:     ptrTime(r.ReadAt),
		CreatedAt:  r.CreatedAt.Time,
		UpdatedAt:  r.UpdatedAt.Time,
	}
}

func inboxPreferencesFromRow(r db.InboxPreference) domain.InboxPreferences {
	return domain.InboxPreferences{
		UserID:     r.UserID,
		MutedKinds: r.MutedKinds,
		UpdatedAt:  r.UpdatedAt.Time,
	}
}
