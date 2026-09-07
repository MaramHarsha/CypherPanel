package onboarding

import (
	"context"
	"errors"
	"testing"
)

type fakeCounts struct {
	users, servers, projects, deploys int64
	err                               error
}

func (f fakeCounts) CountUsers(context.Context) (int64, error)    { return f.users, f.err }
func (f fakeCounts) CountProjects(context.Context) (int64, error) { return f.projects, f.err }
func (f fakeCounts) CountEnrolledServers(context.Context) (int64, error) {
	return f.servers, f.err
}
func (f fakeCounts) CountSucceededDeployments(context.Context) (int64, error) {
	return f.deploys, f.err
}

func steps(t *testing.T, c fakeCounts) map[string]Step {
	t.Helper()
	p, err := (&Service{}).Progress(context.Background(), c)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	out := map[string]Step{}
	for _, s := range p.Steps {
		out[s.Name] = s
	}
	return out
}

// A fresh panel has exactly one step done — the account whose owner is reading
// the band — and the rest are the golden path in order.
func TestAFreshPanelHasOnlyTheAccountStepDone(t *testing.T) {
	got := steps(t, fakeCounts{users: 1})
	if !got[StepOwner].Complete {
		t.Fatal("the owner step is not complete on a panel with an account")
	}
	for _, name := range []string{StepServer, StepProject, StepDeploy} {
		if got[name].Complete {
			t.Fatalf("%s is complete on a fresh panel", name)
		}
	}
}

// The last step is a deployment that SUCCEEDED. A created-but-failed deployment
// proves the opposite of what the step is asking.
func TestOnlyASucceededDeploymentFinishesThePath(t *testing.T) {
	p, err := (&Service{}).Progress(context.Background(), fakeCounts{users: 1, servers: 1, projects: 1, deploys: 0})
	if err != nil {
		t.Fatal(err)
	}
	if p.Done {
		t.Fatal("the path is done with no succeeded deployment")
	}
	p, err = (&Service{}).Progress(context.Background(), fakeCounts{users: 1, servers: 1, projects: 1, deploys: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Done {
		t.Fatal("the path is not done after a succeeded deployment")
	}
}

// Progress is DERIVED, so it moves BOTH ways. Deleting every server makes the
// panel ask for one again — which is true, and a stored flag would have lied
// (guided-onboarding.md §2). This is the property that makes the design worth
// the four queries.
func TestProgressFollowsRealityInBothDirections(t *testing.T) {
	with := steps(t, fakeCounts{users: 1, servers: 2, projects: 1})
	if !with[StepServer].Complete || with[StepServer].Count != 2 {
		t.Fatalf("server step = %+v", with[StepServer])
	}
	without := steps(t, fakeCounts{users: 1, servers: 0, projects: 1})
	if without[StepServer].Complete {
		t.Fatal("the server step stayed complete after every server was removed")
	}
}

// A failure is reported rather than rendered as "nothing left to do": a band
// that silently claimed the panel was set up would be worse than no band.
func TestACountingFailureIsReportedRatherThanReadAsDone(t *testing.T) {
	_, err := (&Service{}).Progress(context.Background(), fakeCounts{err: errors.New("db down")})
	if err == nil {
		t.Fatal("a counting failure was swallowed")
	}
}
