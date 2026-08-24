package rest

// HTTP tests for the notification inbox (notification-inbox.md §5, §6).
//
// The rule under test everywhere here is the one that makes this feature's
// authorization structural: every route serves the AUTHENTICATED caller's rows
// and there is no path shape by which one user addresses another's item. The
// fake records the user id each call arrived with, so a handler that stopped
// threading identity through would fail loudly rather than silently widen.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeInboxService implements InboxService with per-user state, so "did this
// route serve the caller's rows?" is answerable rather than assumed.
type fakeInboxService struct {
	items map[string][]domain.InboxItem // user id → rows, newest first
	prefs map[string][]string
	// callers records the user id every call was made with, in order.
	callers   []string
	lastOpts  inbox.ListOptions
	markedAll int
}

func newFakeInboxService() *fakeInboxService {
	read := time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
	return &fakeInboxService{
		items: map[string][]domain.InboxItem{
			"usr_test": {
				{
					ID: "inb_fail", UserID: "usr_test", ProjectID: "prj_test",
					Kind: domain.EventDeployFailed, Severity: domain.NotifyError,
					Title: "Deploy failed: web", Body: "healthcheck never passed",
					Link:      "/projects/prj_test/applications/app_web/deployments?dep=dep_1",
					LinkLabel: "View deployment", CountOK: 1, CountTotal: 1,
					DedupeKey: "deploy.failed:dep_1",
				},
				{
					ID: "inb_digest", UserID: "usr_test", ProjectID: "prj_test",
					Kind: domain.EventBackupSucceeded, Severity: domain.NotifyInfo,
					Digest: true, Title: "Backups", CountOK: 3, CountTotal: 3,
					Sources: []string{"br_1", "br_2", "br_3"}, ReadAt: &read,
					DedupeKey: "digest:backup.succeeded:prj_test:2026-08-21",
				},
			},
			"usr_other": {
				{ID: "inb_theirs", UserID: "usr_other", ProjectID: "prj_other", Kind: domain.EventDeployFailed,
					Severity: domain.NotifyError, Title: "Deploy failed: their-app"},
			},
		},
		prefs: map[string][]string{},
	}
}

func (f *fakeInboxService) List(_ context.Context, userID string, opts inbox.ListOptions) (inbox.Page, error) {
	f.callers = append(f.callers, userID)
	f.lastOpts = opts
	out := []domain.InboxItem{}
	for _, it := range f.items[userID] {
		if opts.UnreadOnly && it.ReadAt != nil {
			continue
		}
		out = append(out, it)
	}
	return inbox.Page{Items: out, NextBefore: "inb_older"}, nil
}

