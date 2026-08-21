package inbox

// The read and mutate surface behind /api/v1/inbox (notification-inbox.md §6).
// Every method takes the caller's user id as its first argument and no method
// accepts anyone else's: tenancy here is structural rather than resolved, which
// is why this feature adds no projectIDFor… resolver to authz.go (spec §5).

import (
	"context"
	"fmt"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Page is one keyset page of a user's inbox. NextBefore is the cursor for the
// following page, empty when this is the last one.
type Page struct {
	Items      []domain.InboxItem
	NextBefore string
}

// ListOptions are the feed's query parameters.
type ListOptions struct {
	// UnreadOnly is the ?unread=true filter. Filtered-to-zero is a distinct
	// state from empty in the UI (ui-principles §7), so the service reports it
	// by returning an empty page rather than by inventing a flag.
	UnreadOnly bool
	// Limit defaults to 20 and caps at 100.
	Limit int
	// Before continues from that item's (created_at, id) descending.
	Before string
}

// List returns a page of the caller's own items, newest first.
func (s *Service) List(ctx context.Context, userID string, opts ListOptions) (Page, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	// One extra row tells us whether a further page exists without a second
	// query or a count.
	fetch := int32(limit) + 1

	var (
		rows []domain.InboxItem
		err  error
	)
	if opts.Before == "" {
		rows, err = s.store.ListInboxItems(ctx, userID, opts.UnreadOnly, fetch)
	} else {
		rows, err = s.store.ListInboxItemsBefore(ctx, userID, opts.UnreadOnly, opts.Before, fetch)
	}
	if err != nil {
		return Page{}, fmt.Errorf("inbox: listing items: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	return Page{Items: rows, NextBefore: next}, nil
}

// UnreadCount is the bell's number.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int64, error) {
	n, err := s.store.CountUnreadInboxItems(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("inbox: counting unread: %w", err)
	}
	return n, nil
}

// MarkRead stamps one item read. It is idempotent: marking an already-read item
// changes nothing and reports no error. An item that is not the caller's own
// comes back wrapping store.ErrNotFound — the same answer as one that never
// existed, which is what keeps membership unprobeable.
func (s *Service) MarkRead(ctx context.Context, userID, itemID string) error {
	if _, err := s.store.MarkInboxItemRead(ctx, userID, itemID); err != nil {
		return err
	}
	return nil
}

// MarkAllRead clears the caller's unread count and reports how many items it
// changed. The items stay listed — reading is not deleting.
func (s *Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	n, err := s.store.MarkAllInboxItemsRead(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("inbox: marking all read: %w", err)
	}
	return n, nil
}

// AvailableKinds is the taxonomy a preference list may name — served rather
// than hardcoded in the front end, so a new event key needs no client change
// (spec §6).
func AvailableKinds() []string { return domain.EventTypes() }

// Preferences returns the caller's mutes. An account that has never set any
// gets an empty set, which means "everything on" (spec §2).
func (s *Service) Preferences(ctx context.Context, userID string) (domain.InboxPreferences, error) {
	p, err := s.store.GetInboxPreferences(ctx, userID)
	if err != nil {
		return domain.InboxPreferences{}, fmt.Errorf("inbox: getting preferences: %w", err)
	}
	if p.MutedKinds == nil {
		p.MutedKinds = []string{}
	}
	return p, nil
}

// SetPreferences replaces the caller's muted set wholesale. A kind outside the
// taxonomy is a 400 rather than a silently-dropped setting: a typo that mutes
// nothing would be indistinguishable from a preference that works.
func (s *Service) SetPreferences(ctx context.Context, userID string, muted []string) (domain.InboxPreferences, error) {
	seen := map[string]bool{}
	clean := make([]string, 0, len(muted))
	for _, k := range muted {
		if !domain.ValidEventType(k) {
			return domain.InboxPreferences{}, invalid("unknown notification kind: " + k)
		}
		if !seen[k] {
			seen[k] = true
			clean = append(clean, k)
		}
	}
	p, err := s.store.SetInboxPreferences(ctx, userID, clean)
	if err != nil {
		return domain.InboxPreferences{}, fmt.Errorf("inbox: setting preferences: %w", err)
	}
	if p.MutedKinds == nil {
		p.MutedKinds = []string{}
	}
	return p, nil
}
