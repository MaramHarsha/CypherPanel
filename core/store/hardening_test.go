package store

// Real-Postgres tests for control-plane-hardening.md: the session purge (§7),
// the panel-level inbox fan-out (§3) and the deploy-key blocker list (§8).

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// TestStoreExpiredSessionPurge: the purge deletes exactly the rows at or before
// the cutoff and reports the count; a live session survives and still resolves.
func TestStoreExpiredSessionPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "purge-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	now := time.Now()
	liveHash := []byte("live-" + ids.Secret())
	for _, sess := range []struct {
		hash    []byte
		expires time.Time
	}{
		{[]byte("expired-1-" + ids.Secret()), now.Add(-2 * time.Hour)},
		{[]byte("expired-2-" + ids.Secret()), now.Add(-time.Minute)},
		{liveHash, now.Add(time.Hour)},
	} {
		if err := s.CreateSession(ctx, ids.New(ids.PrefixSession), user.ID, sess.hash, sess.expires); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	// Other tests may have left expired rows behind, so the count is a floor,
	// and the cutoff is "now" rather than a fixed instant.
	n, err := s.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if n < 2 {
		t.Fatalf("purged %d rows, want at least the 2 expired ones", n)
	}
	if _, _, err := s.SessionForToken(ctx, liveHash); err != nil {
		t.Fatalf("the live session was purged: %v", err)
	}
	if again, err := s.DeleteExpiredSessions(ctx, now); err != nil || again != 0 {
		t.Fatalf("second purge = %d, %v; want 0, nil", again, err)
	}
	// With the clock moved past its expiry, the live one goes too — the
	// cutoff parameter is what makes the purge deterministic.
	if n, err := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("future-cutoff purge = %d, %v; want 1, nil", n, err)
	}
}

// TestStorePanelInboxItems: a panel-level kind reaches panel owners and team
// owners (once each, minus muters), writes a project-less item exactly once per
// dedupe key, and survives the team-removal sweep that project items do not.
func TestStorePanelInboxItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	suffix := ids.Secret()[:8]
	mk := func(label, role string) domain.User {
		t.Helper()
		u, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), label+"-"+suffix+"@example.test", "hash", role)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", label, err)
		}
		return u
	}
	panelOwner := mk("panel-owner", domain.RoleOwner)
	teamOwner := mk("team-owner", domain.RoleMember)
	both := mk("both", domain.RoleOwner)
	member := mk("member", domain.RoleMember)
	muted := mk("muted-owner", domain.RoleOwner)

	team, err := s.CreateTeam(ctx, ids.New(ids.PrefixTeam), "panel-"+suffix)
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	for _, m := range []struct{ user, role string }{
		{teamOwner.ID, domain.RoleOwner}, {both.ID, domain.RoleOwner}, {member.ID, domain.RoleMember},
	} {
		if _, err := s.UpsertTeamMember(ctx, team.ID, m.user, m.role); err != nil {
			t.Fatalf("UpsertTeamMember: %v", err)
		}
	}
	if _, err := s.SetInboxPreferences(ctx, muted.ID, []string{domain.InboxKindPanelUpdateAvailable}); err != nil {
		t.Fatalf("SetInboxPreferences: %v", err)
	}

	recipients, err := s.ListPanelInboxRecipients(ctx, domain.InboxKindPanelUpdateAvailable)
	if err != nil {
		t.Fatalf("ListPanelInboxRecipients: %v", err)
	}
	for _, want := range []string{panelOwner.ID, teamOwner.ID, both.ID} {
		if !containsString(recipients, want) {
			t.Errorf("recipients %v lack %s", recipients, want)
		}
	}
	for _, unwanted := range []string{member.ID, muted.ID} {
		if containsString(recipients, unwanted) {
			t.Errorf("recipients %v include %s, who is a plain member or muted the kind", recipients, unwanted)
		}
	}
	seen := map[string]int{}
	for _, r := range recipients {
		seen[r]++
	}
	if seen[both.ID] != 1 {
		t.Errorf("a panel owner who is also a team owner appears %d times, want once", seen[both.ID])
	}

	// One item per recipient, no project, and a second write of the same
	// version is a no-op (the once-per-version guard across restarts).
	write := func() {
		t.Helper()
		targets := []string{panelOwner.ID, teamOwner.ID}
		err := s.InsertPanelInboxItems(ctx, InboxFanout{
			IDs:       []string{ids.New(ids.PrefixInboxItem), ids.New(ids.PrefixInboxItem)},
			UserIDs:   targets,
			Kind:      domain.InboxKindPanelUpdateAvailable,
			Severity:  string(domain.NotifyInfo),
			Title:     "CypherPanel v9.9.9 is available",
			Body:      "You're on v9.9.8.",
			DedupeKey: domain.InboxKindPanelUpdateAvailable + ":v9.9.9",
		})
		if err != nil {
			t.Fatalf("InsertPanelInboxItems: %v", err)
		}
	}
	write()
	write()
	items, err := s.ListInboxItems(ctx, teamOwner.ID, false, 10)
	if err != nil {
		t.Fatalf("ListInboxItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("team owner holds %d items, want exactly 1 after two identical writes", len(items))
	}
	if items[0].ProjectID != "" || items[0].Kind != domain.InboxKindPanelUpdateAvailable || items[0].Link != "" {
		t.Fatalf("item = %+v, want a project-less, link-less panel item", items[0])
	}
	if items[0].ReadAt != nil {
		t.Fatal("a fresh panel item must be unread")
	}

	// Leaving the team removes that team's project items, never panel items.
	if err := s.DeleteInboxItemsForTeamMember(ctx, team.ID, teamOwner.ID); err != nil {
		t.Fatalf("DeleteInboxItemsForTeamMember: %v", err)
	}
	if n, err := s.CountUnreadInboxItems(ctx, teamOwner.ID); err != nil || n != 1 {
		t.Fatalf("unread after team removal = %d, %v; want the panel item kept", n, err)
	}
}

