package updates

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recordingAnnouncer counts announcements and can be made to fail.
type recordingAnnouncer struct {
	mu   sync.Mutex
	got  []Release
	fail error
}

func (r *recordingAnnouncer) AnnounceUpdate(_ context.Context, _ Info, latest Release) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, latest)
	return nil
}

func (r *recordingAnnouncer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// feedServer serves a GitHub-shaped releases/latest document and counts hits.
func feedServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "cypherd/") {
			t.Errorf("User-Agent = %q, want cypherd/<version>", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newChecker(t *testing.T, current, feed string, ann Announcer) *Checker {
	t.Helper()
	c, err := New(Options{
		Current:   Info{Version: current, Commit: "abc", GoVersion: "go1.25"},
		FeedURL:   feed,
		Enabled:   true,
		Announcer: ann,
		Log:       quietLog(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestNewerReleaseIsReportedAndAnnouncedOnce: a newer feed entry becomes
// Latest with its semver kind, and the announcer hears about it exactly once
// however many polls see it (control-plane-hardening.md §3).
func TestNewerReleaseIsReportedAndAnnouncedOnce(t *testing.T) {
	srv, _ := feedServer(t, `{"tag_name":"v0.5.0","html_url":"https://example.test/r/v0.5.0","published_at":"2026-09-01T10:00:00Z","draft":false,"prerelease":false}`)
	ann := &recordingAnnouncer{}
	c := newChecker(t, "v0.4.2", srv.URL, ann)

	for range 3 {
		if err := c.CheckNow(context.Background()); err != nil {
			t.Fatalf("CheckNow: %v", err)
		}
	}
	latest := c.Latest()
	if latest == nil {
		t.Fatal("Latest = nil, want the newer release")
	}
	if latest.Version != "v0.5.0" || latest.Kind != "minor" || latest.NotesURL != "https://example.test/r/v0.5.0" {
		t.Fatalf("Latest = %+v", *latest)
	}
	if !latest.PublishedAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("PublishedAt = %v", latest.PublishedAt)
	}
	if ann.count() != 1 {
		t.Fatalf("announced %d times, want once", ann.count())
	}
	// A copy, never the cached pointer.
	latest.Version = "tampered"
	if c.Latest().Version != "v0.5.0" {
		t.Fatal("Latest returned its internal pointer")
	}
}

// A failed announcement is retried on the next poll, then remembered.
func TestAnnouncementRetriesUntilItSucceeds(t *testing.T) {
	srv, _ := feedServer(t, `{"tag_name":"v1.0.0","html_url":"","draft":false,"prerelease":false}`)
	ann := &recordingAnnouncer{fail: errors.New("inbox down")}
	c := newChecker(t, "v0.9.9", srv.URL, ann)

	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("a failed announcement was reported as success")
	}
	ann.fail = nil
	for range 2 {
		if err := c.CheckNow(context.Background()); err != nil {
			t.Fatalf("CheckNow: %v", err)
		}
	}
	if ann.count() != 1 {
		t.Fatalf("announced %d times, want once after the retry", ann.count())
	}
}

// TestUpToDateOlderDraftAndPrereleaseYieldNothing: the four "nothing to say"
// shapes all leave Latest nil and announce nothing.
func TestUpToDateOlderDraftAndPrereleaseYieldNothing(t *testing.T) {
	for name, body := range map[string]string{
		"same":       `{"tag_name":"v0.4.2","draft":false,"prerelease":false}`,
		"older":      `{"tag_name":"v0.4.1","draft":false,"prerelease":false}`,
		"draft":      `{"tag_name":"v9.0.0","draft":true,"prerelease":false}`,
		"prerelease": `{"tag_name":"v9.0.0","draft":false,"prerelease":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := feedServer(t, body)
			ann := &recordingAnnouncer{}
			c := newChecker(t, "v0.4.2", srv.URL, ann)
			if err := c.CheckNow(context.Background()); err != nil {
				t.Fatalf("CheckNow: %v", err)
			}
			if c.Latest() != nil || ann.count() != 0 {
				t.Fatalf("Latest = %+v, announced %d; want nil, 0", c.Latest(), ann.count())
			}
		})
	}
}

// A newer release that later disappears from the feed (a yanked tag) clears
// the cached answer rather than advertising a release that no longer exists.
func TestLatestClearsWhenTheFeedNoLongerIsNewer(t *testing.T) {
	var body atomic.Value
	body.Store(`{"tag_name":"v2.0.0","draft":false,"prerelease":false}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer srv.Close()
	c := newChecker(t, "v1.0.0", srv.URL, nil)
	if err := c.CheckNow(context.Background()); err != nil || c.Latest() == nil || c.Latest().Kind != "major" {
		t.Fatalf("first poll: err %v, latest %+v", err, c.Latest())
	}
	body.Store(`{"tag_name":"v1.0.0","draft":false,"prerelease":false}`)
	if err := c.CheckNow(context.Background()); err != nil || c.Latest() != nil {
		t.Fatalf("second poll: err %v, latest %+v; want nil", err, c.Latest())
	}
}

// TestDisabledAndDevBuildsMakeNoRequest: opt-out and a non-release build both
// short-circuit before any network I/O.
func TestDisabledAndDevBuildsMakeNoRequest(t *testing.T) {
	srv, hits := feedServer(t, `{"tag_name":"v9.9.9"}`)

	off, err := New(Options{Current: Info{Version: "v1.0.0"}, FeedURL: srv.URL, Enabled: false, Log: quietLog()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := off.CheckNow(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled CheckNow = %v, want ErrDisabled", err)
	}
	dev := newChecker(t, "dev", srv.URL, nil)
	if err := dev.CheckNow(context.Background()); !errors.Is(err, ErrNotARelease) {
		t.Fatalf("dev CheckNow = %v, want ErrNotARelease", err)
	}
	// Run returns immediately for both, without a request.
	done := make(chan struct{})
	go func() {
		off.Run(context.Background())
		dev.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return for a disabled/dev checker")
	}
	if hits.Load() != 0 {
		t.Fatalf("feed was hit %d times, want 0", hits.Load())
	}
}

// TestRedirectIntoPrivateAddressIsRefused: the feed cannot bounce the plane
// into its own network (threat-model §5.13). httptest listens on loopback, so
// a redirect to it is exactly the refused case.
func TestRedirectIntoPrivateAddressIsRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"v9.9.9","draft":false,"prerelease":false}`)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := newChecker(t, "v1.0.0", redirector.URL, nil)
	err := c.CheckNow(context.Background())
	if !errors.Is(err, ErrPrivateRedirect) {
		t.Fatalf("err = %v, want ErrPrivateRedirect", err)
	}
	if c.Latest() != nil {
		t.Fatal("a refused redirect still produced a release")
	}
}

// fakeResolver answers hostnames from a table.
type fakeResolver struct{ table map[string][]string }

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := f.table[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
	}
	return out, nil
}

// TestRedirectCheckResolvesHostnames covers the table of what a redirect may
// and may not point at, including a public name whose second address is
// internal (DNS-rebinding shape) and an IPv4-mapped IPv6 loopback.
func TestRedirectCheckResolvesHostnames(t *testing.T) {
	c, err := New(Options{
		Current: Info{Version: "v1.0.0"}, Enabled: true, Log: quietLog(),
		Resolver: fakeResolver{table: map[string][]string{
			"public.example":  {"93.184.216.34"},
			"private.example": {"10.0.0.7"},
			"mixed.example":   {"93.184.216.34", "192.168.1.1"},
			"v6loop.example":  {"::ffff:127.0.0.1"},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tc := range []struct {
		url  string
		want error // nil = allowed
	}{
		{"https://public.example/latest", nil},
		{"https://private.example/latest", ErrPrivateRedirect},
		{"https://mixed.example/latest", ErrPrivateRedirect},
		{"https://v6loop.example/latest", ErrPrivateRedirect},
		{"https://127.0.0.1/latest", ErrPrivateRedirect},
		{"https://[::1]/latest", ErrPrivateRedirect},
		{"https://169.254.169.254/latest/meta-data/", ErrPrivateRedirect},
		{"https://0.0.0.0/", ErrPrivateRedirect},
		{"https://93.184.216.34/latest", nil},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		got := c.checkRedirect(context.Background(), req.URL)
		if tc.want == nil && got != nil {
			t.Errorf("%s: refused with %v, want allowed", tc.url, got)
		}
		if tc.want != nil && !errors.Is(got, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.url, got, tc.want)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "ftp://public.example/x", nil)
	if err := c.checkRedirect(context.Background(), req.URL); err == nil {
		t.Error("a non-http scheme was allowed")
	}
	req, _ = http.NewRequest(http.MethodGet, "https://unknown.example/x", nil)
	if err := c.checkRedirect(context.Background(), req.URL); err == nil {
		t.Error("an unresolvable host was allowed")
	}
}

// TestOversizedAndBadFeedsAreErrors: the body cap and the shape checks hold.
func TestOversizedAndBadFeedsAreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
		want   error // nil = any error
	}{
		"oversized":  {strings.Repeat("x", maxBodyBytes+1), 200, ErrBodyTooLarge},
		"not json":   {"<html>", 200, nil},
		"no tag":     {`{"html_url":"x"}`, 200, nil},
		"bad tag":    {`{"tag_name":"latest"}`, 200, nil},
		"server 500": {`{"tag_name":"v9.9.9"}`, 500, nil},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := newChecker(t, "v1.0.0", srv.URL, nil)
			err := c.CheckNow(context.Background())
			if err == nil {
				t.Fatal("CheckNow succeeded, want an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if c.Latest() != nil {
				t.Fatal("a failed poll produced a release")
			}
		})
	}
}

// TestRunPollsAndStops: the goroutine polls on its schedule and returns when
// its context ends (ENGINEERING rule 7).
func TestRunPollsAndStops(t *testing.T) {
	srv, hits := feedServer(t, `{"tag_name":"v1.0.1","draft":false,"prerelease":false}`)
	c, err := New(Options{
		Current: Info{Version: "v1.0.0"}, FeedURL: srv.URL, Enabled: true, Log: quietLog(),
		InitialDelay: time.Millisecond, Interval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("feed hit %d times, want repeated polls", hits.Load())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if l := c.Latest(); l == nil || l.Kind != "patch" {
		t.Fatalf("Latest = %+v, want a patch release", l)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

// TestNewRejectsABadFeedURL: configuration errors surface at boot.
func TestNewRejectsABadFeedURL(t *testing.T) {
	for _, feed := range []string{"ftp://x/y", "not a url", "/relative", "https://"} {
		if _, err := New(Options{Current: Info{Version: "v1"}, FeedURL: feed, Log: quietLog()}); err == nil {
			t.Errorf("New accepted feed url %q", feed)
		}
	}
	if _, err := New(Options{Current: Info{Version: "v1.0.0"}}); err == nil {
		t.Error("New accepted a nil logger")
	}
}

// TestVersionParsingAndKind is the semver table behind the badge.
func TestVersionParsingAndKind(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ok   bool
		want string
	}{
		{"v1.2.3", true, "1.2.3"}, {"1.2.3", true, "1.2.3"}, {"v1.2", true, "1.2.0"},
		{"v1.2.3-rc1", true, "1.2.3"}, {"v1.2.3+build.7", true, "1.2.3"},
		{"dev", false, ""}, {"main", false, ""}, {"v1", false, ""}, {"v1.2.3.4", false, ""},
		{"v01.2.3", false, ""}, {"v1.-2.3", false, ""}, {"", false, ""},
	} {
		got, ok := parseVersion(tc.in)
		if ok != tc.ok || (ok && got.String() != tc.want) {
			t.Errorf("parseVersion(%q) = %v, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct{ cur, newer, want string }{
		{"v1.0.3", "v1.0.4", "patch"}, {"v1.0.3", "v1.1.0", "minor"}, {"v1.9.9", "v2.0.0", "major"}, {"v0.4.0", "v0.5.7", "minor"},
	} {
		c, _ := parseVersion(tc.cur)
		n, _ := parseVersion(tc.newer)
		if got := kind(c, n); got != tc.want || compare(n, c) <= 0 {
			t.Errorf("kind(%s → %s) = %s, want %s", tc.cur, tc.newer, got, tc.want)
		}
	}
}

// TestRuntimeInfoParsesTheBuildDate: a stamped RFC3339 date is kept in UTC; an
// absent or malformed one leaves BuiltAt zero rather than failing boot.
func TestRuntimeInfoParsesTheBuildDate(t *testing.T) {
	info := RuntimeInfo("v1.2.3", "abc1234", "2026-09-05T10:00:00+02:00")
	if info.Version != "v1.2.3" || info.Commit != "abc1234" || info.GoVersion == "" {
		t.Fatalf("info = %+v", info)
	}
	if !info.BuiltAt.Equal(time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)) || info.BuiltAt.Location() != time.UTC {
		t.Fatalf("BuiltAt = %v, want 08:00 UTC", info.BuiltAt)
	}
	if !RuntimeInfo("dev", "dev", "dev").BuiltAt.IsZero() {
		t.Fatal("a malformed build date was not left zero")
	}
}
