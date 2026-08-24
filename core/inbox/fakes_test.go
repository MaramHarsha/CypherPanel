package inbox

// An in-memory Store that behaves like the real one where the behaviour is the
// point: the (user_id, dedupe_key) uniqueness that makes redelivery a no-op,
// the sources guard on the digest upsert, the (created_at, id) DESC page order,
// and the per-user prune. Anything the tests do not assert on is left simple on
// purpose — a fake that reimplements Postgres proves nothing about Postgres,
// which is what TestStoreInboxRoundtrip is for (ENGINEERING rule 29).

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	// members maps a project id to the users of the team that owns it.
	members map[string][]string
	// items maps a user id to their rows, in insertion order.
	items map[string][]*domain.InboxItem
	prefs map[string][]string

	// clock advances one second per write so page order is deterministic.
	clock time.Time

	// failRecipients makes the first query fail, for the error path.
	failRecipients bool
	prunes         int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		members: map[string][]string{},
		items:   map[string][]*domain.InboxItem{},
		prefs:   map[string][]string{},
		clock:   time.Date(2026, 8, 21, 4, 2, 0, 0, time.UTC),
	}
}

func (f *fakeStore) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fakeStore) find(userID, dedupeKey string) *domain.InboxItem {
	for _, it := range f.items[userID] {
		if it.DedupeKey == dedupeKey {
			return it
		}
	}
	return nil
}

func (f *fakeStore) ListInboxRecipients(_ context.Context, projectID, kind string) ([]string, error) {
	if f.failRecipients {
		return nil, fmt.Errorf("boom")
	}
	out := []string{}
	for _, u := range f.members[projectID] {
		muted := false
		for _, m := range f.prefs[u] {
			if m == kind {
				muted = true
			}
		}
		if !muted {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeStore) InsertInboxItems(_ context.Context, fo store.InboxFanout) error {
	for i, uid := range fo.UserIDs {
		if f.find(uid, fo.DedupeKey) != nil {
			continue // ON CONFLICT (user_id, dedupe_key) DO NOTHING
		}
		at := f.tick()
		f.items[uid] = append(f.items[uid], &domain.InboxItem{
			ID: fo.IDs[i], UserID: uid, ProjectID: fo.ProjectID, Kind: fo.Kind,
			Severity: domain.NotifyLevel(fo.Severity), Title: fo.Title, Body: fo.Body,
			Link: fo.Link, LinkLabel: fo.LinkLabel, CountOK: 1, CountTotal: 1,
			Sources: []string{}, DedupeKey: fo.DedupeKey, CreatedAt: at, UpdatedAt: at,
		})
	}
	return nil
}

func (f *fakeStore) UpsertInboxDigests(_ context.Context, fo store.InboxFanout) error {
	for i, uid := range fo.UserIDs {
		if cur := f.find(uid, fo.DedupeKey); cur != nil {
			if contains(cur.Sources, fo.FocusID) {
				continue // already rolled in — redelivery is a no-op
			}
			cur.CountOK++
			cur.CountTotal++
			cur.Sources = append(cur.Sources, fo.FocusID)
			cur.UpdatedAt = f.tick()
			continue
		}
		at := f.tick()
		f.items[uid] = append(f.items[uid], &domain.InboxItem{
			ID: fo.IDs[i], UserID: uid, ProjectID: fo.ProjectID, Kind: fo.Kind,
			Severity: domain.NotifyLevel(fo.Severity), Digest: true, Title: fo.Title,
			CountOK: 1, CountTotal: 1, Sources: []string{fo.FocusID},
			DedupeKey: fo.DedupeKey, CreatedAt: at, UpdatedAt: at,
		})
	}
	return nil
}

func (f *fakeStore) BumpInboxDigestTotals(_ context.Context, dedupeKey, focusID string) error {
	for _, rows := range f.items {
		for _, it := range rows {
			if it.Digest && it.DedupeKey == dedupeKey && !contains(it.Sources, focusID) {
				it.CountTotal++
				it.Sources = append(it.Sources, focusID)
				it.UpdatedAt = f.tick()
			}
		}
	}
	return nil
}

func (f *fakeStore) PruneInboxItems(_ context.Context, userIDs []string, keep int64) error {
	f.prunes++
	for _, uid := range userIDs {
		rows := f.sorted(uid, false)
		if int64(len(rows)) <= keep {
			continue
		}
		survivors := rows[:keep]
		kept := make([]*domain.InboxItem, 0, len(survivors))
		for _, s := range survivors {
			for _, it := range f.items[uid] {
				if it.ID == s.ID {
					kept = append(kept, it)
				}
			}
		}
		f.items[uid] = kept
	}
	return nil
}

// sorted returns a user's rows newest-first, the exact order the index gives.
func (f *fakeStore) sorted(userID string, unreadOnly bool) []domain.InboxItem {
	out := []domain.InboxItem{}
	for _, it := range f.items[userID] {
		if unreadOnly && it.ReadAt != nil {
			continue
		}
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func (f *fakeStore) ListInboxItems(_ context.Context, userID string, unreadOnly bool, limit int32) ([]domain.InboxItem, error) {
	rows := f.sorted(userID, unreadOnly)
	if int32(len(rows)) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f *fakeStore) ListInboxItemsBefore(_ context.Context, userID string, unreadOnly bool, before string, limit int32) ([]domain.InboxItem, error) {
	all := f.sorted(userID, false)
	var cursor *domain.InboxItem
	for i := range all {
		if all[i].ID == before {
			cursor = &all[i]
		}
	}
	if cursor == nil {
		return []domain.InboxItem{}, nil
	}
	out := []domain.InboxItem{}
	for _, it := range f.sorted(userID, unreadOnly) {
		if it.CreatedAt.Before(cursor.CreatedAt) || (it.CreatedAt.Equal(cursor.CreatedAt) && it.ID < cursor.ID) {
			out = append(out, it)
		}
	}
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) CountUnreadInboxItems(_ context.Context, userID string) (int64, error) {
	var n int64
	for _, it := range f.items[userID] {
		if it.ReadAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) MarkInboxItemRead(_ context.Context, userID, itemID string) (domain.InboxItem, error) {
	for _, it := range f.items[userID] {
		if it.ID == itemID {
			if it.ReadAt == nil {
				at := f.tick()
				it.ReadAt = &at
			}
			return *it, nil
		}
	}
	return domain.InboxItem{}, fmt.Errorf("fake: %w", store.ErrNotFound)
}

func (f *fakeStore) MarkAllInboxItemsRead(_ context.Context, userID string) (int64, error) {
	var n int64
	for _, it := range f.items[userID] {
		if it.ReadAt == nil {
			at := f.tick()
			it.ReadAt = &at
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) GetInboxPreferences(_ context.Context, userID string) (domain.InboxPreferences, error) {
	return domain.InboxPreferences{UserID: userID, MutedKinds: f.prefs[userID]}, nil
}

func (f *fakeStore) SetInboxPreferences(_ context.Context, userID string, muted []string) (domain.InboxPreferences, error) {
	f.prefs[userID] = muted
	return domain.InboxPreferences{UserID: userID, MutedKinds: muted}, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
