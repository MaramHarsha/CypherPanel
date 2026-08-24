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
	InsertInboxItems(ctx context.Context, f store.InboxFanout) error
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
