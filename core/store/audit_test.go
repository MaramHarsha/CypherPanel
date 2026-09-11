package store

// Real-Postgres tests for the audit log (audit-log.md §2, §4, §5, §8). They
// prove the three things only the database can: the ownership chain resolved
// inside the INSERT, the visibility predicate that decides who reads what, and
// the fact that an entry outlives everything it names.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// auditEvent is a minimal valid event; each test overrides what it cares about.
func auditEvent(action string) domain.AuditEvent {
	return domain.AuditEvent{
		ID:       ids.New(ids.PrefixAuditEvent),
		Action:   action,
		Outcome:  domain.AuditSuccess,
		Actor:    domain.AuditActor{Kind: domain.AuditActorSystem, Label: "test"},
		Resource: domain.AuditResource{Kind: "panel", ID: "panel", Name: "panel"},
	}
}

// allScopes is the filter a panel owner gets: it isolates whatever the test is
// actually asserting from the visibility rules, which have their own test.
func allScopes(limit int32) AuditFilter {
	return AuditFilter{AllScopes: true, TeamIDs: []string{}, Limit: limit}
}

func TestStoreAuditRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	in := auditEvent("application.deleted")
	in.Actor = domain.AuditActor{
		Kind: domain.AuditActorToken, UserID: "usr_x", TokenID: "tok_y", Label: "priya@example.com",
	}
	in.Resource = domain.AuditResource{Kind: "application", ID: "app_gone", Name: "notify-svc"}
	in.Detail = map[string]any{"key": "DATABASE_URL", "count": float64(3)}
	in.TraceID = "trace_1111-2222-3333-4444"
	in.ClientIP = "203.0.113.7"

	out, err := s.InsertAuditEvent(ctx, in)
	if err != nil {
		t.Fatalf("InsertAuditEvent: %v", err)
	}
	if out.At.IsZero() {
		t.Error("at was not stamped by the database")
	}
	got, err := s.GetAuditEvent(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetAuditEvent: %v", err)
	}
	if got.Actor != in.Actor || got.Resource != in.Resource {
		t.Errorf("actor/resource round-trip: got %+v %+v", got.Actor, got.Resource)
	}
	if got.TraceID != in.TraceID || got.ClientIP != in.ClientIP || got.Outcome != domain.AuditSuccess {
		t.Errorf("provenance round-trip: %+v", got)
	}
	if got.Detail["key"] != "DATABASE_URL" || got.Detail["count"] != float64(3) {
		t.Errorf("detail round-trip: %+v", got.Detail)
	}
	if _, err := s.GetAuditEvent(ctx, "aud_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAuditEvent(missing) = %v, want ErrNotFound", err)
	}
}

// Snapshot strings are caller-supplied and multibyte. Postgres refuses a string
// that is not valid UTF-8 outright (`invalid byte sequence for encoding
// "UTF8"`), and a refused INSERT is a MISSING audit entry — so the bounds
// core/audit applies must land on rune boundaries. This is the store-side half
// of that contract: what a bounded multibyte snapshot looks like goes in and
// comes back byte for byte.
func TestStoreAuditRoundtripsMultibyteSnapshots(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 66 three-byte runes plus the ellipsis: 201 bytes, the shape truncate
	// produces at the 200-byte resource-name cap plus its marker.
	name := strings.Repeat("日", 66) + "…"
	label := strings.Repeat("é", 100) + "…"
	in := auditEvent("application.deleted")
	in.Actor = domain.AuditActor{Kind: domain.AuditActorAnonymous, Label: label}
	in.Resource = domain.AuditResource{Kind: "application", ID: "app_x", Name: name}
	in.Detail = map[string]any{"note": strings.Repeat("🔒", 50)}

	out, err := s.InsertAuditEvent(ctx, in)
	if err != nil {
		t.Fatalf("InsertAuditEvent with multibyte snapshots: %v", err)
	}
	got, err := s.GetAuditEvent(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetAuditEvent: %v", err)
	}
	if got.Resource.Name != name || got.Actor.Label != label {
		t.Errorf("multibyte snapshots did not round-trip: name %q label %q", got.Resource.Name, got.Actor.Label)
	}
	if got.Detail["note"] != in.Detail["note"] {
		t.Errorf("multibyte detail did not round-trip: %+v", got.Detail)
	}
}

// An absent detail comes back as an empty object, never nil: every reader gets
// an object to index into.
func TestStoreAuditEmptyDetailIsAnObject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	out, err := s.InsertAuditEvent(ctx, auditEvent("auth.logout"))
	if err != nil {
		t.Fatalf("InsertAuditEvent: %v", err)
	}
	got, err := s.GetAuditEvent(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetAuditEvent: %v", err)
	}
	if got.Detail == nil || len(got.Detail) != 0 {
		t.Errorf("detail = %#v, want an empty object", got.Detail)
	}
}

