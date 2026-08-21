package dns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

func zones(names ...string) []domain.DNSZone {
	out := make([]domain.DNSZone, 0, len(names))
	for i, n := range names {
		out = append(out, domain.DNSZone{ID: "dnz_" + n, ProviderZoneID: "cf_" + n, Name: n})
		_ = i
	}
	return out
}

// MatchZone is where ownership is actually decided, so its table is the densest
// in the package. The label-boundary rule is the whole security value: a raw
// strings.HasSuffix would make "notexample.com" match zone "example.com", which
// is an attacker registering a lookalike and inheriting someone's verification.
func TestMatchZone(t *testing.T) {
	zs := zones("example.com", "staging.example.com", "acme.co.uk")
	cases := map[string]struct {
		host     string
		wantZone string
		wantOK   bool
	}{
		"apex":                      {"example.com", "example.com", true},
		"subdomain":                 {"app.example.com", "example.com", true},
		"deep subdomain":            {"a.b.c.example.com", "example.com", true},
		"longest suffix wins":       {"api.staging.example.com", "staging.example.com", true},
		"the more specific apex":    {"staging.example.com", "staging.example.com", true},
		"second zone":               {"shop.acme.co.uk", "acme.co.uk", true},
		"unrelated":                 {"example.org", "", false},
		"empty":                     {"", "", false},
		"case is not significant":   {"APP.Example.COM", "example.com", true},
		"trailing dot is not a TLD": {"app.example.com.", "example.com", true},
		"a pasted port":             {"app.example.com:8080", "example.com", true},

		// The lookalike cases. Each of these matches under a naive suffix test
		// and must not match here.
		"lookalike prefix":      {"notexample.com", "", false},
		"lookalike no dot":      {"myexample.com", "", false},
		"suffix inside a label": {"example.com.evil.test", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := MatchZone(tc.host, zs)
			if ok != tc.wantOK || got.Name != tc.wantZone {
				t.Fatalf("MatchZone(%q) = %q,%v; want %q,%v", tc.host, got.Name, ok, tc.wantZone, tc.wantOK)
			}
		})
	}
}

// ─── A fake store, and a fake provider ──────────────────────────────────────

type fakeStore struct {
	kind          string
	ct, nonce     []byte
	configured    bool
	zones         []domain.DNSZone
	records       map[string]domain.DNSRecord
	orphansReaped int
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: map[string]domain.DNSRecord{}}
}

func (f *fakeStore) GetDNSProvider(context.Context) (string, []byte, []byte, time.Time, error) {
	if !f.configured {
		return "", nil, nil, time.Time{}, store.ErrNotFound
	}
	return f.kind, f.ct, f.nonce, time.Unix(0, 0), nil
}

func (f *fakeStore) SetDNSProvider(_ context.Context, kind string, ct, nonce []byte) error {
	f.kind, f.ct, f.nonce, f.configured = kind, ct, nonce, true
	return nil
}

func (f *fakeStore) DeleteDNSProvider(context.Context) error { f.configured = false; return nil }

func (f *fakeStore) ListDNSZones(context.Context) ([]domain.DNSZone, error) { return f.zones, nil }

func (f *fakeStore) UpsertDNSZone(_ context.Context, z domain.DNSZone) (domain.DNSZone, error) {
	for i, existing := range f.zones {
		if existing.Name == z.Name {
			f.zones[i] = z
			return z, nil
		}
	}
	f.zones = append(f.zones, z)
	return z, nil
}

func (f *fakeStore) DeleteDNSZonesNotIn(_ context.Context, names []string) error {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	var out []domain.DNSZone
	for _, z := range f.zones {
		if keep[z.Name] {
			out = append(out, z)
		}
	}
	f.zones = out
	return nil
}

func (f *fakeStore) GetDNSRecordByApplication(_ context.Context, appID string) (domain.DNSRecord, error) {
	for _, r := range f.records {
		if r.ApplicationID != nil && *r.ApplicationID == appID && r.Desired == domain.DNSDesiredPresent {
			return r, nil
		}
	}
	return domain.DNSRecord{}, store.ErrNotFound
}

func (f *fakeStore) UpsertDNSRecord(_ context.Context, r domain.DNSRecord) (domain.DNSRecord, error) {
	key := r.ZoneID + "|" + r.Name + "|" + r.Type
	if existing, ok := f.records[key]; ok {
		existing.ApplicationID, existing.Content = r.ApplicationID, r.Content
		existing.Desired, existing.LastError, existing.Attempt = domain.DNSDesiredPresent, "", 0
		f.records[key] = existing
		return existing, nil
	}
	r.Desired = domain.DNSDesiredPresent
	f.records[key] = r
	return r, nil
}

