// Package backupsched runs the control-plane side of scheduled backups. On a
// fixed interval it finds destinations whose cadence is due and dispatches one
// idempotent backup.run task per active account.
//
// Like sslrenew, this scheduler only has to be roughly right: restic
// deduplicates, so a backup dispatched slightly early or twice costs almost
// nothing, while a missed one is the failure that actually matters.
package backupsched

import (
	"context"
	"log/slog"
	"time"

	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// DestinationStore is the slice of the backups store the scheduler needs.
type DestinationStore interface {
	ListScheduledDestinations(ctx context.Context, now time.Time) ([]store.BackupDestination, error)
	MarkDestinationRun(ctx context.Context, id string) error
}

// AccountLister enumerates the accounts a sweep should back up. "" means all
// resellers — scheduled backups are fleet-wide.
type AccountLister interface {
	List(ctx context.Context, resellerID string) ([]store.Account, error)
}

// DispatchFunc enqueues one backup.run task. The concrete implementation lives
// in the core wiring so this package stays free of NATS/task-store details.
type DispatchFunc func(ctx context.Context, account store.Account, dest store.BackupDestination) error

// Scheduler periodically backs every active account up to each due destination.
type Scheduler struct {
	Destinations DestinationStore
	Accounts     AccountLister
	Dispatch     DispatchFunc
	Interval     time.Duration
	Clock        func() time.Time
	Log          *slog.Logger
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

// Run sweeps immediately, then every Interval, until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.log().Info("backup scheduler started", "interval", s.Interval)
	s.RunOnce(ctx)

	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log().Info("backup scheduler stopping")
			return
		case <-ticker.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce performs one sweep and returns how many backups it dispatched.
// Per-account failures are logged and skipped so one bad account never stalls
// the rest of the fleet's backups.
func (s *Scheduler) RunOnce(ctx context.Context) (dispatched int) {
	due, err := s.Destinations.ListScheduledDestinations(ctx, s.clock())
	if err != nil {
		s.log().Error("backup scheduler: listing due destinations", "error", err)
		return 0
	}
	if len(due) == 0 {
		return 0
	}

	accounts, err := s.Accounts.List(ctx, "")
	if err != nil {
		s.log().Error("backup scheduler: listing accounts", "error", err)
		return 0
	}

	for _, dest := range due {
		var sent int
		for _, a := range accounts {
			// Only back up accounts that actually have data on disk. A
			// suspended account keeps its files, so it stays in scope; a
			// failed or terminating one does not.
			if a.Status != "active" && a.Status != "suspended" {
				continue
			}
			if err := s.Dispatch(ctx, a, dest); err != nil {
				s.log().Error("backup scheduler: dispatch failed",
					"account_id", a.ID, "destination", dest.Name, "error", err)
				continue
			}
			sent++
		}
		// Mark the destination run even when some accounts failed: the cadence
		// is per-destination, and not marking it would re-sweep every tick and
		// pile duplicate tasks on the accounts that did succeed.
		if err := s.Destinations.MarkDestinationRun(ctx, dest.ID); err != nil {
			s.log().Error("backup scheduler: marking destination run", "destination", dest.Name, "error", err)
		}
		s.log().Info("backup scheduler: swept destination",
			"destination", dest.Name, "schedule", dest.Schedule, "dispatched", sent)
		dispatched += sent
	}
	return dispatched
}