// The insert completes the ownership chain from whichever link the caller knew
// (spec §4) — the property that lets a handler pass only an environment id.
func TestStoreAuditResolvesTheOwnershipChain(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, env, _ := seedApp(t, s)

	t.Run("from the environment", func(t *testing.T) {
		in := auditEvent("application.created")
		in.EnvironmentID = env.ID
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		if out.ProjectID != proj.ID {
			t.Errorf("project_id = %q, want %q", out.ProjectID, proj.ID)
		}
		if out.TeamID != proj.TeamID {
			t.Errorf("team_id = %q, want %q", out.TeamID, proj.TeamID)
		}
	})

	t.Run("from the project", func(t *testing.T) {
		in := auditEvent("project.updated")
		in.ProjectID = proj.ID
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		if out.TeamID != proj.TeamID || out.EnvironmentID != "" {
			t.Errorf("scope = team %q env %q, want team %q and no environment", out.TeamID, out.EnvironmentID, proj.TeamID)
		}
	})

	t.Run("an explicit team wins", func(t *testing.T) {
		in := auditEvent("team.deleted")
		in.TeamID = "tm_default"
		in.ProjectID = proj.ID
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		if out.TeamID != "tm_default" {
			t.Errorf("team_id = %q, want the explicit tm_default", out.TeamID)
		}
	})

	t.Run("a panel-level action stays unscoped", func(t *testing.T) {
		out, err := s.InsertAuditEvent(ctx, auditEvent("panel.mail_updated"))
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		if out.TeamID != "" || out.ProjectID != "" || out.EnvironmentID != "" {
			t.Errorf("a panel action acquired a scope: %+v", out)
		}
	})
}

// The reason there are no foreign keys (spec §2): an entry has to outlive the
// thing it describes, or deleting a project would delete the record of the
// deletion.
func TestStoreAuditEntriesOutliveTheirResources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, proj, env, app := seedApp(t, s)

	in := auditEvent("application.deleted")
	in.EnvironmentID = env.ID
	in.Resource = domain.AuditResource{Kind: "application", ID: app.ID, Name: app.Name}
	in.Actor = domain.AuditActor{Kind: domain.AuditActorUser, UserID: "usr_since_deleted", Label: "gone@example.com"}
	out, err := s.InsertAuditEvent(ctx, in)
	if err != nil {
		t.Fatalf("InsertAuditEvent: %v", err)
	}
	if err := s.DeleteApplication(ctx, app.ID); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	got, err := s.GetAuditEvent(ctx, out.ID)
	if err != nil {
		t.Fatalf("the entry did not survive its project: %v", err)
	}
	// The snapshots are still readable, which is what makes "who deleted
	// notify-svc?" answerable after the fact.
	if got.Resource.Name != app.Name || got.Actor.Label != "gone@example.com" {
		t.Errorf("snapshots lost: %+v %+v", got.Resource, got.Actor)
	}
	if got.ProjectID != proj.ID || got.TeamID != proj.TeamID {
		t.Errorf("scope lost with the project: %+v", got)
	}
}

