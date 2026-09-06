package identity_test

// Agent-side certificate renewal (agent-identity-and-tls.md §3): when the loop
// decides to renew, that the swap on disk is atomic and reversible-free, and
// that a failure never takes the running identity away.

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
)

// ─── fixtures ───────────────────────────────────────────────────────────────

// issue mints a real agent certificate from ca valid for ttl starting at now.
func issue(t *testing.T, ca *pki.CA, serverID string, now time.Time, ttl time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	keyPEM, csrPEM, err := pki.GenerateAgentKey(serverID)
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	certPEM, err = ca.SignAgentCSR(csrPEM, serverID, ttl, now)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	return certPEM, keyPEM
}

// enrolledDir writes a complete identity to a temp dir and returns it with the
// keeper over it.
func enrolledDir(t *testing.T, ca *pki.CA, now time.Time, ttl time.Duration) (string, *identity.Keeper) {
	t.Helper()
	dir := t.TempDir()
	certPEM, keyPEM := issue(t, ca, "srv_test", now, ttl)
	id := &identity.Identity{
		ServerID:  "srv_test",
		NATSURL:   "tls://plane.example.com:4222",
		PlaneAddr: "plane.example.com:8443",
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		CACertPEM: ca.CertPEM(),
	}
	if err := identity.Save(dir, id); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	k, err := identity.NewKeeper(dir, loaded)
	if err != nil {
		t.Fatalf("NewKeeper: %v", err)
	}
	return dir, k
}

// fakeClock is a controllable Clock: After returns a channel the test fires by
// calling Advance, so the renewal loop's schedule is observable without waiting.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	pending chan time.Time
	woken   chan time.Duration // one entry per After call, for the test to read
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now, woken: make(chan time.Duration, 64)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.pending = ch
	c.mu.Unlock()
	c.woken <- d
	return ch
}

// Advance moves the clock forward and releases the wait the loop is sitting on.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	ch := c.pending
	c.pending = nil
	c.mu.Unlock()
	if ch != nil {
		ch <- c.now
	}
}

// nextWait blocks until the loop asks to sleep and returns how long for.
func (c *fakeClock) nextWait(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-c.woken:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("renewal loop never slept; it should be waiting for its renewal date")
		return 0
	}
}

// fakeSigner is the plane: it signs whatever CSR it is handed, or fails.
type fakeSigner struct {
	ca  *pki.CA
	now func() time.Time
	ttl time.Duration

	mu    sync.Mutex
	calls int
	err   error
	// csrSubjects records each CSR's subject, so a test can prove the agent
	// asked for its own identity and nobody else's.
	csrSubjects []string
}

func (f *fakeSigner) RenewCertificate(_ context.Context, serverID string, csrPEM []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	err := f.err
	block, _ := pem.Decode(csrPEM)
	if block != nil {
		if csr, perr := x509.ParseCertificateRequest(block.Bytes); perr == nil {
			f.csrSubjects = append(f.csrSubjects, csr.Subject.CommonName)
		}
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.ca.SignAgentCSR(csrPEM, serverID, f.ttl, f.now())
}

func (f *fakeSigner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ─── tests ──────────────────────────────────────────────────────────────────

func TestKeeperExposesExpiryAndCertificate(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, 90*24*time.Hour)

	if want := now.Add(90 * 24 * time.Hour); !k.NotAfter().Equal(want) {
		t.Fatalf("NotAfter = %s, want %s", k.NotAfter(), want)
	}
	cert, err := k.Certificate(nil)
	if err != nil || cert == nil {
		t.Fatalf("Certificate = %v, %v", cert, err)
	}
	if k.ServerID() != "srv_test" {
		t.Fatalf("ServerID = %q", k.ServerID())
	}
}

// The schedule: renewal happens two thirds of the way through the certificate's
// life, not at expiry — leaving a third of the lifetime to retry in.
func TestRenewAtIsTwoThirdsOfTheLifetime(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, 90*24*time.Hour)
	r := identity.NewRenewer(k, &fakeSigner{ca: ca}, newFakeClock(now), discardLog())

	notBefore, notAfter, err := identity.Lifetime(k.CertificatePEM())
	if err != nil {
		t.Fatalf("Lifetime: %v", err)
	}
	want := notBefore.Add(time.Duration(float64(notAfter.Sub(notBefore)) * 2 / 3))
	if got := r.RenewAt(); !got.Equal(want) {
		t.Fatalf("RenewAt = %s, want %s (two thirds through %s)", got, want, notAfter.Sub(notBefore))
	}
	// Sanity: a 90-day certificate renews around day 60, ~30 days of slack.
	if slack := notAfter.Sub(r.RenewAt()); slack < 25*24*time.Hour {
		t.Fatalf("only %s of retry window before expiry", slack)
	}
}

