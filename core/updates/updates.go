// Package updates knows what build of cypherd is running and whether a newer
// release exists (control-plane-hardening.md §3). It is the source behind
// GET /api/v1/panel/version and the once-per-version panel.update_available
// inbox item. The plane never updates itself (ADR-010): this package only
// tells the operator, on their terms — opt-out, bounded, and quiet.
//
// The check is the panel acting as an HTTP client (threat-model §5.13), so the
// client is hardened the way §5.11's webhook sender is: a timeout, a body cap,
// http/https only, at most three redirects, and a redirect is refused when its
// target resolves to a loopback, private, link-local or unspecified address —
// the feed must not be able to bounce the plane into its own network.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultFeedURL is GitHub's releases/latest endpoint for this project: one
// JSON document describing the newest non-draft, non-prerelease release.
const DefaultFeedURL = "https://api.github.com/repos/MaramHarsha/CypherPanel/releases/latest"

const (
	defaultInterval     = 6 * time.Hour
	defaultInitialDelay = 15 * time.Second
	defaultTimeout      = 10 * time.Second
	maxBodyBytes        = 256 << 10
	maxRedirects        = 3
)

// Sentinel errors, exported so a caller can tell a hardening refusal from a
// transport failure (ENGINEERING rule 3).
var (
	// ErrDisabled: the check is switched off (CYPHERD_UPDATE_CHECK=off).
	ErrDisabled = errors.New("updates: update check is disabled")
	// ErrNotARelease: the running version is not a release (a "dev" build),
	// so there is nothing to compare against.
	ErrNotARelease = errors.New("updates: running version is not a release")
	// ErrPrivateRedirect: the feed redirected towards an address inside the
	// plane's own network; refused (threat-model §5.13).
	ErrPrivateRedirect = errors.New("updates: redirect to a private address refused")
	// ErrBodyTooLarge: the feed answered with more than the cap allows.
	ErrBodyTooLarge = errors.New("updates: feed body exceeds the size cap")
)

// Info is the running build, stamped by -ldflags at release time and "dev"
// locally. BuiltAt is zero when the build carried no date.
type Info struct {
	Version   string
	Commit    string
	BuiltAt   time.Time
	GoVersion string
}

// Release is a release newer than the running build.
type Release struct {
	Version string
	// Kind is the semver class of the delta from the running build: patch |
	// minor | major — canvas 16a's badge.
	Kind        string
	NotesURL    string
	PublishedAt time.Time
}

// Announcer is told the first time a release newer than the running build is
// seen (consumer-defined; the inbox satisfies it through an adapter in main).
// It must be idempotent per version: the checker announces once per process,
// and the inbox's dedupe key makes a restart's re-announcement a no-op.
type Announcer interface {
	AnnounceUpdate(ctx context.Context, current Info, latest Release) error
}

// Resolver answers hostname lookups for the redirect check (consumer-defined;
// *net.Resolver satisfies it).
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Options configures a Checker.
type Options struct {
	// Current is the running build. Required.
	Current Info
	// FeedURL is the release feed; defaults to DefaultFeedURL.
	FeedURL string
	// Enabled switches the outbound check on. Off means Run returns at once
	// and Latest is always nil.
	Enabled bool
	// Interval between checks; defaults to 6 hours.
	Interval time.Duration
	// InitialDelay before the first check after Run starts; defaults to 15s
	// so boot is not delayed and a crash-looping plane does not hammer the
	// feed.
	InitialDelay time.Duration
	// Timeout bounds one request; defaults to 10s.
	Timeout time.Duration
	// Transport lets tests point the client somewhere; nil uses the default.
	Transport http.RoundTripper
	// Resolver backs the redirect check; nil uses net.DefaultResolver.
	Resolver Resolver
	// Announcer is told about a newer release once; nil announces nothing.
	Announcer Announcer
	// Log is required.
	Log *slog.Logger
	// Now is the clock (rule 9); nil uses time.Now.
	Now func() time.Time
}