// TestStoreDeployKeyBlockers: the blocker list names exactly the applications
// referencing the key, by id and name, and is empty otherwise.
func TestStoreDeployKeyBlockers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	dk, err := s.CreateDeployKey(ctx, domain.DeployKey{
		ID: ids.New(ids.PrefixDeployKey), Name: "ci-" + ids.Secret()[:6],
		PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:" + ids.Secret()[:12],
		PrivateKeyCT: []byte("ct"), PrivateKeyNonce: []byte("n"),
	})
	if err != nil {
		t.Fatalf("CreateDeployKey: %v", err)
	}
	if refs, err := s.ListApplicationsByDeployKey(ctx, dk.ID); err != nil || len(refs) != 0 {
		t.Fatalf("blockers before use = %v, %v; want none", refs, err)
	}
	app.Source.DeployKeyID = &dk.ID
	if _, err := s.UpdateApplicationConfig(ctx, app); err != nil {
		t.Fatalf("UpdateApplicationConfig: %v", err)
	}
	refs, err := s.ListApplicationsByDeployKey(ctx, dk.ID)
	if err != nil {
		t.Fatalf("ListApplicationsByDeployKey: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != app.ID || refs[0].Name != app.Name {
		t.Fatalf("blockers = %+v, want [{%s %s}]", refs, app.ID, app.Name)
	}
	if err := s.DeleteDeployKey(ctx, dk.ID); err == nil {
		t.Fatal("deleting a referenced deploy key succeeded; the FK should RESTRICT")
	}
}

// TestStorePendingEmailChange: the lookup returns the newest live change, and
// ignores rows that are spent, expired or another user's. "Newest wins" is what
// makes a second request supersede the first on screen.
func TestStorePendingEmailChange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "pending-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	other, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "other-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}
	now := time.Now()

	// Nothing pending is ErrNotFound, not an empty row.
	if _, err := s.PendingEmailChange(ctx, user.ID); err == nil {
		t.Fatal("PendingEmailChange with nothing pending returned no error")
	}

	// Already expired: never a candidate.
	if _, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), user.ID, "expired@example.test", []byte("h1"), now.Add(-time.Minute)); err != nil {
		t.Fatalf("CreateEmailChange(expired): %v", err)
	}
	// Spent: created, then consumed.
	spent, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), user.ID, "spent@example.test", []byte("h2"), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateEmailChange(spent): %v", err)
	}
	if _, err := s.ConsumeEmailChange(ctx, spent.ID); err != nil {
		t.Fatalf("ConsumeEmailChange: %v", err)
	}
	// Another user's live change must not leak into this user's answer.
	if _, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), other.ID, "theirs@example.test", []byte("h3"), now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailChange(other): %v", err)
	}

	if _, err := s.PendingEmailChange(ctx, user.ID); err == nil {
		t.Fatal("expired and spent rows were treated as pending")
	}

	// Two live changes: the newest is the answer.
	if _, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), user.ID, "first@example.test", []byte("h4"), now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailChange(first): %v", err)
	}
	time.Sleep(5 * time.Millisecond) // created_at is the tiebreak; make it decidable
	if _, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), user.ID, "second@example.test", []byte("h5"), now.Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailChange(second): %v", err)
	}
	got, err := s.PendingEmailChange(ctx, user.ID)
	if err != nil {
		t.Fatalf("PendingEmailChange: %v", err)
	}
	if got.NewEmail != "second@example.test" {
		t.Fatalf("pending change = %q, want the newest request", got.NewEmail)
	}
}

