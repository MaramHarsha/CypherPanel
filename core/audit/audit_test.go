package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore keeps events in insertion order and answers the reads the service
// needs. It does NOT re-implement the visibility SQL: the read tests assert what
// the service asks the store for (the filter), which is exactly the boundary
// this package owns. The predicate itself is proven against real Postgres in
// core/store (TestStoreAuditVisibility).
type fakeStore struct {
	events []domain.AuditEvent
	teams  map[string][]domain.TeamWithRole
	// lastFilter records the filter the last List call was given.
	lastFilter store.AuditFilter
	// purged records each (cutoff, limit) pair PurgeAuditEvents was called
	// with, so a batched drain is observable.
	purged   []int32
	purgeErr error
	getErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{teams: map[string][]domain.TeamWithRole{}}
}

func (f *fakeStore) InsertAuditEvent(_ context.Context, e domain.AuditEvent) (domain.AuditEvent, error) {
	e.At = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC).Add(time.Duration(len(f.events)) * time.Second)
	f.events = append(f.events, e)
	return e, nil
}

func (f *fakeStore) GetAuditEvent(_ context.Context, id string) (domain.AuditEvent, error) {
	if f.getErr != nil {
		return domain.AuditEvent{}, f.getErr
	}
	for _, e := range f.events {
		if e.ID == id {
			return e, nil
		}
	}
	return domain.AuditEvent{}, store.ErrNotFound
}

func (f *fakeStore) ListAuditEvents(_ context.Context, filter store.AuditFilter) ([]domain.AuditEvent, error) {
	f.lastFilter = filter
	out := []domain.AuditEvent{}
	for i := len(f.events) - 1; i >= 0; i-- { // newest first
		if int32(len(out)) == filter.Limit {
			break
		}
		out = append(out, f.events[i])
	}
	return out, nil
}

func (f *fakeStore) PurgeAuditEvents(_ context.Context, _ time.Time, limit int32) (int64, error) {
	if f.purgeErr != nil {
		return 0, f.purgeErr
	}
	f.purged = append(f.purged, limit)
	n := int64(len(f.events))
	if n > int64(limit) {
		n = int64(limit)
	}
	f.events = f.events[n:]
	return n, nil
}

func (f *fakeStore) ListTeamsByUser(_ context.Context, userID string) ([]domain.TeamWithRole, error) {
	return f.teams[userID], nil
}

