package identity

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MaramHarsha/cypherpanel/pkg/pki"
)

// renewFraction is how far through a certificate's life renewal is attempted:
// two thirds, so a 90-day certificate renews on day 60 and there are 30 days of
// retries before anything goes dark (agent-identity-and-tls.md §3). The window
// is a third of the lifetime by design rather than a fixed number of days, so
// shortening CYPHERD_AGENT_CERT_TTL shortens the retry window with it instead
// of silently making renewal impossible.
const renewFraction = 2.0 / 3.0

const (
	// maxRetryInterval bounds the wait after a failed renewal. A plane that is
	// down, mid-upgrade or unreachable is the expected failure and there is a
	// third of the certificate's life to get through it, so retries are
	// unhurried: one an hour is ~720 attempts across a 90-day certificate's
	// window, which is plenty and costs the plane nothing.
	maxRetryInterval = time.Hour
	// minRetryInterval keeps a failing renewal from becoming a hot loop.
	minRetryInterval = 30 * time.Second
	// maxSleep caps a single wait. The renewal date can be two months out and
	// a host's clock can jump (a VM resumed from a snapshot, an NTP step), so
	// the loop re-evaluates at least this often rather than trusting one long
	// timer set from a time that turned out to be wrong.
	maxSleep = 6 * time.Hour
	// requestTimeout bounds one call to the plane. Without it a half-open
	// connection to a plane that went away without a FIN would park the loop
	// indefinitely, and the certificate would expire while a single request sat
	// there — the exact failure this whole loop exists to prevent. Real time,
	// not the injected clock: this is a network deadline, not a schedule.
	requestTimeout = 30 * time.Second
)

// Signer obtains a new certificate for an already-enrolled agent
// (consumer-defined, rule 6; agent/conn satisfies it over the mTLS gRPC
// channel). It is handed the CSR only — the private key stays in this process
// and on this host, renewal included.
type Signer interface {
	RenewCertificate(ctx context.Context, serverID string, csrPEM []byte) (certPEM []byte, err error)
}