// TestStoreCancelPendingEmailChanges: "this wasn't me" spends every live link
// for the account and nobody else's, and says how many it spent.
func TestStoreCancelPendingEmailChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "cancel-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	other, err := s.CreateUser(ctx, ids.New(ids.PrefixUser), "keep-"+ids.Secret()[:8]+"@example.test", "hash", domain.RoleMember)
	if err != nil {
		t.Fatalf("CreateUser(other): %v", err)
	}
	now := time.Now()

	if n, err := s.CancelPendingEmailChanges(ctx, user.ID); err != nil || n != 0 {
		t.Fatalf("cancelling nothing = (%d, %v), want (0, nil)", n, err)
	}

	for _, addr := range []string{"one@example.test", "two@example.test"} {
		if _, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), user.ID, addr, []byte("h-"+addr), now.Add(time.Hour)); err != nil {
			t.Fatalf("CreateEmailChange(%s): %v", addr, err)
		}
	}
	theirs, err := s.CreateEmailChange(ctx, ids.New(ids.PrefixEmailChange), other.ID, "theirs@example.test", []byte("h-theirs"), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateEmailChange(other): %v", err)
	}

	n, err := s.CancelPendingEmailChanges(ctx, user.ID)
	if err != nil {
		t.Fatalf("CancelPendingEmailChanges: %v", err)
	}
	if n != 2 {
		t.Fatalf("cancelled = %d, want both outstanding links", n)
	}
	if _, err := s.PendingEmailChange(ctx, user.ID); err == nil {
		t.Fatal("a change survived the cancel")
	}
	// The other account is untouched, and its link still works.
	if _, err := s.ConsumeEmailChange(ctx, theirs.ID); err != nil {
		t.Fatalf("another user's change was cancelled too: %v", err)
	}
}

// TestStoreDNSZoneManagedRecordCounts: the zones list carries a live count of
// what the panel still wants to exist. Tombstoned records (desired='absent')
// are on their way out and would overstate what a disconnect orphans.
func TestStoreDNSZoneManagedRecordCounts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	busy, err := s.UpsertDNSZone(ctx, domain.DNSZone{
		ID: ids.New(ids.PrefixDNSZone), ProviderZoneID: "pz-busy",
		Name: "busy-" + ids.Secret()[:6] + ".example.test", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertDNSZone(busy): %v", err)
	}
	quiet, err := s.UpsertDNSZone(ctx, domain.DNSZone{
		ID: ids.New(ids.PrefixDNSZone), ProviderZoneID: "pz-quiet",
		Name: "quiet-" + ids.Secret()[:6] + ".example.test", Status: "pending",
	})
	if err != nil {
		t.Fatalf("UpsertDNSZone(quiet): %v", err)
	}

	// Upsert always means "want this record"; leaving is a separate operation
	// (TombstoneDNSRecordsForApplication), so the departing row belongs to a
	// second application that is then reaped.
	leaving, err := s.CreateApplicationWithEnv(ctx, domain.Application{
		ID:                 ids.New(ids.PrefixApplication),
		EnvironmentID:      app.EnvironmentID,
		Name:               "leaving",
		Source:             domain.AppSource{Kind: "github", Repo: "acme/leaving", Branch: "main"},
		Build:              domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime:            app.Runtime,
		Route:              domain.AppRoute{Domain: "leaving.example.com"},
		Health:             app.Health,
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    []byte("ct"),
		WebhookSecretNonce: []byte("nonce"),
	}, nil)
	if err != nil {
		t.Fatalf("CreateApplicationWithEnv(leaving): %v", err)
	}

	appID, leavingID := app.ID, leaving.ID
	for _, r := range []struct {
		name  string
		owner *string
	}{
		{"web", &appID},
		{"api", &appID},
		{"gone", &leavingID},
	} {
		if _, err := s.UpsertDNSRecord(ctx, domain.DNSRecord{
			ID: ids.New(ids.PrefixDNSRecord), ApplicationID: r.owner, ZoneID: busy.ID,
			Name: r.name + "." + busy.Name, Type: "A", Content: "203.0.113.7",
		}); err != nil {
			t.Fatalf("UpsertDNSRecord(%s): %v", r.name, err)
		}
	}
	if err := s.TombstoneDNSRecordsForApplication(ctx, leavingID); err != nil {
		t.Fatalf("TombstoneDNSRecordsForApplication: %v", err)
	}

	zones, err := s.ListDNSZones(ctx)
	if err != nil {
		t.Fatalf("ListDNSZones: %v", err)
	}
	counts := map[string]int64{}
	for _, z := range zones {
		counts[z.ID] = z.ManagedRecords
	}
	if counts[busy.ID] != 2 {
		t.Fatalf("busy zone managed records = %d, want 2 (the tombstoned row must not count)", counts[busy.ID])
	}
	if counts[quiet.ID] != 0 {
		t.Fatalf("quiet zone managed records = %d, want 0", counts[quiet.ID])
	}
}

