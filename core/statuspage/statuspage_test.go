package statuspage

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

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// fake is both the evaluator's Store and the renderer's Reader, so a test can
// drive an observation through to the rendered page in one place.
type fake struct {
	comps     []domain.StatusPageComponent
	intervals map[string][]domain.StatusInterval
	app       domain.Application
	server    domain.Server
	page      domain.StatusPage
	last      time.Time
	stamped   time.Time
	seq       int
}

func newFake() *fake {
	return &fake{
		intervals: map[string][]domain.StatusInterval{},
		app: domain.Application{
			ID: "app_1", Name: "acme-billing-internal", EnvironmentID: "env_1",
			Status: string(domain.StatusRunning), StatusDetail: "exit code 137 on port 5432",
			ObservedRevisionID: "rev_deadbeef",
			Route:              domain.AppRoute{Domain: "billing.internal.acme.example"},
			Runtime:            domain.AppRuntime{ServerID: "srv_1"},
		},
		server: domain.Server{ID: "srv_1", Name: "hetzner-fsn1-node3", Status: domain.StatusRunning},
		page:   domain.StatusPage{ID: "sp_1", Slug: "acme", Title: "Acme", Enabled: true, Domain: "status.acme.com"},
	}
}

func (f *fake) observedAt(t time.Time) { f.app.StatusObservedAt = &t }

func (f *fake) ListAllTrackedComponents(context.Context) ([]domain.StatusPageComponent, error) {
	return f.comps, nil
}
func (f *fake) ListStatusPageComponents(_ context.Context, _ string) ([]domain.StatusPageComponent, error) {
	return f.comps, nil
}
func (f *fake) GetOpenStatusInterval(_ context.Context, id string) (domain.StatusInterval, error) {
	for _, in := range f.intervals[id] {
		if in.EndedAt == nil {
			return in, nil
		}
	}
	return domain.StatusInterval{}, io.EOF
}
func (f *fake) OpenStatusInterval(_ context.Context, id, componentID, state string, at time.Time) (domain.StatusInterval, error) {
	f.seq++
	in := domain.StatusInterval{ID: id, ComponentID: componentID, State: state, StartedAt: at}
	f.intervals[componentID] = append(f.intervals[componentID], in)
	return in, nil
}
func (f *fake) CloseStatusInterval(_ context.Context, id string, at time.Time) error {
	for cid, list := range f.intervals {
		for i := range list {
			if list[i].ID == id && list[i].EndedAt == nil {
				end := at
				f.intervals[cid][i].EndedAt = &end
			}
		}
	}
	return nil
}
func (f *fake) ListStatusIntervalsSince(_ context.Context, id string, _ time.Time) ([]domain.StatusInterval, error) {
	return f.intervals[id], nil
}
func (f *fake) LastEvaluation(context.Context) (time.Time, error) { return f.last, nil }
func (f *fake) StampEvaluation(_ context.Context, at time.Time) error {
	f.stamped = at
	return nil
}
func (f *fake) DeleteStatusIntervalsBefore(context.Context, time.Time, int) error { return nil }
func (f *fake) GetApplication(_ context.Context, id string) (domain.Application, error) {
	if id != f.app.ID {
		return domain.Application{}, io.EOF
	}
	return f.app, nil
}
func (f *fake) GetComposeStack(context.Context, string) (domain.ComposeStack, error) {
	return domain.ComposeStack{}, io.EOF
}
func (f *fake) GetDatabase(context.Context, string) (domain.Database, error) {
	return domain.Database{}, io.EOF
}
func (f *fake) GetServer(_ context.Context, id string) (domain.Server, error) {
	if id != f.server.ID {
		return domain.Server{}, io.EOF
	}
	return f.server, nil
}
func (f *fake) GetStatusPageBySlug(_ context.Context, slug string) (domain.StatusPage, error) {
	if slug != f.page.Slug {
		return domain.StatusPage{}, io.EOF
	}
	return f.page, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func evaluator(f *fake, now *time.Time) *Evaluator {
	e := New(f, Config{Tick: 30 * time.Second, Dwell: time.Minute}, quiet())
	e.SetClock(func() time.Time { return *now })
	return e
}

// THE test for this feature (spec §13.2). A fixture carrying a distinctive
// server name, revision id, route domain, resource name and status_detail is
// rendered through BOTH public surfaces, and none of those strings may appear
// in either. Everything else here is arithmetic; this is the design.
func TestNothingPrivateReachesEitherPublicResponse(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", StatusPageID: "sp_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f.observedAt(now.Add(-time.Hour))
	evaluator(f, &now).Tick(context.Background())

	srv := NewServer(f, f, time.Second)
	srv.SetClock(func() time.Time { return now })

	secrets := []string{
		"hetzner-fsn1-node3",            // server identity, in any form
		"rev_deadbeef",                  // revision id — an index into the running code
		"billing.internal.acme.example", // a route domain is an attack-surface inventory
		"acme-billing-internal",         // the resource's own name; only the label is public
		"exit code 137 on port 5432",    // status_detail, the agent's own words
		"srv_1", "app_1", "env_1",       // ids of any kind
	}
	for _, path := range []string{"/status/acme", "/status/acme/data.json"} {
		mux := http.NewServeMux()
		srv.Routes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Errorf("%s leaks %q — see status-pages.md §2, the right-hand column", path, secret)
			}
		}
		if !strings.Contains(body, "Web app") {
			t.Errorf("%s does not carry the public label", path)
		}
	}
}

