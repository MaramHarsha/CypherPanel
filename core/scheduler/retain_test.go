package scheduler

// The garbage-collection retain set (disk-management.md §2): which revisions'
// images a node is told to keep. This is what turns GC from a heuristic into a
// reconciler, so the properties worth testing are the ones a prune job gets
// wrong.

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// seedRevisions gives an app n revisions, oldest first, and points desired
// state at the newest.
func seedRevisions(fs *fakeStore, appID string, n int) []domain.Revision {
	base := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	out := make([]domain.Revision, 0, n)
	for i := 0; i < n; i++ {
		rev := domain.Revision{
			ID: appID + "-rev" + string(rune('1'+i)), ApplicationID: appID,
			Image: "cypher/" + appID + ":r", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		fs.revisions[rev.ID] = rev
		out = append(out, rev)
	}
	app := fs.apps[appID]
	newest := out[len(out)-1].ID
	app.DesiredRevisionID = &newest
	fs.apps[appID] = app
	return out
}

func TestRetainKeepsTheDeployedRevisionAndTheWindow(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	revs := seedRevisions(fs, "app_1", 5)
	s := newScheduler(fs, fb)
	s.SetRevisionRetain(3)

	got := s.retainFor(context.Background(), "srv_1", []domain.Application{fs.apps["app_1"]})
	if len(got) != 1 {
		t.Fatalf("retain = %+v, want one application", got)
	}
	ids := got[0].GetRevisionIds()
	if len(ids) != 3 {
		t.Fatalf("kept %v, want three", ids)
	}
	// The deployed one first and unconditionally: a window that could exclude
	// it would be a request to delete the image serving traffic.
	if ids[0] != revs[4].ID {
		t.Fatalf("first kept = %s, want the deployed revision %s", ids[0], revs[4].ID)
	}
	// Then the most recent others — what a rollback can name.
	if ids[1] != revs[3].ID || ids[2] != revs[2].ID {
		t.Fatalf("kept %v, want the newest three", ids)
	}
}

// The property a prune job cannot have: what a rollback needs is retained
// because it is named, not because it happened to still be tagged.
func TestRetainNeverDropsTheDeployedRevision(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	revs := seedRevisions(fs, "app_1", 5)
	// Rolled back to the oldest: it is now deployed, and far outside any
	// recency window.
	app := fs.apps["app_1"]
	oldest := revs[0].ID
	app.DesiredRevisionID = &oldest
	fs.apps["app_1"] = app

	s := newScheduler(fs, fb)
	s.SetRevisionRetain(2)

	got := s.retainFor(context.Background(), "srv_1", []domain.Application{fs.apps["app_1"]})
	ids := got[0].GetRevisionIds()
	if ids[0] != oldest {
		t.Fatalf("kept %v, want the rolled-back-to revision first", ids)
	}
}

// An application with nothing to keep is OMITTED, never sent empty: an empty
// list would read as "keep nothing", and the two mistakes do not cost the same.
func TestAnApplicationWithNoRevisionsIsOmitted(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	got := s.retainFor(context.Background(), "srv_1", []domain.Application{fs.apps["app_1"]})
	if len(got) != 0 {
		t.Fatalf("retain = %+v, want the application omitted rather than sent empty", got)
	}
}

// A window smaller than one is not a policy anyone can mean.
func TestRetainWindowIsAtLeastOne(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	seedRevisions(fs, "app_1", 3)
	s := newScheduler(fs, fb)
	s.SetRevisionRetain(0) // ignored

	got := s.retainFor(context.Background(), "srv_1", []domain.Application{fs.apps["app_1"]})
	if len(got) != 1 || len(got[0].GetRevisionIds()) == 0 {
		t.Fatalf("retain = %+v, want the default window rather than nothing", got)
	}
}

// Desired state carries it, so the agent gets the instruction on every sync
// rather than only when something is deployed.
func TestDesiredStateCarriesTheRetainSet(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	seedRevisions(fs, "app_1", 2)
	s := newScheduler(fs, fb)

	data, err := s.DesiredStateFor(context.Background(), "srv_1")
	if err != nil {
		t.Fatalf("DesiredStateFor: %v", err)
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		t.Fatalf("unmarshal desired state: %v", err)
	}
	if len(ds.GetRetain()) != 1 || ds.GetRetain()[0].GetAppId() != "app_1" {
		t.Fatalf("retain = %+v, want the application's window", ds.GetRetain())
	}
}