// Checker polls the feed and caches the answer. Construct with New; the
// goroutine is Run, owned by the caller's context.
type Checker struct {
	current   Info
	feedURL   *url.URL
	enabled   bool
	interval  time.Duration
	initial   time.Duration
	client    *http.Client
	resolver  Resolver
	announcer Announcer
	log       *slog.Logger
	now       func() time.Time

	mu        sync.Mutex
	latest    *Release
	checkedAt time.Time
	announced map[string]bool
}

// New validates the options and builds a checker. A malformed feed URL is a
// configuration error and is refused here rather than at the first poll.
func New(o Options) (*Checker, error) {
	if o.Log == nil {
		return nil, errors.New("updates: Log is required")
	}
	if o.Current.Version == "" {
		return nil, errors.New("updates: Current.Version is required")
	}
	feed := o.FeedURL
	if feed == "" {
		feed = DefaultFeedURL
	}
	u, err := url.Parse(feed)
	if err != nil {
		return nil, fmt.Errorf("updates: parsing feed url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("updates: feed url %q must be an absolute http(s) url", feed)
	}
	c := &Checker{
		current:   o.Current,
		feedURL:   u,
		enabled:   o.Enabled,
		interval:  o.Interval,
		initial:   o.InitialDelay,
		resolver:  o.Resolver,
		announcer: o.Announcer,
		log:       o.Log,
		now:       o.Now,
		announced: map[string]bool{},
	}
	if c.interval <= 0 {
		c.interval = defaultInterval
	}
	if c.initial <= 0 {
		c.initial = defaultInitialDelay
	}
	if c.resolver == nil {
		c.resolver = net.DefaultResolver
	}
	if c.now == nil {
		c.now = time.Now
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	c.client = &http.Client{
		Timeout:   timeout,
		Transport: o.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("updates: more than %d redirects", maxRedirects)
			}
			return c.checkRedirect(req.Context(), req.URL)
		},
	}
	return c, nil
}

// Current is the running build.
func (c *Checker) Current() Info { return c.current }

// Latest is the newest release seen that is newer than the running build, or
// nil when there is nothing to say: up to date, disabled, not checked yet, or
// not a release build.
func (c *Checker) Latest() *Release {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest == nil {
		return nil
	}
	r := *c.latest
	return &r
}

// Run polls until ctx is done: first after the initial delay, then every
// interval. It returns immediately — and never touches the network — when
// the check is disabled or the running build is not a release. It owns its
// timer (ENGINEERING rule 7); a failed poll is logged and the next one
// proceeds.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		c.log.Info("update check disabled")
		return
	}
	if _, ok := parseVersion(c.current.Version); !ok {
		c.log.Info("update check skipped: running version is not a release", "version", c.current.Version)
		return
	}
	timer := time.NewTimer(c.initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.CheckNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
			c.log.Warn("update check failed", "feed_host", c.feedURL.Host, "error", err)
		}
		timer.Reset(c.interval)
	}
}

// CheckNow performs one poll and updates the cached answer. It returns
// ErrDisabled or ErrNotARelease without a request when there is nothing to
// check. A newer release is announced the first time this process sees it.
func (c *Checker) CheckNow(ctx context.Context) error {
	if !c.enabled {
		return ErrDisabled
	}
	cur, ok := parseVersion(c.current.Version)
	if !ok {
		return ErrNotARelease
	}
	entry, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.checkedAt = c.now()
	c.mu.Unlock()
	if entry.Draft || entry.Prerelease {
		c.log.Debug("update check: newest feed entry is a draft or pre-release; ignored", "tag", entry.TagName)
		return nil
	}
	seen, ok := parseVersion(entry.TagName)
	if !ok {
		return fmt.Errorf("updates: feed tag %q is not a version", entry.TagName)
	}
	if compare(seen, cur) <= 0 {
		c.mu.Lock()
		c.latest = nil
		c.mu.Unlock()
		return nil
	}
	rel := Release{
		Version:     "v" + seen.String(),
		Kind:        kind(cur, seen),
		NotesURL:    entry.HTMLURL,
		PublishedAt: entry.PublishedAt,
	}
	c.mu.Lock()
	c.latest = &rel
	already := c.announced[rel.Version]
	c.mu.Unlock()
	if already || c.announcer == nil {
		return nil
	}
	if err := c.announcer.AnnounceUpdate(ctx, c.current, rel); err != nil {
		// Not marked announced: the next poll tries again, and the inbox's
		// dedupe key keeps a late success from doubling up.
		return fmt.Errorf("updates: announcing %s: %w", rel.Version, err)
	}
	c.mu.Lock()
	c.announced[rel.Version] = true
	c.mu.Unlock()
	c.log.Info("newer cypherd release available", "current", c.current.Version, "latest", rel.Version, "kind", rel.Kind)
	return nil
}

