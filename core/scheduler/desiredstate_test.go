package scheduler

// Fail-closed desired state (ADR-005, agent-identity-and-tls.md §4).
//
// The sync reply is the COMPLETE desired set and the agent REPLACES what it
// holds with it, so an application missing from this reply is an instruction to
// tear its container down. These tests pin the boundary: a store that could not
// answer fails the whole sync (the agent times out and keeps what it has),
// while a permanent, application-scoped data problem omits exactly one entry.
//
// The distinction became reachable while a fleet is up when a `work.<id>.resync`
// nudge started re-syncing every node on a panel-wide settings change; before
// that, a mid-life re-sync merged and only boot could shrink the set.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// errStoreDown is the transient infrastructure failure these tests inject: a
// connection that dropped, not a row that is missing.
var errStoreDown = errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")

// buildApp adds an application to srv_1 and drives it to a built revision, so
// it is part of the server's desired set.
func buildApp(t *testing.T, s *Scheduler, fs *fakeStore, appID string) domain.Application {
	t.Helper()
	app := fs.addApp(appID, "srv_1")
	dep, err := s.Deploy(context.Background(), appID, "manual", "")
	if err != nil {
		t.Fatalf("Deploy %s: %v", appID, err)
	}
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	return app
}

// A store read that did not answer must fail the sync, not shrink the desired
// set. Answering with the remaining apps would tell the agent to remove the one
// whose revision could not be read — a momentary Postgres blip would take a
// running application down on every node the resync nudge reached.
func TestDesiredStateFailsClosedWhenARevisionCannotBeLoaded(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	s := newScheduler(fs, fb)
	buildApp(t, s, fs, "app_1")
	buildApp(t, s, fs, "app_2")

	// Both are desired while the store is healthy — so the shrunken set below
	// really would have been a teardown instruction.
	if got := desiredState(t, s, "srv_1").GetSpecs(); len(got) != 2 {
		t.Fatalf("desired = %d apps before the failure, want 2", len(got))
	}

	fs.revisionErr = errStoreDown
	data, err := s.DesiredStateFor(context.Background(), "srv_1")
	if err == nil {
		t.Fatalf("DesiredStateFor answered a %d-byte set while the store was down; the agent would have torn apps down", len(data))
	}
	if !errors.Is(err, errStoreDown) {
		t.Fatalf("error = %v, want it to wrap the store failure", err)
	}
	if data != nil {
		t.Fatalf("payload = %d bytes alongside an error, want none", len(data))
	}
}

// The same rule for managed databases: the agent's database reconciler removes
// what desired state does not name (managed-databases.md §6).
func TestDesiredStateFailsClosedWhenADatabaseRevisionCannotBeLoaded(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	addDatabase(fs, "db_1", domain.EnginePostgreSQL, "app")
	s := newScheduler(fs, fb)

	if got := desiredState(t, s, "srv_1").GetDbSpecs(); len(got) != 1 {
		t.Fatalf("desired = %d databases before the failure, want 1", len(got))
	}

	fs.dbRevisionErr = errStoreDown
	if _, err := s.DesiredStateFor(context.Background(), "srv_1"); !errors.Is(err, errStoreDown) {
		t.Fatalf("error = %v, want the store failure to fail the sync", err)
	}
}

// Sealed data the master key cannot open is a key problem, not an application
// problem: every app on the node would be omitted at once, so it must fail the
// sync rather than empty the fleet's desired state.
func TestDesiredStateFailsClosedWhenSealedEnvWillNotOpen(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	s := newScheduler(fs, fb)
	buildApp(t, s, fs, "app_1")
	fs.envVars["app_1"] = []domain.EnvVar{{Key: "K", ValueCT: []byte("sealed:v"), ValueNonce: []byte("n")}}

	broken := New(fs, fb, failingOpener{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := broken.DesiredStateFor(context.Background(), "srv_1"); err == nil {
		t.Fatal("DesiredStateFor omitted an app whose env would not unseal; a wrong master key would empty desired state")
	}
}

// The other side of the boundary: a permanent, application-scoped data problem
// omits that one application and lets the rest of the node converge. No retry
// can produce a spec for it, so holding the whole node's sync hostage to it
// would strand every other application on the server.
func TestDesiredStateOmitsOneAppOnPermanentDataProblems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(fs *fakeStore, revID string)
	}{
		{
			name: "revision row is gone",
			breakIt: func(fs *fakeStore, revID string) {
				fs.mu.Lock()
				defer fs.mu.Unlock()
				delete(fs.revisions, revID)
			},
		},
		{
			name: "config snapshot will not parse",
			breakIt: func(fs *fakeStore, revID string) {
				fs.mu.Lock()
				defer fs.mu.Unlock()
				rev := fs.revisions[revID]
				rev.ConfigSnapshot = []byte("{ not json")
				fs.revisions[revID] = rev
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, fb := newFakeStore(), &fakeBus{}
			s := newScheduler(fs, fb)
			buildApp(t, s, fs, "app_broken")
			buildApp(t, s, fs, "app_healthy")

			broken := fs.apps["app_broken"]
			if broken.DesiredRevisionID == nil {
				t.Fatal("app_broken has no desired revision")
			}
			tc.breakIt(fs, *broken.DesiredRevisionID)

			specs := desiredState(t, s, "srv_1").GetSpecs()
			if len(specs) != 1 || specs[0].GetAppId() != "app_healthy" {
				t.Fatalf("desired = %+v, want only app_healthy", specs)
			}
		})
	}
}
