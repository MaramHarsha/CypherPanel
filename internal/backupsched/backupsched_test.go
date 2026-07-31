package backupsched

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MaramHarsha/CypherPanel/internal/store"
)

type fakeDestinations struct {
	due    []store.BackupDestination
	marked []string
}

func (f *fakeDestinations) ListScheduledDestinations(context.Context, time.Time) ([]store.BackupDestination, error) {
	return f.due, nil
}

func (f *fakeDestinations) MarkDestinationRun(_ context.Context, id string) error {
	f.marked = append(f.marked, id)
	return nil
}

type fakeAccounts struct{ accounts []store.Account }

func (f *fakeAccounts) List(context.Context, string) ([]store.Account, error) {
	return f.accounts, nil
}

func quietScheduler(d DestinationStore, a AccountLister, dispatch DispatchFunc) *Scheduler {
	return &Scheduler{
		Destinations: d, Accounts: a, Dispatch: dispatch,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Only accounts with data on disk should be swept. A suspended account keeps
// its files (and is the one you most want a backup of), while failed and
// terminating accounts have nothing worth snapshotting.
func TestRunOnceSkipsAccountsWithoutData(t *testing.T) {
	dests := &fakeDestinations{due: []store.BackupDestination{{ID: "d1", Name: "offsite", Schedule: "daily"}}}
	accounts := &fakeAccounts{accounts: []store.Account{
		{ID: "a1", Status: "active"},
		{ID: "a2", Status: "suspended"},
		{ID: "a3", Status: "failed"},
		{ID: "a4", Status: "terminating"},
	}}

	var dispatched []string
	s := quietScheduler(dests, accounts, func(_ context.Context, a store.Account, _ store.BackupDestination) error {
		dispatched = append(dispatched, a.ID)
		return nil
	})

	if got := s.RunOnce(context.Background()); got != 2 {
		t.Errorf("dispatched count = %d, want 2", got)
	}
	if len(dispatched) != 2 || dispatched[0] != "a1" || dispatched[1] != "a2" {
		t.Errorf("dispatched = %v, want [a1 a2]", dispatched)
	}
}

// A per-account dispatch failure must not stop the rest of the fleet, and the
// destination must still be marked run — otherwise the next tick re-sweeps and
// piles duplicate tasks on the accounts that already succeeded.
func TestRunOnceContinuesAfterDispatchFailure(t *testing.T) {
	dests := &fakeDestinations{due: []store.BackupDestination{{ID: "d1", Name: "offsite"}}}
	accounts := &fakeAccounts{accounts: []store.Account{
		{ID: "a1", Status: "active"},
		{ID: "a2", Status: "active"},
		{ID: "a3", Status: "active"},
	}}

	var seen []string
	s := quietScheduler(dests, accounts, func(_ context.Context, a store.Account, _ store.BackupDestination) error {
		seen = append(seen, a.ID)
		if a.ID == "a2" {
			return errors.New("nats down")
		}
		return nil
	})

	if got := s.RunOnce(context.Background()); got != 2 {
		t.Errorf("dispatched count = %d, want 2 (a2 failed)", got)
	}
	if len(seen) != 3 {
		t.Errorf("every account should be attempted, got %v", seen)
	}
	if len(dests.marked) != 1 || dests.marked[0] != "d1" {
		t.Errorf("destination should be marked run even with a partial failure, got %v", dests.marked)
	}
}

func TestRunOnceNoDueDestinations(t *testing.T) {
	s := quietScheduler(&fakeDestinations{}, &fakeAccounts{}, func(context.Context, store.Account, store.BackupDestination) error {
		t.Fatal("dispatch must not be called when nothing is due")
		return nil
	})
	if got := s.RunOnce(context.Background()); got != 0 {
		t.Errorf("dispatched = %d, want 0", got)
	}
}