func (f *fakeStore) TombstoneDNSRecordsForApplication(_ context.Context, appID string) error {
	for k, r := range f.records {
		if r.ApplicationID != nil && *r.ApplicationID == appID && r.Desired == domain.DNSDesiredPresent {
			r.Desired = domain.DNSDesiredAbsent
			f.records[k] = r
		}
	}
	return nil
}

func (f *fakeStore) TombstoneOrphanedDNSRecords(context.Context) error {
	for k, r := range f.records {
		if r.ApplicationID == nil && r.Desired == domain.DNSDesiredPresent {
			r.Desired = domain.DNSDesiredAbsent
			f.records[k] = r
			f.orphansReaped++
		}
	}
	return nil
}

func (f *fakeStore) ListDueDNSRecords(_ context.Context, _ time.Time, _ int32) ([]domain.DNSRecordWithZone, error) {
	var out []domain.DNSRecordWithZone
	for _, r := range f.records {
		outOfSync := (r.Desired == domain.DNSDesiredPresent && r.ProviderRecordID == nil) ||
			(r.Desired == domain.DNSDesiredAbsent && r.ProviderRecordID != nil)
		if !outOfSync {
			continue
		}
		zoneID, zoneName := "", ""
		for _, z := range f.zones {
			if z.ID == r.ZoneID {
				zoneID, zoneName = z.ProviderZoneID, z.Name
			}
		}
		out = append(out, domain.DNSRecordWithZone{DNSRecord: r, ProviderZoneID: zoneID, ZoneName: zoneName})
	}
	return out, nil
}

func (f *fakeStore) MarkDNSRecordCreated(_ context.Context, id, providerRecordID string) (domain.DNSRecord, error) {
	for k, r := range f.records {
		if r.ID == id {
			pid := providerRecordID
			r.ProviderRecordID, r.LastError, r.Attempt = &pid, "", 0
			f.records[k] = r
			return r, nil
		}
	}
	return domain.DNSRecord{}, store.ErrNotFound
}

func (f *fakeStore) DeleteDNSRecordRow(_ context.Context, id string) error {
	for k, r := range f.records {
		if r.ID == id {
			delete(f.records, k)
		}
	}
	return nil
}

func (f *fakeStore) FailDNSRecord(_ context.Context, id, lastErr string, attempt int, next *time.Time) error {
	for k, r := range f.records {
		if r.ID == id {
			r.LastError, r.Attempt, r.NextAttemptAt = lastErr, attempt, next
			f.records[k] = r
		}
	}
	return nil
}

// plainBox is a no-op SecretBox: these tests are about DNS logic, not sealing.
type plainBox struct{}

func (plainBox) Seal(p []byte) ([]byte, []byte, error) { return p, []byte("n"), nil }
func (plainBox) Open(c, _ []byte) ([]byte, error)      { return c, nil }

type fakeClient struct {
	zones   []Zone
	records map[string]Record // key: zone|name|type
	listErr error
	creates int
	deletes int
	nextID  int
}

func newFakeClient(zoneNames ...string) *fakeClient {
	c := &fakeClient{records: map[string]Record{}}
	for _, n := range zoneNames {
		c.zones = append(c.zones, Zone{ProviderID: "cf_" + n, Name: n})
	}
	return c
}

func key(zone, name, typ string) string { return zone + "|" + name + "|" + typ }

func (c *fakeClient) ListZones(context.Context) ([]Zone, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.zones, nil
}

func (c *fakeClient) FindRecord(_ context.Context, zoneID, name, typ string) (Record, bool, error) {
	r, ok := c.records[key(zoneID, name, typ)]
	return r, ok, nil
}

func (c *fakeClient) CreateRecord(_ context.Context, zoneID string, r Record) (Record, error) {
	c.nextID++
	c.creates++
	r.ProviderID = "rec_" + strings.Repeat("x", c.nextID)
	c.records[key(zoneID, r.Name, r.Type)] = r
	return r, nil
}

func (c *fakeClient) UpdateRecord(_ context.Context, zoneID, recordID string, r Record) (Record, error) {
	r.ProviderID = recordID
	c.records[key(zoneID, r.Name, r.Type)] = r
	return r, nil
}