// feedEntry is the subset of GitHub's release document the check reads.
type feedEntry struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

func (c *Checker) fetch(ctx context.Context) (feedEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL.String(), nil)
	if err != nil {
		return feedEntry{}, fmt.Errorf("updates: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cypherd/"+c.current.Version)
	resp, err := c.client.Do(req)
	if err != nil {
		return feedEntry{}, fmt.Errorf("updates: fetching feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status only: a feed's error page is not ours to log.
		return feedEntry{}, fmt.Errorf("updates: feed answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return feedEntry{}, fmt.Errorf("updates: reading feed: %w", err)
	}
	if len(body) > maxBodyBytes {
		return feedEntry{}, ErrBodyTooLarge
	}
	var entry feedEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		return feedEntry{}, fmt.Errorf("updates: decoding feed: %w", err)
	}
	if entry.TagName == "" {
		return feedEntry{}, errors.New("updates: feed entry has no tag_name")
	}
	return entry, nil
}

// checkRedirect refuses a redirect off http(s) or towards an address inside
// the plane's own network. The hostname is resolved here, at request time, so
// a name that points inward is caught as surely as a literal address.
func (c *Checker) checkRedirect(ctx context.Context, target *url.URL) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("updates: redirect to %s scheme refused", target.Scheme)
	}
	host := target.Hostname()
	if host == "" {
		return errors.New("updates: redirect without a host refused")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if isInternal(addr) {
			return ErrPrivateRedirect
		}
		return nil
	}
	ips, err := c.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("updates: resolving redirect host: %w", err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok || isInternal(addr) {
			return ErrPrivateRedirect
		}
	}
	return nil
}

// isInternal reports whether an address is one the plane must never be
// redirected to: loopback, private, link-local, unspecified, or multicast.
func isInternal(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() ||
		a.IsInterfaceLocalMulticast()
}

// ─── semver ─────────────────────────────────────────────────────────────────

// version is a parsed release tag. Pre-release and build suffixes are
// dropped: the feed already excludes pre-releases, and a running "v1.2.3-rc1"
// compares as 1.2.3 — close enough to say whether 1.2.4 is newer.
type version struct{ major, minor, patch int }

func (v version) String() string {
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

// parseVersion accepts "v1.2.3", "1.2.3", "v1.2", "v1.2.3-rc1" and
// "v1.2.3+build"; anything else — "dev", "main", a sha — is not a release.
func parseVersion(s string) (version, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return version{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return version{}, false
		}
		nums[i] = n
	}
	return version{nums[0], nums[1], nums[2]}, true
}

// compare orders two versions: negative when a < b, zero when equal.
func compare(a, b version) int {
	switch {
	case a.major != b.major:
		return a.major - b.major
	case a.minor != b.minor:
		return a.minor - b.minor
	default:
		return a.patch - b.patch
	}
}

// kind classifies the delta from cur to newer (newer > cur assumed).
func kind(cur, newer version) string {
	switch {
	case newer.major != cur.major:
		return "major"
	case newer.minor != cur.minor:
		return "minor"
	default:
		return "patch"
	}
}

// RuntimeInfo fills the build-independent half of Info: the Go toolchain the
// binary was compiled with. Exposed so main assembles Info in one place.
func RuntimeInfo(version, commit, buildDate string) Info {
	info := Info{Version: version, Commit: commit, GoVersion: runtime.Version()}
	if t, err := time.Parse(time.RFC3339, buildDate); err == nil {
		info.BuiltAt = t.UTC()
	}
	return info
}