func newService(t *testing.T, st Store, retention time.Duration) *Service {
	t.Helper()
	return NewService(st, retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func member(id string) domain.User { return domain.User{ID: id, Role: domain.RoleMember} }

// ─── the vocabulary ─────────────────────────────────────────────────────────

// A verb outside the closed set is refused rather than stored: a row nobody can
// filter for is worse than no row, and every call site uses a constant, so this
// can only ever fire on a programmer error.
func TestRecordRefusesAnUnknownAction(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	_, err := s.Record(context.Background(), Entry{
		Action:   "deploy.teleported",
		Resource: Resource(ResourceApplication, "app_x", "web"),
	})
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("Record(unknown action) = %v, want ErrUnknownAction", err)
	}
	if len(st.events) != 0 {
		t.Fatalf("an unknown action was stored anyway: %+v", st.events)
	}
}

func TestRecordRefusesAResourcelessEntryAndAnUnknownOutcome(t *testing.T) {
	s := newService(t, newFakeStore(), 0)
	if _, err := s.Record(context.Background(), Entry{Action: ActionLogin}); err == nil {
		t.Error("an entry with no resource kind was accepted")
	}
	if _, err := s.Record(context.Background(), Entry{
		Action:   ActionLogin,
		Outcome:  "maybe",
		Resource: Resource(ResourceUser, "usr_x", "a@b.c"),
	}); err == nil {
		t.Error("an unknown outcome was accepted")
	}
}

// Defaults are the common case: success, and an actor kind for the wiring that
// has no principal at all.
func TestRecordDefaultsOutcomeAndActorKind(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ev, err := s.Record(context.Background(), Entry{
		Action:   ActionServerCreated,
		Resource: Resource(ResourceServer, "srv_1", "box"),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if ev.Outcome != domain.AuditSuccess {
		t.Errorf("outcome = %q, want success", ev.Outcome)
	}
	if ev.Actor.Kind != domain.AuditActorSystem {
		t.Errorf("actor kind = %q, want system", ev.Actor.Kind)
	}
	if !strings.HasPrefix(ev.ID, "aud_") {
		t.Errorf("id = %q, want an aud_ prefix", ev.ID)
	}
}

// The whole point of §6: a detail key that names a secret is dropped, while
// `key` — the NAME of an application env var — is kept, because that is exactly
// what an env-var row is for.
func TestRecordStripsSecretDetailKeysButKeepsEnvVarNames(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ev, err := s.Record(context.Background(), Entry{
		Action:   ActionEnvVarSet,
		Resource: Resource(ResourceApplication, "app_x", "web"),
		Detail: map[string]any{
			"key":      "DATABASE_URL",
			"value":    "postgres://user:hunter2@db/app",
			"password": "hunter2",
			"token":    "tok_secret",
		},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if ev.Detail["key"] != "DATABASE_URL" {
		t.Errorf("detail lost the env var name: %+v", ev.Detail)
	}
	for _, banned := range []string{"value", "password", "token"} {
		if _, ok := ev.Detail[banned]; ok {
			t.Errorf("detail kept a secret-shaped key %q: %+v", banned, ev.Detail)
		}
	}
}

// An oversized detail is dropped rather than stored: an audit row records an
// action, it is not a place to put data.
func TestRecordDropsAnOversizedDetail(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ev, err := s.Record(context.Background(), Entry{
		Action:   ActionApplicationUpdated,
		Resource: Resource(ResourceApplication, "app_x", "web"),
		Detail:   oversizedDetail(),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if ev.Detail["detail_dropped"] != "too large" {
		t.Fatalf("detail = %+v, want it dropped", ev.Detail)
	}
}

// oversizedDetail is bigger than the cap even AFTER each value is truncated to
// maxDetailValue — which is the only way to reach the drop branch, and worth
// stating: a single runaway string is clipped, not dropped.
func oversizedDetail() map[string]any {
	d := map[string]any{}
	for i := range 2 + maxDetailBytes/maxDetailValue {
		d[string(rune('a'+i))] = strings.Repeat("x", maxDetailValue*2)
	}
	return d
}

// Snapshot strings are bounded, and a truncated one says so — a clipped name
// must not read as the whole name.
func TestRecordTruncatesLongSnapshots(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ev, err := s.Record(context.Background(), Entry{
		Action:   ActionLogin,
		Actor:    domain.AuditActor{Kind: domain.AuditActorAnonymous, Label: strings.Repeat("a", maxLabelLen*2)},
		Resource: Resource(ResourceUser, "", strings.Repeat("b", maxResourceName*2)),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len([]rune(ev.Actor.Label)) > maxLabelLen || !strings.HasSuffix(ev.Actor.Label, "…") {
		t.Errorf("label not truncated with a marker: %d chars", len(ev.Actor.Label))
	}
	if !strings.HasSuffix(ev.Resource.Name, "…") {
		t.Error("resource name not truncated with a marker")
	}
}

// A snapshot longer than its cap that is not ASCII is the interesting case: a
// byte-boundary cut produces invalid UTF-8, which Postgres refuses, so the
// INSERT fails and the row — the record that someone tried and was refused —
// is silently missing. Every result here must be valid UTF-8 and within its
// byte cap, which is what the column and the driver enforce.
func TestRecordTruncatesOnRuneBoundaries(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ev, err := s.Record(context.Background(), Entry{
		Action:   ActionLogin,
		Actor:    domain.AuditActor{Kind: domain.AuditActorAnonymous, Label: strings.Repeat("日", maxLabelLen)},
		Resource: Resource(ResourceUser, "", strings.Repeat("é", maxResourceName)),
		Detail:   map[string]any{"note": strings.Repeat("🔒", maxDetailValue)},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	for _, tc := range []struct {
		what string
		got  string
		max  int
	}{
		{"actor label", ev.Actor.Label, maxLabelLen},
		{"resource name", ev.Resource.Name, maxResourceName},
		{"detail value", ev.Detail["note"].(string), maxDetailValue},
	} {
		if !utf8.ValidString(tc.got) {
			t.Errorf("%s is not valid UTF-8 — Postgres would refuse the row", tc.what)
		}
		if len(tc.got) > tc.max {
			t.Errorf("%s is %d bytes, over its %d-byte cap", tc.what, len(tc.got), tc.max)
		}
		if !strings.HasSuffix(tc.got, "…") {
			t.Errorf("%s was cut without a marker: %q", tc.what, tc.got)
		}
	}
}

func TestTruncateBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short string is untouched", "hello", 10, "hello"},
		{"exactly at the cap is untouched", "hello", 5, "hello"},
		{"ascii is cut with a marker inside the cap", "abcdefghij", 6, "abc…"},
		{"multibyte cuts on a rune boundary", strings.Repeat("日", 4), 9, "日日…"},
		{"a cap too small for the marker clips", "日本語", 2, ""},
		{"invalid utf-8 is coerced, not passed on", "ab\xff", 10, "ab\uFFFD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncate(%q, %d) is not valid UTF-8", tc.in, tc.max)
			}
			if len(got) > tc.max {
				t.Errorf("truncate(%q, %d) is %d bytes, over the cap", tc.in, tc.max, len(got))
			}
		})
	}
}

func TestActionsIsSortedAndSelfConsistent(t *testing.T) {
	list := Actions()
	if len(list) < 40 {
		t.Fatalf("the vocabulary has only %d verbs — the table is not wired", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1] >= list[i] {
			t.Fatalf("Actions() is not sorted at %d: %q then %q", i, list[i-1], list[i])
		}
	}
	for _, a := range list {
		if !ValidAction(a) {
			t.Errorf("Actions() returned %q, which ValidAction rejects", a)
		}
		if !strings.Contains(a, ".") {
			t.Errorf("%q is not a dotted verb", a)
		}
	}
}

// ─── visibility (§5) ────────────────────────────────────────────────────────

// A panel owner reads everything, without a team lookup: the same superadmin
// bypass teams.RoleInTeam already grants.
func TestPanelOwnerSeesEveryScope(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	if _, err := s.List(context.Background(), domain.User{ID: "usr_o", Role: domain.RoleOwner}, Query{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !st.lastFilter.AllScopes {
		t.Error("a panel owner's filter did not set AllScopes")
	}
}

// A panel admin adds the panel-scoped rows — the servers and users they are the
// role that acts on — on top of their own teams.
func TestPanelAdminSeesPanelScopedRowsAndTheirTeams(t *testing.T) {
	st := newFakeStore()
	st.teams["usr_a"] = []domain.TeamWithRole{{Team: domain.Team{ID: "tm_1"}, Role: domain.RoleAdmin}}
	s := newService(t, st, 0)
	if _, err := s.List(context.Background(), domain.User{ID: "usr_a", Role: domain.RoleAdmin}, Query{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	f := st.lastFilter
	if f.AllScopes {
		t.Error("a panel admin was given the owner bypass")
	}
	if !f.PanelScope {
		t.Error("a panel admin cannot see panel-scoped rows")
	}
	if len(f.TeamIDs) != 1 || f.TeamIDs[0] != "tm_1" {
		t.Errorf("team ids = %v, want [tm_1]", f.TeamIDs)
	}
}

// A plain member sees their teams and their own actions — and NOT the
// panel-scoped rows, which is where the servers and the other accounts live.
func TestPlainMemberSeesNeitherEverythingNorPanelRows(t *testing.T) {
	st := newFakeStore()
	st.teams["usr_m"] = []domain.TeamWithRole{{Team: domain.Team{ID: "tm_1"}, Role: domain.RoleMember}}
	s := newService(t, st, 0)
	if _, err := s.List(context.Background(), member("usr_m"), Query{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	f := st.lastFilter
	if f.AllScopes || f.PanelScope {
		t.Errorf("a member was widened: AllScopes=%v PanelScope=%v", f.AllScopes, f.PanelScope)
	}
	if f.ViewerID != "usr_m" {
		t.Errorf("viewer id = %q, want usr_m", f.ViewerID)
	}
}

// Get applies exactly the same rule as List — an event in another team is
// ErrNotFound, never 403, so the log cannot be probed.
func TestGetHidesAnotherTeamsEvent(t *testing.T) {
	st := newFakeStore()
	st.teams["usr_m"] = []domain.TeamWithRole{{Team: domain.Team{ID: "tm_mine"}, Role: domain.RoleMember}}
	s := newService(t, st, 0)
	ctx := context.Background()
	theirs, err := s.Record(ctx, Entry{
		Action:   ActionApplicationDeleted,
		Actor:    domain.AuditActor{Kind: domain.AuditActorUser, UserID: "usr_other"},
		Resource: Resource(ResourceApplication, "app_x", "web"),
		TeamID:   "tm_theirs",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.Get(ctx, member("usr_m"), theirs.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(another team's event) = %v, want ErrNotFound", err)
	}
}

// Your own actions follow you: an entry you caused stays visible even when it
// landed in a scope you can no longer read.
func TestGetAlwaysShowsYourOwnAction(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ctx := context.Background()
	mine, err := s.Record(ctx, Entry{
		Action:   ActionTeamMemberRemoved,
		Actor:    domain.AuditActor{Kind: domain.AuditActorUser, UserID: "usr_m", Label: "m@example.com"},
		Resource: Resource(ResourceTeam, "tm_gone", "old team"),
		TeamID:   "tm_gone",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := s.Get(ctx, member("usr_m"), mine.ID)
	if err != nil {
		t.Fatalf("Get(own action) = %v, want it visible", err)
	}
	if got.ID != mine.ID {
		t.Errorf("got %q, want %q", got.ID, mine.ID)
	}
}

func TestGetPropagatesAStoreFailure(t *testing.T) {
	st := newFakeStore()
	st.getErr = errors.New("boom")
	s := newService(t, st, 0)
	if _, err := s.Get(context.Background(), member("usr_m"), "aud_x"); err == nil {
		t.Fatal("a store failure was reported as not-found")
	}
}

// ─── paging (§7) ────────────────────────────────────────────────────────────

func TestListClampsTheLimitAndDefaultsIt(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		in   int
		want int32
	}{
		{"absent", 0, defaultPageLimit},
		{"negative", -5, defaultPageLimit},
		{"over the cap", maxPageLimit * 10, maxPageLimit},
		{"in range", 7, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.List(ctx, member("usr_m"), Query{Limit: tc.in}); err != nil {
				t.Fatalf("List: %v", err)
			}
			if st.lastFilter.Limit != tc.want {
				t.Errorf("limit %d became %d, want %d", tc.in, st.lastFilter.Limit, tc.want)
			}
		})
	}
}

// A full page hands back a cursor; a short one is the end of the log.
func TestListReturnsACursorOnlyForAFullPage(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	ctx := context.Background()
	for range 3 {
		if _, err := s.Record(ctx, Entry{Action: ActionLogin, Resource: Resource(ResourceUser, "usr_1", "a@b.c")}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	full, err := s.List(ctx, member("usr_m"), Query{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if full.NextBefore != full.Events[2].ID {
		t.Errorf("next_before = %q, want the last event's id %q", full.NextBefore, full.Events[2].ID)
	}
	short, err := s.List(ctx, member("usr_m"), Query{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if short.NextBefore != "" {
		t.Errorf("a short page returned a cursor %q", short.NextBefore)
	}
}

func TestListRejectsAnUnknownOutcomeFilter(t *testing.T) {
	s := newService(t, newFakeStore(), 0)
	_, err := s.List(context.Background(), member("usr_m"), Query{Outcome: "partly"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("List(outcome=partly) = %v, want a ValidationError", err)
	}
}

func TestListPassesEveryFilterThrough(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	q := Query{
		TeamID: "tm_1", ProjectID: "prj_1", ResourceID: "app_1",
		Action: "deploy", Actor: "priya@example.com",
		Outcome: domain.AuditFailure, Since: since, Before: "aud_cursor",
	}
	if _, err := s.List(context.Background(), member("usr_m"), q); err != nil {
		t.Fatalf("List: %v", err)
	}
	f := st.lastFilter
	if f.TeamID != q.TeamID || f.ProjectID != q.ProjectID || f.ResourceID != q.ResourceID ||
		f.Action != q.Action || f.Actor != q.Actor || f.Outcome != q.Outcome ||
		!f.Since.Equal(since) || f.Before != q.Before {
		t.Fatalf("filters were not passed through: %+v", f)
	}
}

// ─── retention (§8) ─────────────────────────────────────────────────────────

// Retention disabled must cost nothing — and must never delete anything.
func TestPurgeIsANoOpWhenRetentionIsDisabled(t *testing.T) {
	st := newFakeStore()
	s := newService(t, st, 0)
	if _, err := s.Record(context.Background(), Entry{Action: ActionLogin, Resource: Resource(ResourceUser, "usr_1", "a@b.c")}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	n, err := s.Purge(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("Purge with retention disabled = (%d, %v), want (0, nil)", n, err)
	}
	if len(st.events) != 1 {
		t.Fatal("Purge deleted an event although retention is disabled")
	}
	// RunRetention returns immediately rather than ticking over a cutoff that
	// can never match.
	done := make(chan struct{})
	go func() {
		s.RunRetention(context.Background(), time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunRetention did not return with retention disabled")
	}
}

// A backlog longer than one batch drains in bounded steps rather than one lock
// over the whole table.
func TestPurgeDrainsInBoundedBatches(t *testing.T) {
	st := newFakeStore()
	st.events = make([]domain.AuditEvent, purgeBatch+7)
	s := newService(t, st, 24*time.Hour)
	n, err := s.Purge(context.Background())
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != int64(purgeBatch+7) {
		t.Errorf("purged %d, want %d", n, purgeBatch+7)
	}
	if len(st.purged) != 2 {
		t.Fatalf("purge ran %d times, want 2 bounded batches", len(st.purged))
	}
	for _, limit := range st.purged {
		if limit != purgeBatch {
			t.Errorf("batch limit = %d, want %d", limit, purgeBatch)
		}
	}
}

// The cutoff is read from the injected clock, so the horizon is deterministic
// (ENGINEERING rule 9).
func TestPurgeUsesTheInjectedClock(t *testing.T) {
	frozen := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	var seen time.Time
	// The store is wrapped so the cutoff is observable without widening the
	// Store interface for a test's benefit.
	s := newService(t, &cutoffRecorder{fakeStore: newFakeStore(), seen: &seen}, 90*24*time.Hour)
	s.now = func() time.Time { return frozen }
	if _, err := s.Purge(context.Background()); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if want := frozen.Add(-90 * 24 * time.Hour); !seen.Equal(want) {
		t.Errorf("cutoff = %s, want %s", seen, want)
	}
}

type cutoffRecorder struct {
	*fakeStore
	seen *time.Time
}

func (c *cutoffRecorder) PurgeAuditEvents(ctx context.Context, cutoff time.Time, limit int32) (int64, error) {
	*c.seen = cutoff
	return c.fakeStore.PurgeAuditEvents(ctx, cutoff, limit)
}

func TestPurgeSurfacesAStoreFailure(t *testing.T) {
	st := newFakeStore()
	st.purgeErr = errors.New("boom")
	s := newService(t, st, time.Hour)
	if _, err := s.Purge(context.Background()); err == nil {
		t.Fatal("a purge failure was swallowed")
	}
}

// A cancelled context ends the drain without reporting a failure: shutdown is
// not an error.
func TestPurgeStopsOnCancellation(t *testing.T) {
	st := newFakeStore()
	st.events = make([]domain.AuditEvent, purgeBatch*3)
	s := newService(t, st, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := s.Purge(ctx)
	if err != nil {
		t.Fatalf("a cancelled purge reported %v", err)
	}
	if n != 0 {
		t.Errorf("a cancelled purge deleted %d events", n)
	}
}

func TestRetentionIsReadable(t *testing.T) {
	s := newService(t, newFakeStore(), 90*24*time.Hour)
	if s.Retention() != 90*24*time.Hour {
		t.Errorf("Retention() = %s", s.Retention())
	}
}
