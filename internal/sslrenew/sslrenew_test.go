package sslrenew

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/CypherPanel/internal/store"
)

// fakeStore records SetSSL calls and returns a fixed candidate list.
type fakeStore struct {
	due      []store.Account
	listErr  error
	setCalls []struct {
		id     string
		status string
	}
	setErrFor map[string]error
}

func (f *fakeStore) ListExpiringSSL(_ context.Context, _ time.Time) ([]store.Account, error) {
	return f.due, f.listErr
}

func (f *fakeStore) SetSSL(_ context.Context, id, status string, _ *time.Time) error {
	f.setCalls = append(f.setCalls, struct {
		id     string
		status string
	}{id, status})
	if f.setErrFor != nil {
		if err, ok := f.setErrFor[id+":"+status]; ok {
			return err
		}
	}
	return nil
}

func acct(id, domain string) store.Account {
	exp := time.Now().Add(10 * 24 * time.Hour)
	return store.Account{ID: id, PrimaryDomain: domain, SystemUsername: "cyph_" + id, SSLExpiresAt: &exp}
}

func TestRunOnce_DispatchesEachDueAccountAndMarksIssuing(t *testing.T) {
	fs := &fakeStore{due: []store.Account{acct("a1", "one.example.com"), acct("a2", "two.example.com")}}
	var dispatched []string
	s := &Scheduler{
		Accounts:  fs,
		Threshold: 30 * 24 * time.Hour,
		Interval:  time.Hour,
		Dispatch: func(_ context.Context, a store.Account) error {
			dispatched = append(dispatched, a.ID)
			return nil
		},
	}

	if got := s.runOnce(context.Background()); got != 2 {
		t.Fatalf("renewed = %d, want 2", got)
	}
	if len(dispatched) != 2 || dispatched[0] != "a1" || dispatched[1] != "a2" {
		t.Fatalf("dispatched = %v, want [a1 a2]", dispatched)
	}
	// Each account should have been marked issuing (once), no failed writes.
	for _, c := range fs.setCalls {
		if c.status != "issuing" {
			t.Fatalf("unexpected SetSSL status %q for %s", c.status, c.id)
		}
	}
	if len(fs.setCalls) != 2 {
		t.Fatalf("SetSSL called %d times, want 2", len(fs.setCalls))
	}
}

func TestRunOnce_DispatchFailureMarksFailedAndContinues(t *testing.T) {
	fs := &fakeStore{due: []store.Account{acct("bad", "bad.example.com"), acct("ok", "ok.example.com")}}
	s := &Scheduler{
		Accounts:  fs,
		Threshold: 30 * 24 * time.Hour,
		Interval:  time.Hour,
		Dispatch: func(_ context.Context, a store.Account) error {
			if a.ID == "bad" {
				return errors.New("nats down")
			}
			return nil
		},
	}

	if got := s.runOnce(context.Background()); got != 1 {
		t.Fatalf("renewed = %d, want 1 (only ok)", got)
	}
	// "bad" must be marked issuing then failed; "ok" only issuing.
	var badFailed, okIssuing bool
	for _, c := range fs.setCalls {
		if c.id == "bad" && c.status == "failed" {
			badFailed = true
		}
		if c.id == "ok" && c.status == "issuing" {
			okIssuing = true
		}
	}
	if !badFailed {
		t.Fatal("failed dispatch should mark account failed")
	}
	if !okIssuing {
		t.Fatal("subsequent account should still be processed after a failure")
	}
}

func TestRunOnce_NoDueAccountsIsNoOp(t *testing.T) {
	fs := &fakeStore{due: nil}
	called := false
	s := &Scheduler{
		Accounts:  fs,
		Threshold: 30 * 24 * time.Hour,
		Interval:  time.Hour,
		Dispatch:  func(_ context.Context, _ store.Account) error { called = true; return nil },
	}
	if got := s.runOnce(context.Background()); got != 0 {
		t.Fatalf("renewed = %d, want 0", got)
	}
	if called {
		t.Fatal("dispatch should not be called when nothing is due")
	}
}

func TestRunOnce_ListErrorIsHandled(t *testing.T) {
	fs := &fakeStore{listErr: errors.New("db down")}
	s := &Scheduler{
		Accounts:  fs,
		Threshold: 30 * 24 * time.Hour,
		Interval:  time.Hour,
		Dispatch:  func(_ context.Context, _ store.Account) error { return nil },
	}
	if got := s.runOnce(context.Background()); got != 0 {
		t.Fatalf("renewed = %d, want 0 on list error", got)
	}
}

// The threshold must be applied to the cutoff passed to the store.
func TestRunOnce_CutoffUsesThreshold(t *testing.T) {
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	var gotCutoff time.Time
	fs := &cutoffStore{capture: &gotCutoff}
	s := &Scheduler{
		Accounts:  fs,
		Threshold: 30 * 24 * time.Hour,
		Interval:  time.Hour,
		Clock:     func() time.Time { return now },
		Dispatch:  func(_ context.Context, _ store.Account) error { return nil },
	}
	s.runOnce(context.Background())
	want := now.Add(30 * 24 * time.Hour)
	if !gotCutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", gotCutoff, want)
	}
}

type cutoffStore struct{ capture *time.Time }

func (c *cutoffStore) ListExpiringSSL(_ context.Context, before time.Time) ([]store.Account, error) {
	*c.capture = before
	return nil, nil
}
func (c *cutoffStore) SetSSL(_ context.Context, _, _ string, _ *time.Time) error { return nil }
