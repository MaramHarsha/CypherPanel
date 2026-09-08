package onboarding

// Guided onboarding progress (docs/features/guided-onboarding.md).
//
// PROGRESS IS DERIVED, NEVER STORED, and that is the whole design. A stored
// flag has to be written by whoever completes a step, so every creation path
// would have to remember to write it — the template installer, the preview
// environment, the API, a future CLI. dns-automation.md §4.3 records exactly
// this lesson after the first real use of DNS automation produced no record,
// because a template install created an application through a path nobody had
// hooked. Onboarding would fail the same way and more visibly: the panel would
// still be asking for a server twenty minutes after one was added.
//
// Four counts, read on request. Nothing to migrate, nothing to backfill, and it
// self-heals in the direction that matters — delete every server and the panel
// says you need one again, which is true, where a stored flag would have lied.

import (
	"context"
	"fmt"
)

// Step names, in the order the matrix's four steps run.
const (
	StepOwner   = "owner"
	StepServer  = "server"
	StepProject = "project"
	StepDeploy  = "deploy"
)

// Step is one row of the band.
type Step struct {
	Name     string
	Complete bool
	// Count is what the step is counting — servers, projects, successful
	// deployments. Shown so "1 server" reads as a fact rather than a tick.
	Count int64
}

// Progress is the whole answer.
type Progress struct {
	Steps []Step
	// Done is true once the LAST step is complete. The band renders only while
	// this is false, and because the whole thing is derived it then stays gone:
	// a panel whose last server is decommissioned during maintenance must not
	// greet its owner with a beginner's wizard (§3).
	Done bool
}

// ProgressStore is the counting this needs (consumer-defined; *store.Store
// satisfies it). Counts rather than lists: the band shows how many, and loading
// every application to learn there is one would be a strange way to ask.
type ProgressStore interface {
	CountUsers(ctx context.Context) (int64, error)
	CountEnrolledServers(ctx context.Context) (int64, error)
	CountProjects(ctx context.Context) (int64, error)
	CountSucceededDeployments(ctx context.Context) (int64, error)
}

// Progress reports how far this panel is through the golden path.
func (s *Service) Progress(ctx context.Context, ps ProgressStore) (Progress, error) {
	users, err := ps.CountUsers(ctx)
	if err != nil {
		return Progress{}, fmt.Errorf("onboarding: counting users: %w", err)
	}
	servers, err := ps.CountEnrolledServers(ctx)
	if err != nil {
		return Progress{}, fmt.Errorf("onboarding: counting servers: %w", err)
	}
	projects, err := ps.CountProjects(ctx)
	if err != nil {
		return Progress{}, fmt.Errorf("onboarding: counting projects: %w", err)
	}
	deploys, err := ps.CountSucceededDeployments(ctx)
	if err != nil {
		return Progress{}, fmt.Errorf("onboarding: counting deployments: %w", err)
	}

	p := Progress{Steps: []Step{
		{Name: StepOwner, Complete: users > 0, Count: users},
		{Name: StepServer, Complete: servers > 0, Count: servers},
		{Name: StepProject, Complete: projects > 0, Count: projects},
		// A SUCCEEDED deployment, not a created one: the step proves the whole
		// path worked, and a deployment that failed proves the opposite.
		{Name: StepDeploy, Complete: deploys > 0, Count: deploys},
	}}
	p.Done = p.Steps[len(p.Steps)-1].Complete
	return p, nil
}