// The loop waits until its renewal date, and does not renew before it.
func TestRenewalLoopWaitsUntilItsDate(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, 30*24*time.Hour)
	clock := newFakeClock(now)
	signer := &fakeSigner{ca: ca, now: clock.Now, ttl: 30 * 24 * time.Hour}
	r := identity.NewRenewer(k, signer, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()

	// It sleeps rather than renewing: the certificate is brand new.
	if d := clock.nextWait(t); d <= 0 {
		t.Fatalf("first wait = %s, want a positive delay", d)
	}
	if signer.count() != 0 {
		t.Fatalf("renewed %d times before the renewal date", signer.count())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on context cancel")
	}
}

// Past the renewal date the loop renews once, installs the new material, and
// goes back to sleep against the NEW expiry.
func TestRenewalLoopRenewsAndSwapsAtomically(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	ttl := 30 * 24 * time.Hour
	dir, k := enrolledDir(t, ca, now, ttl)
	oldCert := k.CertificatePEM()
	oldNotAfter := k.NotAfter()

	clock := newFakeClock(now)
	signer := &fakeSigner{ca: ca, now: clock.Now, ttl: ttl}
	r := identity.NewRenewer(k, signer, clock, discardLog())
	rotated := make(chan time.Time, 1)
	r.OnRotate(func(notAfter time.Time) { rotated <- notAfter })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	// Jump past the renewal date.
	clock.nextWait(t)
	clock.Advance(25 * 24 * time.Hour)

	select {
	case notAfter := <-rotated:
		if !notAfter.After(oldNotAfter) {
			t.Fatalf("renewed NotAfter %s is not later than %s", notAfter, oldNotAfter)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renewal never happened after the renewal date passed")
	}

	// In memory: new material, same identity.
	if string(k.CertificatePEM()) == string(oldCert) {
		t.Fatal("keeper still holds the old certificate")
	}
	if k.ServerID() != "srv_test" {
		t.Fatalf("identity changed across renewal: %q", k.ServerID())
	}

	// On disk: whatever identity.json points at is a complete, matching pair —
	// which is the atomicity property. Reload from scratch and prove it.
	reloaded, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("Load after rotation: %v", err)
	}
	if string(reloaded.CertPEM) != string(k.CertificatePEM()) {
		t.Fatal("on-disk certificate is not the one in use")
	}
	if _, err := identity.NewKeeper(dir, reloaded); err != nil {
		t.Fatalf("reloaded material is not a usable keypair: %v", err)
	}
	// The superseded slot is gone, not left behind with live key material.
	if _, err := os.Stat(filepath.Join(dir, "agent-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("the replaced key file is still present (err=%v)", err)
	}

	// The CSR carried the agent's own id, and the key pair is new: the private
	// key never crosses the wire, at renewal any more than at enrollment.
	signer.mu.Lock()
	subjects := append([]string(nil), signer.csrSubjects...)
	signer.mu.Unlock()
	if len(subjects) != 1 || subjects[0] != "srv_test" {
		t.Fatalf("CSR subjects = %v, want [srv_test]", subjects)
	}
	if string(reloaded.KeyPEM) == "" {
		t.Fatal("no key on disk after rotation")
	}
}

// A plane that refuses (revoked identity) or is unreachable must never cost the
// agent the certificate it is still using.
func TestFailedRenewalKeepsTheRunningIdentity(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	ttl := 30 * 24 * time.Hour
	dir, k := enrolledDir(t, ca, now, ttl)
	before := k.CertificatePEM()

	clock := newFakeClock(now)
	signer := &fakeSigner{ca: ca, now: clock.Now, ttl: ttl, err: errors.New("permission denied: unknown or revoked agent identity")}
	r := identity.NewRenewer(k, signer, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	clock.nextWait(t)                  // the initial wait for the renewal date
	clock.Advance(25 * 24 * time.Hour) // past it: renewal is attempted and fails

	// The next wait is the retry backoff, which is how we know it failed and
	// scheduled another attempt rather than giving up. Five days remain of the
	// 30-day certificate, so a quarter of that is well over the hour cap.
	if d := clock.nextWait(t); d != time.Hour {
		t.Fatalf("retry wait = %s, want the 1h cap", d)
	}
	if signer.count() == 0 {
		t.Fatal("no renewal was attempted")
	}
	if string(k.CertificatePEM()) != string(before) {
		t.Fatal("a failed renewal replaced the certificate in use")
	}
	reloaded, err := identity.Load(dir)
	if err != nil {
		t.Fatalf("Load after a failed renewal: %v", err)
	}
	if string(reloaded.CertPEM) != string(before) {
		t.Fatal("a failed renewal wrote to disk")
	}
}

// Long waits are capped so a clock jump (a resumed VM, an NTP step) cannot park
// the loop on a timer set from a time that turned out to be wrong.
func TestLongWaitsAreCapped(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, 365*24*time.Hour)
	clock := newFakeClock(now)
	r := identity.NewRenewer(k, &fakeSigner{ca: ca, now: clock.Now, ttl: time.Hour}, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	if d := clock.nextWait(t); d > 6*time.Hour {
		t.Fatalf("wait = %s, want it capped at 6h", d)
	}
}

// A "renewal" that does not move the expiry forward is refused: installing it
// would leave the loop asking again immediately, forever.
func TestRenewalRefusesACertificateThatIsNoNewer(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	ttl := 30 * 24 * time.Hour
	_, k := enrolledDir(t, ca, now, ttl)
	before := k.CertificatePEM()

	clock := newFakeClock(now)
	// The signer keeps issuing certificates that expire when the current one
	// does — a plane frozen in time.
	signer := &fakeSigner{ca: ca, now: func() time.Time { return now }, ttl: ttl}
	r := identity.NewRenewer(k, signer, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	clock.nextWait(t)
	clock.Advance(25 * 24 * time.Hour)

	if d := clock.nextWait(t); d != time.Hour {
		t.Fatalf("wait after a useless renewal = %s, want the 1h retry cap", d)
	}
	if string(k.CertificatePEM()) != string(before) {
		t.Fatal("installed a certificate that was no newer than the one in use")
	}
}

// The retry backoff is a fraction of the time actually left, not a constant.
// A constant is only right for one TTL: an hour's backoff on a certificate with
// twenty minutes left would let the identity expire while the loop slept. Found
// by running the loop live against a deliberately short CYPHERD_AGENT_CERT_TTL.
func TestRetryBackoffShrinksWithTheRemainingLifetime(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	// A 20-minute certificate whose renewal every attempt fails.
	_, k := enrolledDir(t, ca, now, 20*time.Minute)
	clock := newFakeClock(now)
	signer := &fakeSigner{ca: ca, now: clock.Now, ttl: 20 * time.Minute, err: errors.New("plane unreachable")}
	r := identity.NewRenewer(k, signer, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	clock.nextWait(t)               // waiting for the renewal date
	clock.Advance(15 * time.Minute) // past it; the attempt fails

	// About a quarter of the ~5 minutes left, and nowhere near the hour a
	// constant would have waited — which is longer than this certificate has to
	// live at all.
	d := clock.nextWait(t)
	if d >= time.Hour {
		t.Fatalf("retry wait = %s; a certificate with ~5m left would expire before the next attempt", d)
	}
	if d < 30*time.Second {
		t.Fatalf("retry wait = %s, below the hot-loop floor", d)
	}
	if signer.count() == 0 {
		t.Fatal("no renewal was attempted")
	}
}

// However short the remaining life, retries never become a hot loop.
func TestRetryBackoffHasAFloor(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, time.Minute)
	clock := newFakeClock(now)
	signer := &fakeSigner{ca: ca, now: clock.Now, ttl: time.Minute, err: errors.New("plane unreachable")}
	r := identity.NewRenewer(k, signer, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	// A one-minute certificate is already past its renewal point at issuance
	// (the CA's clock-skew backdate dominates it), so the loop attempts, fails,
	// and backs off — repeatedly, into and past expiry. The floor is what keeps
	// that from becoming a hot loop against a plane that will not answer.
	for i := 0; i < 3; i++ {
		if d := clock.nextWait(t); d < 30*time.Second {
			t.Fatalf("retry wait %d = %s, want at least the 30s floor", i, d)
		}
		clock.Advance(time.Minute)
	}
	if signer.count() < 3 {
		t.Fatalf("attempts = %d, want one per backoff", signer.count())
	}
}

// One call to the plane is bounded. Without a deadline a half-open connection —
// a plane that vanished without a FIN — would park the loop forever and the
// certificate would expire while a single request sat there.
func TestRenewalRequestIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	ca, err := pki.NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	_, k := enrolledDir(t, ca, now, 30*24*time.Hour)
	clock := newFakeClock(now)

	// A signer that never answers on its own: only the request deadline can end
	// the call.
	deadlines := make(chan bool, 1)
	blocking := blockingSigner{deadlines: deadlines}
	r := identity.NewRenewer(k, blocking, clock, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	clock.nextWait(t)
	clock.Advance(25 * 24 * time.Hour)

	select {
	case hadDeadline := <-deadlines:
		if !hadDeadline {
			t.Fatal("the renewal call was made with no deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renewal was never attempted")
	}
	// And the loop is still alive: it backed off rather than hanging.
	if d := clock.nextWait(t); d <= 0 {
		t.Fatalf("retry wait = %s, want a positive backoff", d)
	}
}

// blockingSigner reports whether it was called with a deadline, then fails
// immediately so the loop moves on.
type blockingSigner struct{ deadlines chan bool }

func (b blockingSigner) RenewCertificate(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	_, ok := ctx.Deadline()
	select {
	case b.deadlines <- ok:
	default:
	}
	return nil, errors.New("plane did not answer")
}