// The visibility predicate (spec §5), against the real SQL.
func TestStoreAuditVisibility(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mine, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "audit-mine-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	theirs, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "audit-theirs-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	viewer := "usr_viewer_" + ids.Secret()[:8]

	insert := func(action, teamID, actorID string) string {
		t.Helper()
		in := auditEvent(action)
		in.TeamID = teamID
		if actorID != "" {
			in.Actor = domain.AuditActor{Kind: domain.AuditActorUser, UserID: actorID, Label: actorID + "@example.com"}
		}
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		return out.ID
	}
	inMyTeam := insert("project.created", mine.ID, "usr_someone_else")
	inTheirTeam := insert("project.created", theirs.ID, "usr_someone_else")
	panelLevel := insert("server.created", "", "usr_someone_else")
	myOwnInTheirTeam := insert("team.member_removed", theirs.ID, viewer)

	ids := func(f AuditFilter) map[string]bool {
		t.Helper()
		f.Limit = 500
		if f.TeamIDs == nil {
			f.TeamIDs = []string{}
		}
		events, err := s.ListAuditEvents(ctx, f)
		if err != nil {
			t.Fatalf("ListAuditEvents: %v", err)
		}
		seen := map[string]bool{}
		for _, e := range events {
			seen[e.ID] = true
		}
		return seen
	}

	t.Run("a member sees their team and their own actions, nothing else", func(t *testing.T) {
		seen := ids(AuditFilter{ViewerID: viewer, TeamIDs: []string{mine.ID}})
		if !seen[inMyTeam] {
			t.Error("a member cannot see their own team's row")
		}
		if !seen[myOwnInTheirTeam] {
			t.Error("a member cannot see an action they performed themselves")
		}
		if seen[inTheirTeam] {
			t.Error("a member can read another team's row")
		}
		if seen[panelLevel] {
			t.Error("a member can read a panel-level row")
		}
	})

	t.Run("a panel admin also sees panel-level rows", func(t *testing.T) {
		seen := ids(AuditFilter{ViewerID: viewer, TeamIDs: []string{mine.ID}, PanelScope: true})
		if !seen[panelLevel] {
			t.Error("a panel admin cannot read a panel-level row")
		}
		if seen[inTheirTeam] {
			t.Error("panel scope leaked another team's row")
		}
	})

	t.Run("a panel owner sees everything", func(t *testing.T) {
		seen := ids(AuditFilter{AllScopes: true})
		for _, id := range []string{inMyTeam, inTheirTeam, panelLevel, myOwnInTheirTeam} {
			if !seen[id] {
				t.Errorf("a panel owner cannot see %s", id)
			}
		}
	})

	t.Run("a team filter cannot widen visibility", func(t *testing.T) {
		// Asking for a team you do not belong to yields an empty page, not a
		// leak and not an error.
		seen := ids(AuditFilter{ViewerID: viewer, TeamIDs: []string{mine.ID}, TeamID: theirs.ID})
		if seen[inTheirTeam] {
			t.Error("filtering by another team's id returned its rows")
		}
	})
}

// The filters, against the real SQL — including the family prefix, which is the
// one that is easy to get wrong.
func TestStoreAuditFilters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "audit-filters-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}

	mk := func(action, resourceID, actorLabel, outcome string) string {
		t.Helper()
		in := auditEvent(action)
		in.TeamID = team.ID
		in.Outcome = outcome
		in.Resource = domain.AuditResource{Kind: "application", ID: resourceID, Name: "web"}
		in.Actor = domain.AuditActor{Kind: domain.AuditActorUser, UserID: "usr_a", Label: actorLabel}
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		return out.ID
	}
	started := mk("deploy.started", "app_1", "priya@example.com", domain.AuditSuccess)
	rolled := mk("deploy.rolled_back", "app_1", "sam@example.com", domain.AuditSuccess)
	frozen := mk("deploy.started", "app_2", "priya@example.com", domain.AuditFailure)
	created := mk("application.created", "app_2", "sam@example.com", domain.AuditSuccess)

	base := func() AuditFilter {
		return AuditFilter{AllScopes: true, TeamIDs: []string{}, TeamID: team.ID, Limit: 100}
	}
	run := func(t *testing.T, f AuditFilter) map[string]bool {
		t.Helper()
		events, err := s.ListAuditEvents(ctx, f)
		if err != nil {
			t.Fatalf("ListAuditEvents: %v", err)
		}
		seen := map[string]bool{}
		for _, e := range events {
			seen[e.ID] = true
		}
		return seen
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*AuditFilter)
		want    []string
		notWant []string
	}{
		{
			name:    "an exact action",
			mutate:  func(f *AuditFilter) { f.Action = "deploy.rolled_back" },
			want:    []string{rolled},
			notWant: []string{started, created},
		},
		{
			name:    "a whole family by prefix",
			mutate:  func(f *AuditFilter) { f.Action = "deploy" },
			want:    []string{started, rolled, frozen},
			notWant: []string{created},
		},
		{
			name:    "an actor by label",
			mutate:  func(f *AuditFilter) { f.Actor = "priya@example.com" },
			want:    []string{started, frozen},
			notWant: []string{rolled},
		},
		{
			name:    "an actor by id",
			mutate:  func(f *AuditFilter) { f.Actor = "usr_a" },
			want:    []string{started, rolled, frozen, created},
			notWant: nil,
		},
		{
			name:    "one resource's timeline",
			mutate:  func(f *AuditFilter) { f.ResourceID = "app_2" },
			want:    []string{frozen, created},
			notWant: []string{started, rolled},
		},
		{
			name:    "only refusals",
			mutate:  func(f *AuditFilter) { f.Outcome = domain.AuditFailure },
			want:    []string{frozen},
			notWant: []string{started, rolled, created},
		},
		{
			name:    "a time window",
			mutate:  func(f *AuditFilter) { f.Since = time.Now().Add(-time.Minute) },
			want:    []string{started, rolled, frozen, created},
			notWant: nil,
		},
		{
			name:    "a window that excludes everything",
			mutate:  func(f *AuditFilter) { f.Since = time.Now().Add(time.Hour) },
			want:    nil,
			notWant: []string{started, rolled, frozen, created},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.mutate(&f)
			seen := run(t, f)
			for _, id := range tc.want {
				if !seen[id] {
					t.Errorf("%s is missing from the page", id)
				}
			}
			for _, id := range tc.notWant {
				if seen[id] {
					t.Errorf("%s should have been filtered out", id)
				}
			}
		})
	}
}

