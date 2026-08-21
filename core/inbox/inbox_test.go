package inbox

// The acceptance list from notification-inbox.md §8, in order, plus the copy
// and validation rules §3 and §5 fix. What these prove is behaviour the fake
// cannot fake away: which users a fan-out reaches, how many rows one flood of
// successes becomes, and that converging twice equals converging once.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// day is the UTC calendar day every test's events fall in; the service's clock
// is pinned to it so the digest window is deterministic (ENGINEERING rule 9).
var day = time.Date(2026, 8, 21, 4, 2, 0, 0, time.UTC)

func newService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	s := New(st, quietLog())
	s.now = func() time.Time { return day }
	return s, st
}

func deployFailed(project, app, dep string) domain.NotifyEvent {
	return domain.NotifyEvent{
		Type: domain.EventDeployFailed, Level: domain.NotifyError,
		Title: "Deploy failed: web", Body: `Application "web" (revision rev_1) failed.`,
		ProjectID: project, ResourceKind: domain.WebhookResourceApplication,
		ResourceID: app, FocusID: dep,
	}
}

func backupSucceeded(project, db, rec string) domain.NotifyEvent {
	return domain.NotifyEvent{
		Type: domain.EventBackupSucceeded, Level: domain.NotifyInfo,
		Title: "Backup succeeded: atlas-pg", Body: `Database "atlas-pg" backup succeeded.`,
		ProjectID: project, ResourceKind: domain.WebhookResourceDatabase,
		ResourceID: db, FocusID: rec,
	}
}

func backupFailed(project, db, rec string) domain.NotifyEvent {
	ev := backupSucceeded(project, db, rec)
	ev.Type, ev.Level = domain.EventBackupFailed, domain.NotifyError
	ev.Title = "Backup failed: atlas-pg"
	return ev
}

// Acceptance 1. Two teams, one member each: a failed deploy in team A's project
// produces one unread item for A's member and ZERO rows for B's. This is the
// whole tenancy guarantee — leakage would require the recipient query to be
// wrong, and nothing else in the feature can leak (spec §5).
func TestFanoutReachesOnlyTheOwningTeam(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	st.members["prj_b"] = []string{"usr_b"}

	if err := s.Record(context.Background(), deployFailed("prj_a", "app_web", "dep_1")); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if n, _ := s.UnreadCount(context.Background(), "usr_a"); n != 1 {
		t.Fatalf("owner unread = %d, want 1", n)
	}
	if n, _ := s.UnreadCount(context.Background(), "usr_b"); n != 0 {
		t.Fatalf("outsider unread = %d, want 0", n)
	}
	page, err := s.List(context.Background(), "usr_b", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("outsider inbox = %+v, want empty", page.Items)
	}
}

// Acceptance 2, table-driven over the preference matrix: a mute suppresses
// exactly its own kind and nothing else. Mutes rather than subscriptions is the
// decision under test — an account that has never touched preferences receives
// everything (spec §2).
func TestPreferenceMatrix(t *testing.T) {
	cases := []struct {
		name      string
		muted     []string
		wantKinds []string
	}{
		{"nothing muted receives both", nil, []string{domain.EventBackupSucceeded, domain.EventDeployFailed}},
		{"muting the digest keeps the failure", []string{domain.EventBackupSucceeded}, []string{domain.EventDeployFailed}},
		{"muting the failure keeps the digest", []string{domain.EventDeployFailed}, []string{domain.EventBackupSucceeded}},
		{"muting both receives nothing", []string{domain.EventBackupSucceeded, domain.EventDeployFailed}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, st := newService(t)
			st.members["prj_a"] = []string{"usr_a"}
			st.prefs["usr_a"] = tc.muted
			ctx := context.Background()

			if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_1")); err != nil {
				t.Fatalf("Record deploy: %v", err)
			}
			if err := s.Record(ctx, backupSucceeded("prj_a", "db_1", "br_1")); err != nil {
				t.Fatalf("Record backup: %v", err)
			}

			page, err := s.List(ctx, "usr_a", ListOptions{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			got := map[string]bool{}
			for _, it := range page.Items {
				got[it.Kind] = true
			}
			if len(got) != len(tc.wantKinds) {
				t.Fatalf("kinds = %v, want %v", got, tc.wantKinds)
			}
			for _, k := range tc.wantKinds {
				if !got[k] {
					t.Fatalf("kind %q missing from %v", k, got)
				}
			}
		})
	}
}