// Clock is the renewal loop's view of time (rule 9: injected, so the schedule
// is testable without waiting sixty days). The production implementation is
// SystemClock.
type Clock interface {
	Now() time.Time
	// After behaves like time.After. Implementations must deliver even when d
	// is zero or negative.
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the real clock.
type SystemClock struct{}

// Now reports the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// After delivers on a channel after d.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Renewer keeps an agent's certificate fresh. It owns one goroutine with a
// defined lifecycle (rule 7): Run returns when its context is canceled, and
// every failure is logged with the server id rather than swallowed — there is
// no path where the agent stops renewing without saying so.
type Renewer struct {
	keeper *Keeper
	signer Signer
	clock  Clock
	log    *slog.Logger

	// onRotate, when set, is called after a successful rotation. The bus and
	// relay pick the new certificate up on their next handshake by themselves
	// (see Keeper), so this is for observation — a heartbeat annotation, a
	// test — never for reconnection.
	onRotate func(notAfter time.Time)
}

// NewRenewer wires the renewal loop.
func NewRenewer(k *Keeper, s Signer, clock Clock, log *slog.Logger) *Renewer {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Renewer{keeper: k, signer: s, clock: clock, log: log}
}

// OnRotate registers an observer called after each successful rotation.
func (r *Renewer) OnRotate(f func(notAfter time.Time)) { r.onRotate = f }

// RenewAt is when the certificate currently held should be renewed: two thirds
// of the way through its validity window. Exported so the agent can log it on
// boot — "this identity renews on <date>" is the answer to the only question an
// operator has about certificate lifetimes.
func (r *Renewer) RenewAt() time.Time {
	notBefore, notAfter, err := Lifetime(r.keeper.CertificatePEM())
	if err != nil {
		// Unparseable material cannot happen past NewKeeper, which parses it.
		// Renew immediately rather than never.
		return r.clock.Now()
	}
	return renewAt(notBefore, notAfter)
}

// retryIn is how long to wait before the next attempt after a failure. It is a
// fraction of the time actually left rather than a constant, because a constant
// is only right for one TTL: an hour's backoff on a certificate with twenty
// minutes left would let the identity expire while the loop slept, and a
// 30-second backoff on one with thirty days left would hammer the plane for a
// month. A quarter of the remaining window gives at least three more attempts
// whatever the lifetime, bounded at both ends.
//
// It is also what makes the short-lifetime edge case self-heal: when the plane
// re-issues a certificate expiring no later than the current one (possible only
// when the whole lifetime is minutes, so that the CA's clock-skew backdate
// dominates it), the next attempt comes soon enough that wall-clock has moved
// and the re-issue does advance.
func (r *Renewer) retryIn() time.Duration {
	retry := r.keeper.NotAfter().Sub(r.clock.Now()) / 4
	if retry < minRetryInterval {
		return minRetryInterval
	}
	if retry > maxRetryInterval {
		return maxRetryInterval
	}
	return retry
}

func renewAt(notBefore, notAfter time.Time) time.Time {
	lifetime := notAfter.Sub(notBefore)
	if lifetime <= 0 {
		return notAfter
	}
	return notBefore.Add(time.Duration(float64(lifetime) * renewFraction))
}

// Run renews the agent's certificate for as long as ctx lives.
//
// The loop is deliberately dumb: look at the certificate on hand, work out when
// it should be replaced, sleep until then (in bounded steps), renew, repeat.
// State lives in the certificate itself, so a restarted agent resumes exactly
// where it was with nothing to reconstruct.
func (r *Renewer) Run(ctx context.Context) {
	r.log.Info("certificate renewal loop started",
		"server_id", r.keeper.ServerID(),
		"not_after", r.keeper.NotAfter(),
		"renew_at", r.RenewAt(),
	)
	for {
		wait := r.RenewAt().Sub(r.clock.Now())
		if wait > 0 {
			if wait > maxSleep {
				wait = maxSleep
			}
			select {
			case <-ctx.Done():
				return
			case <-r.clock.After(wait):
			}
			continue
		}

		if err := r.renewOnce(ctx); err != nil {
			// Never fatal. The certificate in hand is still valid for the rest
			// of its final third, and the next attempt may find a plane that
			// has come back.
			retry := r.retryIn()
			r.log.Error("certificate renewal failed; will retry",
				"server_id", r.keeper.ServerID(),
				"not_after", r.keeper.NotAfter(),
				"retry_in", retry,
				"error", err,
			)
			select {
			case <-ctx.Done():
				return
			case <-r.clock.After(retry):
			}
		}
	}
}

// renewOnce performs one renewal: fresh key, CSR to the plane, atomic swap.
// Exported behaviour is tested through Run; this is separated so the retry
// policy above reads as policy rather than plumbing.
func (r *Renewer) renewOnce(ctx context.Context) error {
	serverID := r.keeper.ServerID()
	// A NEW key pair every time. Rotating the certificate over the same key
	// would bound how long a certificate is valid without bounding how long a
	// key that has leaked stays useful — which is most of the point
	// (threat-model §5.2).
	keyPEM, csrPEM, err := pki.GenerateAgentKey(serverID)
	if err != nil {
		return fmt.Errorf("renew: generating key for %s: %w", serverID, err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	certPEM, err := r.signer.RenewCertificate(reqCtx, serverID, csrPEM)
	if err != nil {
		return fmt.Errorf("renew: requesting certificate for %s: %w", serverID, err)
	}
	notAfter, err := NotAfter(certPEM)
	if err != nil {
		return fmt.Errorf("renew: plane returned unusable material for %s: %w", serverID, err)
	}
	if !notAfter.After(r.keeper.NotAfter()) {
		// A certificate that expires no later than the one already held is not
		// a renewal. Refusing it keeps the loop from spinning: installing it
		// would leave RenewAt in the past and we would ask again immediately.
		return fmt.Errorf("renew: plane issued a certificate for %s expiring at %s, no later than the current %s",
			serverID, notAfter, r.keeper.NotAfter())
	}
	if err := r.keeper.Rotate(certPEM, keyPEM); err != nil {
		return fmt.Errorf("renew: installing certificate for %s: %w", serverID, err)
	}
	r.log.Info("agent certificate renewed",
		"server_id", serverID,
		"not_after", notAfter,
		"renew_at", r.RenewAt(),
	)
	if r.onRotate != nil {
		r.onRotate(notAfter)
	}
	return nil
}