// Seek pagination on (at, id) DESC: consecutive pages neither repeat nor skip.
func TestStoreAuditCursorPaging(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "audit-paging-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	const total = 5
	for range total {
		in := auditEvent("auth.login")
		in.TeamID = team.ID
		if _, err := s.InsertAuditEvent(ctx, in); err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
	}

	page := func(before string) []domain.AuditEvent {
		t.Helper()
		f := allScopes(2)
		f.TeamID = team.ID
		f.Before = before
		events, err := s.ListAuditEvents(ctx, f)
		if err != nil {
			t.Fatalf("ListAuditEvents: %v", err)
		}
		return events
	}

	var walked []string
	cursor := ""
	for range total {
		events := page(cursor)
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			walked = append(walked, e.ID)
		}
		cursor = events[len(events)-1].ID
	}
	if len(walked) != total {
		t.Fatalf("walked %d events, want %d (%v)", len(walked), total, walked)
	}
	seen := map[string]bool{}
	for _, id := range walked {
		if seen[id] {
			t.Fatalf("event %s appeared on two pages", id)
		}
		seen[id] = true
	}

	// A cursor the caller cannot see resolves to nothing, so the page is empty
	// rather than restarting at the newest row.
	hidden := auditEvent("auth.login")
	hidden.TeamID = "tm_someone_else"
	hiddenEv, err := s.InsertAuditEvent(ctx, hidden)
	if err != nil {
		t.Fatalf("InsertAuditEvent: %v", err)
	}
	f := AuditFilter{TeamIDs: []string{team.ID}, ViewerID: "usr_nobody", Limit: 10, Before: hiddenEv.ID}
	events, err := s.ListAuditEvents(ctx, f)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("an unreachable cursor returned %d rows, want 0", len(events))
	}
}

// Retention (spec §8): only rows past the cutoff go, in bounded batches.
func TestStoreAuditPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "audit-purge-"+ids.Secret()[:8])
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	insert := func() domain.AuditEvent {
		t.Helper()
		in := auditEvent("auth.login")
		in.TeamID = team.ID
		out, err := s.InsertAuditEvent(ctx, in)
		if err != nil {
			t.Fatalf("InsertAuditEvent: %v", err)
		}
		return out
	}
	old := insert()
	// The cutoff comes from the DATABASE's own stamps, not the test process's
	// clock: `at` is set by now() inside the insert, and comparing it against a
	// time read here would make the test fail by exactly the clock skew.
	time.Sleep(5 * time.Millisecond)
	fresh := insert()
	if !old.At.Before(fresh.At) {
		t.Fatalf("the two events share a timestamp (%s), so the cutoff proves nothing", old.At)
	}

	// Nothing is old enough yet.
	n, err := s.PurgeAuditEvents(ctx, old.At, 100)
	if err != nil {
		t.Fatalf("PurgeAuditEvents: %v", err)
	}
	if _, err := s.GetAuditEvent(ctx, old.ID); err != nil {
		t.Fatalf("an event exactly at the cutoff was removed (purged %d): %v", n, err)
	}

	// One bounded batch removes at most `limit` rows.
	n, err = s.PurgeAuditEvents(ctx, fresh.At, 1)
	if err != nil {
		t.Fatalf("PurgeAuditEvents: %v", err)
	}
	if n != 1 {
		t.Fatalf("a batch of 1 removed %d rows", n)
	}

	// Draining the rest takes everything before the cutoff and stops there.
	for {
		n, err := s.PurgeAuditEvents(ctx, fresh.At, 1000)
		if err != nil {
			t.Fatalf("PurgeAuditEvents: %v", err)
		}
		if n < 1000 {
			break
		}
	}
	if _, err := s.GetAuditEvent(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAuditEvent(purged) = %v, want ErrNotFound", err)
	}
	if _, err := s.GetAuditEvent(ctx, fresh.ID); err != nil {
		t.Errorf("the event at the cutoff was purged: %v", err)
	}
}