func (f *fakeInboxService) UnreadCount(_ context.Context, userID string) (int64, error) {
	f.callers = append(f.callers, userID)
	var n int64
	for _, it := range f.items[userID] {
		if it.ReadAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *fakeInboxService) MarkRead(_ context.Context, userID, itemID string) error {
	f.callers = append(f.callers, userID)
	for i, it := range f.items[userID] {
		if it.ID == itemID {
			if it.ReadAt == nil {
				at := time.Now()
				f.items[userID][i].ReadAt = &at
			}
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeInboxService) MarkAllRead(_ context.Context, userID string) (int64, error) {
	f.callers = append(f.callers, userID)
	f.markedAll++
	var n int64
	for i, it := range f.items[userID] {
		if it.ReadAt == nil {
			at := time.Now()
			f.items[userID][i].ReadAt = &at
			n++
		}
	}
	return n, nil
}

func (f *fakeInboxService) Preferences(_ context.Context, userID string) (domain.InboxPreferences, error) {
	f.callers = append(f.callers, userID)
	m := f.prefs[userID]
	if m == nil {
		m = []string{}
	}
	return domain.InboxPreferences{UserID: userID, MutedKinds: m}, nil
}

func (f *fakeInboxService) SetPreferences(_ context.Context, userID string, muted []string) (domain.InboxPreferences, error) {
	f.callers = append(f.callers, userID)
	for _, k := range muted {
		if !domain.ValidEventType(k) {
			return domain.InboxPreferences{}, &inbox.ValidationError{Msg: "unknown notification kind: " + k}
		}
	}
	if muted == nil {
		muted = []string{}
	}
	f.prefs[userID] = muted
	return domain.InboxPreferences{UserID: userID, MutedKinds: muted}, nil
}

// newInboxServer wires an API whose only interesting dependency is the inbox.
func newInboxServer(t *testing.T) (*httptest.Server, *fakeInboxService) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	authStore := &fakeAuthStore{
		user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
		sessions: map[string]domain.User{},
		tokens:   map[string]domain.APIToken{},
		byHash:   map[string]string{},
	}
	svc := newFakeInboxService()
	api := New(Deps{
		Auth:   auth.NewAuthenticator(authStore, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Inbox:  svc,
		Teams:  newFakeTeams(),
		Pinger: okPinger{},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	return ts, svc
}

// inboxRoutes is every route this feature adds. Deletion of state is ordered so
// a table walked in order does not invalidate a later row.
var inboxRoutes = []struct {
	name   string
	method string
	path   string
	body   string
}{
	{"list", "GET", "/api/v1/inbox", ""},
	{"unread count", "GET", "/api/v1/inbox/unread-count", ""},
	{"get preferences", "GET", "/api/v1/inbox/preferences", ""},
	{"set preferences", "PUT", "/api/v1/inbox/preferences", `{"muted_kinds":["deploy.succeeded"]}`},
	{"mark one read", "POST", "/api/v1/inbox/inb_fail/read", ""},
	{"mark all read", "POST", "/api/v1/inbox/read-all", ""},
}

func TestInboxRoutesRequireAuthentication(t *testing.T) {
	ts, _ := newInboxServer(t)
	for _, r := range inboxRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, "", r.body)
		if status != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated = %d, want 401 (body %s)", r.name, status, body)
		}
	}
}

// The inbox is always the caller's. There is no owner segment in any path, and
// every handler threads the authenticated user's id into the service — which is
// what makes the tenancy guarantee syntactic rather than enforced (spec §5).
func TestEveryInboxRouteServesTheAuthenticatedCaller(t *testing.T) {
	ts, svc := newInboxServer(t)
	token := login(t, ts)
	for _, r := range inboxRoutes {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, token, r.body)
		if status >= 400 {
			t.Fatalf("%s = %d (body %s)", r.name, status, body)
		}
	}
	if len(svc.callers) != len(inboxRoutes) {
		t.Fatalf("service calls = %d, want one per route (%d)", len(svc.callers), len(inboxRoutes))
	}
	for i, got := range svc.callers {
		if got != "usr_test" {
			t.Fatalf("%s served user %q, want the authenticated caller", inboxRoutes[i].name, got)
		}
	}
}

// A digest's title is composed server-side, and the counters behind it stay out
// of the contract: a client rendering them into English would be a second home
// for copy (spec §6).
func TestInboxDTOComposesTitlesAndHidesCounters(t *testing.T) {
	ts, _ := newInboxServer(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox", token, "")
	if status != http.StatusOK {
		t.Fatalf("list = %d (%s)", status, body)
	}
	var page struct {
		Items []struct {
			ID        string  `json:"id"`
			Kind      string  `json:"kind"`
			Severity  string  `json:"severity"`
			Digest    bool    `json:"digest"`
			Title     string  `json:"title"`
			Link      string  `json:"link"`
			LinkLabel string  `json:"link_label"`
			ReadAt    *string `json:"read_at"`
		} `json:"items"`
		NextBefore string `json:"next_before"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	if page.NextBefore != "inb_older" {
		t.Fatalf("next_before = %q", page.NextBefore)
	}
	fail, digest := page.Items[0], page.Items[1]
	if fail.Title != "Deploy failed: web" || fail.Severity != "error" || fail.Digest {
		t.Fatalf("immediate item = %+v", fail)
	}
	if fail.Link == "" || fail.LinkLabel != "View deployment" {
		t.Fatalf("immediate item lost its deep link: %+v", fail)
	}
	if fail.ReadAt != nil {
		t.Fatalf("unread item carries read_at %v", *fail.ReadAt)
	}
	if !digest.Digest || digest.Title != "Backups: 3/3 succeeded" {
		t.Fatalf("digest title = %q, want the composed line", digest.Title)
	}
	if digest.Link != "" {
		t.Fatalf("digest carries a link %q; a rollup has no single thing to open", digest.Link)
	}
	// Counters and the dedupe key are internal; leaking them would invite a
	// second home for the copy above.
	for _, forbidden := range []string{"count_ok", "count_total", "sources", "dedupe_key", "user_id"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("response exposes %q: %s", forbidden, body)
		}
	}
}

func TestInboxUnreadCountAndFilter(t *testing.T) {
	ts, svc := newInboxServer(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox/unread-count", token, "")
	if status != http.StatusOK {
		t.Fatalf("unread-count = %d (%s)", status, body)
	}
	var count struct {
		Unread int `json:"unread"`
	}
	if err := json.Unmarshal(body, &count); err != nil || count.Unread != 1 {
		t.Fatalf("unread = %s (%v), want 1", body, err)
	}

	// ?unread=true is a filter, not a different collection: the same route,
	// narrowed, which is what lets the UI show filtered-to-zero distinctly.
	status, _, body = doJSON(t, "GET", ts.URL+"/api/v1/inbox?unread=true&limit=5&before=inb_x", token, "")
	if status != http.StatusOK {
		t.Fatalf("filtered list = %d (%s)", status, body)
	}
	if !svc.lastOpts.UnreadOnly || svc.lastOpts.Limit != 5 || svc.lastOpts.Before != "inb_x" {
		t.Fatalf("query parameters not threaded through: %+v", svc.lastOpts)
	}
	var page struct {
		Items []struct{} `json:"items"`
	}
	_ = json.Unmarshal(body, &page)
	if len(page.Items) != 1 {
		t.Fatalf("unread filter returned %d items, want 1", len(page.Items))
	}
}

func TestInboxListRejectsABadLimit(t *testing.T) {
	ts, _ := newInboxServer(t)
	token := login(t, ts)
	// "2147483648" is 2^31: it parses fine as a platform int on 64-bit and then
	// wraps to a negative when narrowed to the int32 the query's LIMIT takes.
	// Parsing at an explicit bit size is what turns that into an honest 400
	// (CodeQL go/incorrect-integer-conversion).
	for _, bad := range []string{"0", "-3", "many", "2147483648", "9223372036854775808"} {
		status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox?limit="+bad, token, "")
		if status != http.StatusBadRequest {
			t.Errorf("limit=%s = %d, want 400 (%s)", bad, status, body)
		}
	}
}

// Marking is idempotent (204 both times) and an item that is not the caller's
// is 404 — the same answer a nonexistent one gets, so no inbox is probeable.
func TestMarkInboxItemReadIsIdempotentAndScoped(t *testing.T) {
	ts, _ := newInboxServer(t)
	token := login(t, ts)

	for i := 0; i < 2; i++ {
		status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/inbox/inb_fail/read", token, "")
		if status != http.StatusNoContent {
			t.Fatalf("mark read #%d = %d (%s), want 204", i, status, body)
		}
	}
	for _, id := range []string{"inb_theirs", "inb_nope"} {
		status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/inbox/"+id+"/read", token, "")
		if status != http.StatusNotFound {
			t.Fatalf("marking %s = %d (%s), want 404", id, status, body)
		}
	}
}

func TestMarkAllInboxReadReportsWhatItChanged(t *testing.T) {
	ts, svc := newInboxServer(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/inbox/read-all", token, "")
	if status != http.StatusOK {
		t.Fatalf("read-all = %d (%s)", status, body)
	}
	var got struct {
		Marked int `json:"marked"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Marked != 1 {
		t.Fatalf("marked = %s (%v), want 1", body, err)
	}
	// The items stay listed — reading is not deleting.
	if len(svc.items["usr_test"]) != 2 {
		t.Fatalf("items after read-all = %d, want 2", len(svc.items["usr_test"]))
	}
	// And the other user's inbox was never touched.
	if svc.items["usr_other"][0].ReadAt != nil {
		t.Fatal("read-all reached another user's inbox")
	}
}

// available_kinds is served rather than hardcoded, so a new taxonomy entry
// needs no front-end change; a kind outside it is a 400, not a silently
// dropped setting.
func TestInboxPreferencesRoundtrip(t *testing.T) {
	ts, _ := newInboxServer(t)
	token := login(t, ts)

	status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox/preferences", token, "")
	if status != http.StatusOK {
		t.Fatalf("get preferences = %d (%s)", status, body)
	}
	var prefs struct {
		Muted     []string `json:"muted_kinds"`
		Available []string `json:"available_kinds"`
	}
	if err := json.Unmarshal(body, &prefs); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(prefs.Muted) != 0 {
		t.Fatalf("a fresh account starts muted: %v — mutes must default to empty", prefs.Muted)
	}
	if len(prefs.Available) != len(domain.EventTypes()) {
		t.Fatalf("available_kinds = %v, want the taxonomy %v", prefs.Available, domain.EventTypes())
	}

	status, _, body = doJSON(t, "PUT", ts.URL+"/api/v1/inbox/preferences", token,
		`{"muted_kinds":["deploy.succeeded"]}`)
	if status != http.StatusOK {
		t.Fatalf("put preferences = %d (%s)", status, body)
	}
	if err := json.Unmarshal(body, &prefs); err != nil || len(prefs.Muted) != 1 {
		t.Fatalf("muted after PUT = %s (%v)", body, err)
	}

	status, _, body = doJSON(t, "PUT", ts.URL+"/api/v1/inbox/preferences", token,
		`{"muted_kinds":["deploy.exploded"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d (%s), want 400", status, body)
	}
}

// An API token acts as its owner, narrowed by its abilities: reads need `read`,
// the two mark verbs and the preference write need `write`. These routes are
// deliberately NOT session-only — clearing your own inbox is not credential
// management (spec §5).
func TestInboxRespectsTokenAbilities(t *testing.T) {
	ts, _ := newInboxServer(t)
	session := login(t, ts)
	readOnly := createToken(t, ts, session, "ci-read", `["read"]`)

	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox", readOnly, ""); status != http.StatusOK {
		t.Fatalf("read token listing = %d (%s), want 200", status, body)
	}
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox/unread-count", readOnly, ""); status != http.StatusOK {
		t.Fatalf("read token counting = %d (%s), want 200", status, body)
	}
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/v1/inbox/inb_fail/read", ""},
		{"POST", "/api/v1/inbox/read-all", ""},
		{"PUT", "/api/v1/inbox/preferences", `{"muted_kinds":[]}`},
	} {
		status, _, body := doJSON(t, r.method, ts.URL+r.path, readOnly, r.body)
		if status != http.StatusForbidden {
			t.Errorf("%s %s with a read-only token = %d (%s), want 403", r.method, r.path, status, body)
		}
	}

	writeToken := createToken(t, ts, session, "ci-write", `["read","write"]`)
	if status, _, body := doJSON(t, "POST", ts.URL+"/api/v1/inbox/read-all", writeToken, ""); status != http.StatusOK {
		t.Fatalf("write token read-all = %d (%s), want 200", status, body)
	}
}

// A panel built without the inbox wired still answers its routes rather than
// 500ing: an empty feed and a zero count are honest, and the one route that
// cannot pretend says so.
func TestInboxRoutesDegradeWhenUnwired(t *testing.T) {
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	api := New(Deps{
		Auth: auth.NewAuthenticator(&fakeAuthStore{
			user:     domain.User{ID: "usr_test", Email: testEmail, PasswordHash: hash, Role: domain.RoleMember},
			sessions: map[string]domain.User{},
		}, fakeBox{}, auth.NewLimiter(100, time.Minute), time.Hour),
		Teams:  newFakeTeams(),
		Pinger: okPinger{},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(api.Handler())
	t.Cleanup(ts.Close)
	token := login(t, ts)

	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox", token, ""); status != http.StatusOK {
		t.Fatalf("unwired list = %d (%s)", status, body)
	}
	if status, _, body := doJSON(t, "GET", ts.URL+"/api/v1/inbox/unread-count", token, ""); status != http.StatusOK {
		t.Fatalf("unwired count = %d (%s)", status, body)
	}
	if status, _, _ := doJSON(t, "POST", ts.URL+"/api/v1/inbox/inb_x/read", token, ""); status != http.StatusNotFound {
		t.Fatalf("unwired mark = %d, want 404", status)
	}
	if status, _, _ := doJSON(t, "PUT", ts.URL+"/api/v1/inbox/preferences", token, `{"muted_kinds":[]}`); status != http.StatusNotImplemented {
		t.Fatalf("unwired preference write = %d, want 501", status)
	}
}