// A teammate who muted nothing still gets both — the mute is per user, not per
// project (acceptance 2's second half).
func TestMuteIsPerUser(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_quiet", "usr_all"}
	st.prefs["usr_quiet"] = []string{domain.EventDeploySucceeded}
	ctx := context.Background()

	ev := deployFailed("prj_a", "app_web", "dep_1")
	ev.Type, ev.Level, ev.Title = domain.EventDeploySucceeded, domain.NotifyInfo, "Deploy succeeded: web"
	if err := s.Record(ctx, ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_2")); err != nil {
		t.Fatalf("Record failure: %v", err)
	}

	if n, _ := s.UnreadCount(ctx, "usr_quiet"); n != 1 {
		t.Fatalf("muted teammate unread = %d, want 1 (the failure only)", n)
	}
	if n, _ := s.UnreadCount(ctx, "usr_all"); n != 2 {
		t.Fatalf("unmuted teammate unread = %d, want 2 (digest + failure)", n)
	}
}

// Acceptance 3. Three successes in one project on one UTC day are ONE digest
// item reading "Backups: 3/3 succeeded", unread count 1 — not 3. That is the
// entire point of digesting.
func TestSuccessesDigestIntoOneUnread(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	for _, rec := range []string{"br_1", "br_2", "br_3"} {
		if err := s.Record(ctx, backupSucceeded("prj_a", "db_1", rec)); err != nil {
			t.Fatalf("Record %s: %v", rec, err)
		}
	}

	page, err := s.List(ctx, "usr_a", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1 digest", len(page.Items))
	}
	it := page.Items[0]
	if !it.Digest {
		t.Fatalf("item is not a digest: %+v", it)
	}
	if got := DisplayTitle(it); got != "Backups: 3/3 succeeded" {
		t.Fatalf("digest title = %q, want %q", got, "Backups: 3/3 succeeded")
	}
	if it.Link != "" || it.LinkLabel != "" {
		t.Fatalf("digest carries a link (%q/%q); a rollup has no single thing to open", it.Link, it.LinkLabel)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 1 {
		t.Fatalf("unread = %d, want 1", n)
	}
}

// Acceptance 4. Make the third fail: the failure is its own immediate item, and
// the digest denominator moves so "2/3" sits honestly beside it. The digest is
// never CREATED by a failure — a day of nothing but failures shows the
// failures, not a "0/2 succeeded" row nobody asked for.
func TestFailureBumpsTheDenominatorWithoutCreatingADigest(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	for _, rec := range []string{"br_1", "br_2"} {
		if err := s.Record(ctx, backupSucceeded("prj_a", "db_1", rec)); err != nil {
			t.Fatalf("Record %s: %v", rec, err)
		}
	}
	if err := s.Record(ctx, backupFailed("prj_a", "db_1", "br_3")); err != nil {
		t.Fatalf("Record failure: %v", err)
	}

	page, _ := s.List(ctx, "usr_a", ListOptions{})
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2 (one digest, one failure)", len(page.Items))
	}
	var digest, failure domain.InboxItem
	for _, it := range page.Items {
		if it.Digest {
			digest = it
		} else {
			failure = it
		}
	}
	if got := DisplayTitle(digest); got != "Backups: 2/3 succeeded" {
		t.Fatalf("digest title = %q, want %q", got, "Backups: 2/3 succeeded")
	}
	if failure.Severity != domain.NotifyError || failure.Kind != domain.EventBackupFailed {
		t.Fatalf("failure item = %+v", failure)
	}
	if failure.Link != "/projects/prj_a/databases/db_1/backups" || failure.LinkLabel != "View backups" {
		t.Fatalf("failure link = %q / %q", failure.Link, failure.LinkLabel)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 2 {
		t.Fatalf("unread = %d, want 2", n)
	}
}

// A failure on a day with no successes yet must not conjure a digest.
func TestFailureAloneCreatesNoDigest(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}

	if err := s.Record(context.Background(), backupFailed("prj_a", "db_1", "br_1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	page, _ := s.List(context.Background(), "usr_a", ListOptions{})
	if len(page.Items) != 1 || page.Items[0].Digest {
		t.Fatalf("items = %+v, want exactly one non-digest failure", page.Items)
	}
}

// Acceptance 5 — the idempotency test ENGINEERING rule 13 asks for: converging
// twice equals converging once. HandleDbBackupEvent has no terminal-state guard
// today, so a redelivered DbBackupEvent really does reach here twice; without
// the sources guard that would silently inflate a digest counter.
func TestRedeliveryChangesNothing(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	success := backupSucceeded("prj_a", "db_1", "br_1")
	failure := deployFailed("prj_a", "app_web", "dep_1")
	for i := 0; i < 2; i++ {
		if err := s.Record(ctx, success); err != nil {
			t.Fatalf("Record success #%d: %v", i, err)
		}
		if err := s.Record(ctx, failure); err != nil {
			t.Fatalf("Record failure #%d: %v", i, err)
		}
	}

	page, _ := s.List(ctx, "usr_a", ListOptions{})
	if len(page.Items) != 2 {
		t.Fatalf("items = %d after redelivery, want 2", len(page.Items))
	}
	for _, it := range page.Items {
		if it.Digest {
			if it.CountOK != 1 || it.CountTotal != 1 {
				t.Fatalf("digest counters = %d/%d after redelivery, want 1/1", it.CountOK, it.CountTotal)
			}
			if len(it.Sources) != 1 {
				t.Fatalf("digest sources = %v after redelivery, want one entry", it.Sources)
			}
		}
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 2 {
		t.Fatalf("unread = %d after redelivery, want 2", n)
	}
}

// Acceptance 6. Mark one read, then all: the count falls and the items STAY
// listed, because reading is not deleting. Marking an already-read item is a
// no-op that still succeeds.
func TestMarkReadIsIdempotentAndKeepsItems(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	for _, dep := range []string{"dep_1", "dep_2", "dep_3"} {
		if err := s.Record(ctx, deployFailed("prj_a", "app_web", dep)); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	page, _ := s.List(ctx, "usr_a", ListOptions{})
	first := page.Items[0].ID

	if err := s.MarkRead(ctx, "usr_a", first); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 2 {
		t.Fatalf("unread after one = %d, want 2", n)
	}
	if err := s.MarkRead(ctx, "usr_a", first); err != nil {
		t.Fatalf("MarkRead twice: %v", err)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 2 {
		t.Fatalf("unread after re-marking = %d, want 2", n)
	}

	marked, err := s.MarkAllRead(ctx, "usr_a")
	if err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if marked != 2 {
		t.Fatalf("marked = %d, want 2 (the already-read one is not counted twice)", marked)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 0 {
		t.Fatalf("unread after all = %d, want 0", n)
	}
	after, _ := s.List(ctx, "usr_a", ListOptions{})
	if len(after.Items) != 3 {
		t.Fatalf("items after marking all read = %d, want 3 — reading is not deleting", len(after.Items))
	}
	unread, _ := s.List(ctx, "usr_a", ListOptions{UnreadOnly: true})
	if len(unread.Items) != 0 {
		t.Fatalf("unread filter = %d items, want 0", len(unread.Items))
	}
}

// Someone else's item is not addressable, and answers exactly as a nonexistent
// one does (spec §5).
func TestMarkReadRefusesAnotherUsersItem(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	st.members["prj_b"] = []string{"usr_b"}
	ctx := context.Background()

	if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	page, _ := s.List(ctx, "usr_a", ListOptions{})
	theirs := page.Items[0].ID

	if err := s.MarkRead(ctx, "usr_b", theirs); err == nil {
		t.Fatal("marking another user's item succeeded; it must be not-found")
	}
	if err := s.MarkRead(ctx, "usr_b", "inb_does_not_exist"); err == nil {
		t.Fatal("marking a nonexistent item succeeded")
	}
}

// Acceptance 8. A user at the cap receives one more and the oldest goes. The
// prune runs once per event, not once per recipient.
func TestRetentionPrunesTheOldest(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	total := domain.InboxRetention + 1
	for i := 0; i < total; i++ {
		if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_"+itoa(i))); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	if got := len(st.items["usr_a"]); got != domain.InboxRetention {
		t.Fatalf("stored items = %d, want the cap %d", got, domain.InboxRetention)
	}
	for _, it := range st.items["usr_a"] {
		if it.DedupeKey == domain.EventDeployFailed+":dep_0" {
			t.Fatal("the oldest item survived the prune")
		}
	}
	if st.prunes != total {
		t.Fatalf("prune calls = %d, want one per event (%d)", st.prunes, total)
	}
}

// Paging is keyset: the cursor page is strictly older, with no overlap and no
// gap (spec §6).
func TestKeysetPaging(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_"+itoa(i))); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	first, err := s.List(ctx, "usr_a", ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(first.Items) != 2 || first.NextBefore == "" {
		t.Fatalf("first page = %d items, cursor %q", len(first.Items), first.NextBefore)
	}
	second, err := s.List(ctx, "usr_a", ListOptions{Limit: 2, Before: first.NextBefore})
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(second.Items) != 2 {
		t.Fatalf("second page = %d items, want 2", len(second.Items))
	}
	for _, a := range first.Items {
		for _, b := range second.Items {
			if a.ID == b.ID {
				t.Fatalf("item %s appeared on both pages", a.ID)
			}
		}
	}
	last, _ := s.List(ctx, "usr_a", ListOptions{Limit: 2, Before: second.NextBefore})
	if last.NextBefore != "" {
		t.Fatalf("final page still offers a cursor %q", last.NextBefore)
	}
}

func TestListLimitCaps(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.Record(ctx, deployFailed("prj_a", "app_web", "dep_"+itoa(i))); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	// A limit above the cap must not become an unbounded scan; the page is
	// still served, just bounded.
	page, err := s.List(ctx, "usr_a", ListOptions{Limit: 100000})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(page.Items))
	}
}

// The deep link is rendered server-side so a CLI prints the same words the
// drawer does, and it is always an in-panel path (spec §3, §5).
func TestDeepLinks(t *testing.T) {
	cases := []struct {
		name      string
		ev        domain.NotifyEvent
		wantPath  string
		wantLabel string
	}{
		{
			"deployment", deployFailed("prj_1", "app_1", "dep_1"),
			"/projects/prj_1/applications/app_1/deployments?dep=dep_1", "View deployment",
		},
		{
			"backup", backupFailed("prj_1", "db_1", "br_1"),
			"/projects/prj_1/databases/db_1/backups", "View backups",
		},
		{
			"unknown resource kind carries no link",
			domain.NotifyEvent{ProjectID: "prj_1", ResourceKind: "mystery", ResourceID: "x", FocusID: "y"},
			"", "",
		},
		{
			"a deployment with no focus has nothing to open",
			domain.NotifyEvent{ProjectID: "prj_1", ResourceKind: domain.WebhookResourceApplication, ResourceID: "app_1"},
			"", "",
		},
		{
			"an id that would escape the panel is rejected",
			domain.NotifyEvent{
				ProjectID: "prj_1", ResourceKind: domain.WebhookResourceDatabase,
				ResourceID: "..//evil.example.com",
			},
			"", "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, label := deepLink(tc.ev)
			if path != tc.wantPath || label != tc.wantLabel {
				t.Fatalf("deepLink = %q / %q, want %q / %q", path, label, tc.wantPath, tc.wantLabel)
			}
		})
	}
}

func TestValidPathRejectsEscapes(t *testing.T) {
	bad := []string{
		"https://evil.example/x", "//evil.example/x", "/a//b", "javascript:alert(1)",
		"relative/path", "/has space", "/has\nnewline",
	}
	for _, p := range bad {
		if validPath(p) {
			t.Errorf("validPath(%q) = true, want false", p)
		}
	}
	if !validPath("/projects/prj_1/applications/app_1/deployments?dep=dep_1") {
		t.Error("a legitimate in-panel path was rejected")
	}
}

// A body is capped, and the cut lands on a rune boundary rather than splitting
// a multi-byte character into mojibake (spec §5).
func TestBodyIsClampedOnARuneBoundary(t *testing.T) {
	body := strings.Repeat("é", domain.InboxBodyMax) // two bytes each
	got := clampBody(body)
	if len(got) > domain.InboxBodyMax {
		t.Fatalf("clamped body = %d bytes, want <= %d", len(got), domain.InboxBodyMax)
	}
	if !utf8Valid(got) {
		t.Fatal("clamp split a multi-byte rune")
	}
	if short := "already small"; clampBody(short) != short {
		t.Fatal("a short body was altered")
	}
}

// An event outside the taxonomy, or one with no project, is a silent no-op: the
// outcome is already recorded and logged elsewhere, and there is no team to fan
// it out to.
func TestUnroutableEventsAreNoOps(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	ctx := context.Background()

	ping := domain.NotifyEvent{Type: domain.EventWebhookPing, Level: domain.NotifyInfo, ProjectID: "prj_a"}
	if err := s.Record(ctx, ping); err != nil {
		t.Fatalf("Record ping: %v", err)
	}
	orphan := deployFailed("", "app_web", "dep_1")
	if err := s.Record(ctx, orphan); err != nil {
		t.Fatalf("Record projectless: %v", err)
	}
	if n, _ := s.UnreadCount(ctx, "usr_a"); n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
}

// A recipient query that fails is an error the caller logs, not a silent drop.
func TestRecordSurfacesStoreErrors(t *testing.T) {
	s, st := newService(t)
	st.members["prj_a"] = []string{"usr_a"}
	st.failRecipients = true
	if err := s.Record(context.Background(), deployFailed("prj_a", "app_web", "dep_1")); err == nil {
		t.Fatal("Record swallowed a store failure")
	}
}

func TestDigestTitleTrimsWhatCannotBeProven(t *testing.T) {
	// The board reads "Nightly backups: 3/3 succeeded, verified". The plane does
	// not know a schedule's cadence and nothing verifies a backup, so both
	// claims are gone (spec §3).
	got := DigestTitle(domain.EventBackupSucceeded, 3, 3)
	if got != "Backups: 3/3 succeeded" {
		t.Fatalf("DigestTitle = %q", got)
	}
	if strings.Contains(got, "Nightly") || strings.Contains(got, "verified") {
		t.Fatalf("DigestTitle claims what we cannot prove: %q", got)
	}
	if got := DigestTitle(domain.EventDeploySucceeded, 7, 9); got != "Deploys: 7/9 succeeded" {
		t.Fatalf("deploy digest title = %q", got)
	}
}

// An immediate item's title is the event's own, verbatim — the bell and the
// Slack message say the same words because both render one NotifyEvent.
func TestDisplayTitlePassesImmediateItemsThrough(t *testing.T) {
	it := domain.InboxItem{Title: "Deploy failed: web", CountOK: 1, CountTotal: 1}
	if got := DisplayTitle(it); got != "Deploy failed: web" {
		t.Fatalf("DisplayTitle = %q", got)
	}
}

func TestSetPreferencesValidatesAndDeduplicates(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()

	if _, err := s.SetPreferences(ctx, "usr_a", []string{"deploy.exploded"}); err == nil {
		t.Fatal("an unknown kind was accepted; a typo that mutes nothing is indistinguishable from a preference that works")
	}
	p, err := s.SetPreferences(ctx, "usr_a", []string{domain.EventDeployFailed, domain.EventDeployFailed})
	if err != nil {
		t.Fatalf("SetPreferences: %v", err)
	}
	if len(p.MutedKinds) != 1 {
		t.Fatalf("muted = %v, want one entry after dedupe", p.MutedKinds)
	}
	// An empty set means "everything on" and must round-trip as one.
	cleared, err := s.SetPreferences(ctx, "usr_a", nil)
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if len(cleared.MutedKinds) != 0 {
		t.Fatalf("cleared muted = %v", cleared.MutedKinds)
	}
	back, err := s.Preferences(ctx, "usr_a")
	if err != nil {
		t.Fatalf("Preferences: %v", err)
	}
	if back.MutedKinds == nil {
		t.Fatal("an absent preference set came back nil; the API must serve an empty array")
	}
}

func TestAvailableKindsIsTheTaxonomy(t *testing.T) {
	got := AvailableKinds()
	if len(got) != len(domain.EventTypes()) {
		t.Fatalf("AvailableKinds = %v, want the taxonomy %v", got, domain.EventTypes())
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// utf8Valid reports whether s is well-formed UTF-8, without importing the
// package the clamp itself leans on.
func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