// The dwell earns its place or it does not exist: a flap shorter than it must
// change nothing, and a state held past it must open an interval.
func TestAFlapShorterThanTheDwellIsNotAnOutage(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f.observedAt(now.Add(-time.Hour))
	e := evaluator(f, &now)

	e.Tick(context.Background()) // opens operational
	f.app.Status = string(domain.StatusError)
	now = now.Add(30 * time.Second)
	e.Tick(context.Background()) // pending, not yet recorded
	f.app.Status = string(domain.StatusRunning)
	now = now.Add(30 * time.Second)
	e.Tick(context.Background()) // recovered inside the dwell

	if got := len(f.intervals["spc_1"]); got != 1 {
		t.Fatalf("a 30-second flap wrote %d intervals; the dwell exists precisely so it writes 1", got)
	}
	if f.intervals["spc_1"][0].State != domain.PublicOperational {
		t.Fatalf("state is %q, want operational", f.intervals["spc_1"][0].State)
	}
}

func TestAnOutageHeldPastTheDwellOpensAnIncident(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f.observedAt(now.Add(-time.Hour))
	e := evaluator(f, &now)

	e.Tick(context.Background())
	f.app.Status = string(domain.StatusError)
	for i := 0; i < 4; i++ {
		now = now.Add(30 * time.Second)
		e.Tick(context.Background())
	}
	list := f.intervals["spc_1"]
	if len(list) != 2 || list[1].State != domain.PublicDown {
		t.Fatalf("want a closed operational interval and an open down one, got %+v", list)
	}
	if list[0].EndedAt == nil || !list[0].EndedAt.Equal(list[1].StartedAt) {
		t.Error("the intervals are not contiguous; a component must be in exactly one state at every observed moment")
	}

	page, err := Build(context.Background(), f, f.page, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if page.Headline != "Some systems are down" {
		t.Errorf("headline is %q", page.Headline)
	}
	if len(page.Incidents) != 1 {
		t.Fatalf("want 1 incident, got %d — an incident IS a down interval", len(page.Incidents))
	}
}

// ui-principles §10, made public: a silent agent means we do not know, and the
// page says so rather than freezing a green light on a host that fell off the
// internet an hour ago.
func TestASilentServerMakesItsComponentsUnknown(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	f.observedAt(now.Add(-time.Hour))
	e := evaluator(f, &now)
	e.Tick(context.Background())

	// The application still says "running" — that is the point.
	f.server.Status = domain.StatusUnknown
	for i := 0; i < 4; i++ {
		now = now.Add(30 * time.Second)
		e.Tick(context.Background())
	}
	page, err := Build(context.Background(), f, f.page, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if page.Components[0].State != domain.PublicUnknown {
		t.Fatalf("component is %q while its server is silent; want unknown", page.Components[0].State)
	}
	if page.Headline != "Some systems are not reporting" {
		t.Errorf("headline is %q — unknown must outrank operational", page.Headline)
	}
}

// The most common dishonesty in this product category, refused in one `if`.
func TestAPageThatHasMeasuredNothingShowsADashNotAHundredPercent(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	page, err := Build(context.Background(), f, f.page, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if page.Components[0].Uptime != nil {
		t.Fatalf("uptime is %v with no coverage; want nil so the page prints —", *page.Components[0].Uptime)
	}
	body, _ := json.Marshal(page)
	if !strings.Contains(string(body), `"uptime":null`) {
		t.Error("the JSON does not carry a null uptime")
	}
}

// §6.3: time the plane could not see is grey, and out of the denominator —
// never counted as up.
func TestThePlanesOwnOutageIsRecordedAsUnobservedTime(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	start := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	now := start
	f.observedAt(start.Add(-time.Hour))
	e := evaluator(f, &now)
	e.Tick(context.Background())

	// The plane was down for two hours.
	f.last = start
	now = start.Add(2 * time.Hour)
	e.CloseGap(context.Background())

	list := f.intervals["spc_1"]
	if len(list) != 2 {
		t.Fatalf("want the original interval closed and a gap interval, got %d", len(list))
	}
	gap := list[1]
	if gap.State != domain.PublicUnknown || gap.EndedAt == nil {
		t.Fatalf("the gap is %+v; want a CLOSED unknown interval", gap)
	}
	if !gap.StartedAt.Equal(start) || !gap.EndedAt.Equal(now) {
		t.Errorf("the gap does not cover the outage: %v–%v", gap.StartedAt, gap.EndedAt)
	}
}

// A stopped component that has never run is the birth state, not an instant
// permanent incident nobody caused.
func TestAComponentThatHasNeverRunIsUnknownRatherThanDown(t *testing.T) {
	if got := domain.PublicStateFor(string(domain.StatusStopped), false, true); got != domain.PublicUnknown {
		t.Errorf("never-run stopped is %q, want unknown", got)
	}
	if got := domain.PublicStateFor(string(domain.StatusStopped), true, true); got != domain.PublicDown {
		t.Errorf("stopped after running is %q; the page describes what a visitor gets, so it is down", got)
	}
	if got := domain.PublicStateFor(string(domain.StatusDeploying), true, true); got != "" {
		t.Errorf("deploying is %q; it must leave the recorded state alone", got)
	}
}

// The public routes take no credential and must not ask for one — including
// when a visitor sends a bad one (spec §13.8).
func TestThePublicRoutesNeverAnswer401(t *testing.T) {
	f := newFake()
	f.comps = []domain.StatusPageComponent{{ID: "spc_1", ResourceKind: domain.StatusResourceApplication, ResourceID: "app_1", Label: "Web app"}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	srv := NewServer(f, f, time.Second)
	srv.SetClock(func() time.Time { return now })
	mux := http.NewServeMux()
	srv.Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/status/acme", nil)
	req.Header.Set("Authorization", "Bearer definitely-not-a-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an invalid credential got %d; the public page takes none", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP is %q; a page that forbids script cannot be made to run one", csp)
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Error("the page carries a script tag")
	}
}

// A disabled page and an unknown slug answer the same undifferentiated 404, so
// nobody can enumerate which projects have a page and which turned theirs off.
func TestADisabledPageIsIndistinguishableFromOneThatDoesNotExist(t *testing.T) {
	f := newFake()
	f.page.Enabled = false
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	srv := NewServer(f, f, time.Second)
	srv.SetClock(func() time.Time { return now })
	mux := http.NewServeMux()
	srv.Routes(mux)

	var bodies []string
	for _, path := range []string{"/status/acme", "/status/nothing-here"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Error("a disabled page answers differently from an unknown slug")
	}
}