func (c *fakeClient) DeleteRecord(_ context.Context, zoneID, recordID string) error {
	c.deletes++
	for k, r := range c.records {
		if r.ProviderID == recordID && strings.HasPrefix(k, zoneID+"|") {
			delete(c.records, k)
		}
	}
	return nil
}

func newTestService(st *fakeStore, cli *fakeClient) *Service {
	s := New(st, plainBox{})
	s.newCli = func(string) Client { return cli }
	s.now = func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) }
	return s
}

func configure(t *testing.T, st *fakeStore, cli *fakeClient) *Service {
	t.Helper()
	s := newTestService(st, cli)
	if _, err := s.Set(context.Background(), Config{APIToken: "tok"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	return s
}

// ─── Verification ───────────────────────────────────────────────────────────

// With no provider, nothing is enforced. This is what keeps an install that
// never connects Cloudflare behaving exactly as it did before (spec §4.1), and
// it is acceptance case 1.
func TestVerifyIsUnenforcedWithoutAProvider(t *testing.T) {
	s := newTestService(newFakeStore(), newFakeClient())
	v, err := s.Verify(context.Background(), "anything.example.com")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Enforced || !v.Verified {
		t.Fatalf("Verify with no provider = %+v; want unenforced and verified", v)
	}
}

func TestVerifyGatesOnTheZoneList(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)

	in, err := s.Verify(context.Background(), "app.example.com")
	if err != nil || !in.Enforced || !in.Verified || in.Zone != "example.com" {
		t.Fatalf("in-zone domain = %+v, %v", in, err)
	}
	out, err := s.Verify(context.Background(), "app.elsewhere.com")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !out.Enforced || out.Verified {
		t.Fatalf("out-of-zone domain = %+v; want enforced and unverified", out)
	}
	// A refusal must say what you DO have, or it is a dead end.
	if len(out.AvailableZones) != 1 || out.AvailableZones[0] != "example.com" {
		t.Fatalf("available zones = %v; want the connected zone named", out.AvailableZones)
	}
}

// ─── Provider configuration ─────────────────────────────────────────────────

// A token that cannot list zones is refused BEFORE anything is written, with
// the provider's own words (acceptance case 2).
func TestSetRefusesABadTokenAndPersistsNothing(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient()
	cli.listErr = &AuthError{Msg: "Invalid API Token"}
	s := newTestService(st, cli)

	_, err := s.Set(context.Background(), Config{APIToken: "bad"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Set with a bad token = %v; want a ValidationError", err)
	}
	if !strings.Contains(ve.Msg, "Invalid API Token") {
		t.Errorf("message %q does not carry Cloudflare's own words", ve.Msg)
	}
	if st.configured {
		t.Fatal("a refused token was persisted")
	}
}

// The token is never readable back through Settings — only a hint naming the
// zones, which is the thing an operator actually wants confirmed.
func TestSettingsNeverCarryTheToken(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com", "acme.co.uk")
	s := configure(t, st, cli)

	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Configured || got.ZoneCount != 2 {
		t.Fatalf("Get = %+v; want configured with 2 zones", got)
	}
	if strings.Contains(got.Hint, "tok") {
		t.Fatalf("hint %q leaks the token", got.Hint)
	}
}

// Disconnecting must delete NOTHING at the provider: removing the token removes
// our ability to act, not our obligation to be careful (acceptance case 8).
func TestDeleteProviderDeletesNothingAtTheProvider(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	cli.records[key("cf_example.com", "app.example.com", "A")] = Record{ProviderID: "rec_1", Content: "1.2.3.4"}

	if err := s.Delete(context.Background()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cli.deletes != 0 {
		t.Fatalf("disconnecting deleted %d records at the provider; want 0", cli.deletes)
	}
	// And every domain is now unverified, which fails closed.
	v, _ := s.Verify(context.Background(), "app.example.com")
	if v.Enforced {
		t.Fatal("provider is gone but verification still reports enforced")
	}
}

// ─── Record lifecycle (spec §8 acceptance 4, 6, 7, 9, 10) ───────────────────

func appWithDomain(id, host string) domain.Application {
	a := domain.Application{ID: id}
	a.Route.Domain = host
	return a
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A verified domain on a server with a public address becomes a real record at
// the provider (acceptance 4).
func TestVerifiedDomainBecomesARecord(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())

	got, ok := cli.records[key("cf_example.com", "shop.example.com", "A")]
	if !ok {
		t.Fatalf("no record was created; provider holds %v", cli.records)
	}
	if got.Content != "203.0.113.7" || got.Proxied {
		t.Fatalf("record = %+v; want content 203.0.113.7 and DNS-only", got)
	}
}

// A domain outside every zone gets no record at all — the app still deploys,
// but nothing is written into someone else's DNS (acceptance 5).
func TestUnverifiedDomainGetsNoRecord(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.elsewhere.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())
	if len(cli.records) != 0 {
		t.Fatalf("an unverified domain produced records: %v", cli.records)
	}
}

// A server with no public address has nowhere to point, so no record is written
// — and the API reports the reason rather than failing silently (§3.4).
func TestNoPublicAddressWritesNoRecord(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.example.com"), ""); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())
	if len(cli.records) != 0 {
		t.Fatalf("a server with no public address produced records: %v", cli.records)
	}
}

// Changing the domain must not leave the old name behind (acceptance 6). This
// is the case the user asked about directly.
func TestChangingTheDomainReapsTheOldRecord(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "old.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())
	if _, ok := cli.records[key("cf_example.com", "old.example.com", "A")]; !ok {
		t.Fatal("the first record was never created")
	}

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "new.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication (rename): %v", err)
	}
	s.SweepDue(ctx, discard())

	if _, ok := cli.records[key("cf_example.com", "old.example.com", "A")]; ok {
		t.Error("the old record is still at the provider after the domain changed")
	}
	if _, ok := cli.records[key("cf_example.com", "new.example.com", "A")]; !ok {
		t.Error("the new record was not created")
	}
}