// TestStoreListApplicationsWithManagedDNS: the disconnect preview names the
// domains that stop being verified — the ones still wanted, attached to an
// application that still exists.
func TestStoreListApplicationsWithManagedDNS(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, _, _, app := seedApp(t, s)

	zone, err := s.UpsertDNSZone(ctx, domain.DNSZone{
		ID: ids.New(ids.PrefixDNSZone), ProviderZoneID: "pz-1",
		Name: "preview-" + ids.Secret()[:6] + ".example.test", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertDNSZone: %v", err)
	}

	appID := app.ID
	live := "live." + zone.Name
	if _, err := s.UpsertDNSRecord(ctx, domain.DNSRecord{
		ID: ids.New(ids.PrefixDNSRecord), ApplicationID: &appID, ZoneID: zone.ID,
		Name: live, Type: "A", Content: "203.0.113.7", Desired: "present",
	}); err != nil {
		t.Fatalf("UpsertDNSRecord(live): %v", err)
	}
	// Tombstoned: already leaving, so not part of the blast radius. It belongs
	// to a second application so reaping it does not take the live one too.
	leaving, err := s.CreateApplicationWithEnv(ctx, domain.Application{
		ID:                 ids.New(ids.PrefixApplication),
		EnvironmentID:      app.EnvironmentID,
		Name:               "leaving",
		Source:             domain.AppSource{Kind: "github", Repo: "acme/leaving", Branch: "main"},
		Build:              domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
		Runtime:            app.Runtime,
		Route:              domain.AppRoute{Domain: "leaving.example.com"},
		Health:             app.Health,
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    []byte("ct"),
		WebhookSecretNonce: []byte("nonce"),
	}, nil)
	if err != nil {
		t.Fatalf("CreateApplicationWithEnv(leaving): %v", err)
	}
	leavingID := leaving.ID
	if _, err := s.UpsertDNSRecord(ctx, domain.DNSRecord{
		ID: ids.New(ids.PrefixDNSRecord), ApplicationID: &leavingID, ZoneID: zone.ID,
		Name: "leaving." + zone.Name, Type: "A", Content: "203.0.113.7",
	}); err != nil {
		t.Fatalf("UpsertDNSRecord(leaving): %v", err)
	}
	if err := s.TombstoneDNSRecordsForApplication(ctx, leavingID); err != nil {
		t.Fatalf("TombstoneDNSRecordsForApplication: %v", err)
	}

	domains, err := s.ListApplicationsWithManagedDNS(ctx)
	if err != nil {
		t.Fatalf("ListApplicationsWithManagedDNS: %v", err)
	}
	var found *domain.DNSVerifiedDomain
	for i := range domains {
		if domains[i].Domain == live {
			found = &domains[i]
		}
		if domains[i].Domain == "leaving."+zone.Name {
			t.Fatal("a tombstoned record was listed as verified")
		}
	}
	if found == nil {
		t.Fatalf("the live domain %q was not listed: %+v", live, domains)
	}
	if found.ApplicationID != app.ID || found.ApplicationName != app.Name || found.ZoneName != zone.Name {
		t.Fatalf("the listed domain does not name its application and zone: %+v", *found)
	}
}
