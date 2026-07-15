// Package sslrenew runs the control-plane side of automatic certificate
// renewal. On a fixed interval it finds accounts whose certificate is within
// the renewal window and re-dispatches the very same idempotent `ssl.issue`
// task used for first issuance (see the ssl-acme skill: "renewal is the same
// task path as issuance"). The agent's own >30-day skip guard means a
// dispatched renewal that turns out not to be needed is a harmless no-op, so
// this scheduler only has to be roughly right, never exact.
package sslrenew

import (
	"context"
	"log/slog"
	"time"

	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// Store is the slice of the accounts store the scheduler needs. Kept as an
// interface so the loop is unit-testable without a database.
type Store interface {
	ListExpiringSSL(ctx context.Context, before time.Time) ([]store.Account, error)
	SetSSL(ctx context.Context, id, status string, expiresAt *time.Time) error
}

// DispatchFunc enqueues an ssl.issue task for one account (create the task row
// + publish to the agent). The concrete implementation lives in the core
// wiring so this package stays free of NATS/task-store details.
type DispatchFunc func(ctx context.Context, a store.Account) error

// Scheduler periodically renews soon-to-expire certificates.
type Scheduler struct {
	Accounts  Store
	Dispatch  DispatchFunc
	Threshold time.Duration // renew when the cert expires within this window
	Interval  time.Duration // how often to scan
	// Clock is injectable for tests; defaults to time.Now.
	Clock func() time.Time
	Log   *slog.Logger
}

func (s *Scheduler) clock() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run scans immediately, then every Interval, until ctx is cancelled. Intended
// to be launched in its own goroutine at core startup.
func (s *Scheduler) Run(ctx context.Context) {
	s.log().Info("ssl renewal scheduler started",
		"interval", s.Interval, "threshold", s.Threshold)
	// An initial scan on boot catches anything that came due while the core
	// was down.
	s.runOnce(ctx)

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log().Info("ssl renewal scheduler stopping")
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// runOnce performs a single renewal sweep. Per-account failures are logged and
// skipped so one bad account never stalls the rest of the batch.
func (s *Scheduler) runOnce(ctx context.Context) (renewed int) {
	before := s.clock().Add(s.Threshold)
	due, err := s.Accounts.ListExpiringSSL(ctx, before)
	if err != nil {
		s.log().Error("ssl renewal: listing expiring certs", "error", err)
		return 0
	}
	if len(due) == 0 {
		return 0
	}
	s.log().Info("ssl renewal: certificates due", "count", len(due))
	for _, a := range due {
		// Mirror the manual issuance path: mark issuing, then dispatch. The
		// task result flips it back to active (or failed) with the new expiry.
		if err := s.Accounts.SetSSL(ctx, a.ID, "issuing", a.SSLExpiresAt); err != nil {
			s.log().Error("ssl renewal: marking issuing", "account_id", a.ID, "error", err)
			continue
		}
		if err := s.Dispatch(ctx, a); err != nil {
			s.log().Error("ssl renewal: dispatch failed", "account_id", a.ID, "domain", a.PrimaryDomain, "error", err)
			// Leave a breadcrumb the operator can see; the next sweep retries.
			_ = s.Accounts.SetSSL(ctx, a.ID, "failed", a.SSLExpiresAt)
			continue
		}
		s.log().Info("ssl renewal: dispatched", "account_id", a.ID, "domain", a.PrimaryDomain,
			"expires_at", a.SSLExpiresAt)
		renewed++
	}
	return renewed
}