// Deleting the project deletes the record from the provider (acceptance 7).
//
// The application row is gone, so application_id is NULL — which is exactly the
// trace an application, environment OR project deletion leaves. The tombstone
// outlives the application precisely so this delete is guaranteed to happen.
func TestDeletingTheProjectDeletesTheRecord(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())
	if len(cli.records) != 1 {
		t.Fatalf("setup: provider holds %d records, want 1", len(cli.records))
	}

	// What ON DELETE SET NULL does when the project (→ environment → app) goes.
	for k, r := range st.records {
		r.ApplicationID = nil
		st.records[k] = r
	}

	s.SweepDue(ctx, discard())

	if len(cli.records) != 0 {
		t.Fatalf("the record survived the project's deletion: %v", cli.records)
	}
	if st.orphansReaped == 0 {
		t.Error("the orphan was never reaped")
	}
	if len(st.records) != 0 {
		t.Errorf("the tombstone row survived after the provider delete: %v", st.records)
	}
}

// We never overwrite a record we did not create (spec §4.4, acceptance 9).
func TestAnOperatorsOwnRecordIsAConflictNotAnOverwrite(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	// The operator already has this name pointed somewhere else, by hand.
	cli.records[key("cf_example.com", "shop.example.com", "A")] = Record{
		ProviderID: "theirs", Type: "A", Name: "shop.example.com", Content: "198.51.100.1",
	}

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())

	got := cli.records[key("cf_example.com", "shop.example.com", "A")]
	if got.Content != "198.51.100.1" || got.ProviderID != "theirs" {
		t.Fatalf("we overwrote the operator's own record: %+v", got)
	}
	var stored domain.DNSRecord
	for _, r := range st.records {
		stored = r
	}
	if !strings.Contains(stored.LastError, "already exists") {
		t.Fatalf("last_error = %q; want a named conflict the operator can act on", stored.LastError)
	}
}

// A record whose content already matches is ADOPTED, not duplicated — and a
// second sweep over converged state calls nothing (acceptance 9, 10).
func TestConvergenceIsIdempotent(t *testing.T) {
	st, cli := newFakeStore(), newFakeClient("example.com")
	s := configure(t, st, cli)
	ctx := context.Background()

	if err := s.SyncApplication(ctx, appWithDomain("app_1", "shop.example.com"), "203.0.113.7"); err != nil {
		t.Fatalf("SyncApplication: %v", err)
	}
	s.SweepDue(ctx, discard())
	after := cli.creates

	s.SweepDue(ctx, discard())
	s.SweepDue(ctx, discard())
	if cli.creates != after {
		t.Fatalf("re-sweeping created %d more records; want none", cli.creates-after)
	}
	if len(cli.records) != 1 {
		t.Fatalf("provider holds %d records; want exactly 1", len(cli.records))
	}
}
