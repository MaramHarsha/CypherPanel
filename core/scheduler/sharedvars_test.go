package scheduler

// Shared-variable resolution on the existing sealed-env path
// (shared-variables.md §4, §5). This feature adds no path to the agent: the
// only thing that changes is what AppSpec.Env holds by the time it goes on the
// wire, so every test here reads the published work item rather than any new
// surface.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// sharedVar builds a sealed shared variable in the fakeOpener's convention.
func sharedVar(key, value string, envID *string) domain.SharedVariable {
	return domain.SharedVariable{
		ID:            "sv_" + key,
		ProjectID:     "prj_1",
		EnvironmentID: envID,
		Key:           key,
		ValueCT:       []byte("sealed:" + value),
		ValueNonce:    []byte("n"),
		UpdatedAt:     time.Unix(0, 0),
	}
}

// envVar builds a sealed app env var carrying the given shared references.
func envVar(key, value string, refs ...string) domain.EnvVar {
	return domain.EnvVar{
		Key:        key,
		ValueCT:    []byte("sealed:" + value),
		ValueNonce: []byte("n"),
		SharedRefs: refs,
	}
}

// rolloutSpec drives one app to rollout and returns the spec that was published.
func rolloutSpec(t *testing.T, s *Scheduler, fs *fakeStore, fb *fakeBus, appID string) *agentv1.AppSpec {
	t.Helper()
	dep, err := s.Deploy(context.Background(), appID, "manual", "")
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	s.HandleDeployEvent(context.Background(), fs.apps[appID].Runtime.ServerID, &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_BUILD,
		Outcome:      agentv1.DeployEvent_OUTCOME_SUCCEEDED,
	})
	p, ok := fb.last()
	if !ok {
		t.Fatal("nothing published")
	}
	var rw agentv1.RolloutWork
	if err := proto.Unmarshal(p.data, &rw); err != nil {
		t.Fatalf("unmarshal rollout: %v", err)
	}
	return rw.GetSpec()
}

// Acceptance §9.1: the container's env holds the shared value, expanded
// substring-wise so a connection string can be composed from shared parts.
func TestSharedVariablesExpandIntoTheSpec(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{
		sharedVar("SENTRY_DSN", "https://k@sentry.io/1", nil),
		sharedVar("DB_USER", "app", nil),
		sharedVar("DB_PASS", "hunter2", nil),
	}
	fs.envVars["app_1"] = []domain.EnvVar{
		envVar("SENTRY_DSN", "{{shared.SENTRY_DSN}}", "SENTRY_DSN"),
		envVar("DATABASE_URL", "postgres://{{shared.DB_USER}}:{{shared.DB_PASS}}@db:5432/app", "DB_USER", "DB_PASS"),
		envVar("PLAIN", "no references here"),
	}
	s := newScheduler(fs, fb)

	env := rolloutSpec(t, s, fs, fb, "app_1").GetEnv()
	if env["SENTRY_DSN"] != "https://k@sentry.io/1" {
		t.Errorf("SENTRY_DSN = %q, want the shared value", env["SENTRY_DSN"])
	}
	if env["DATABASE_URL"] != "postgres://app:hunter2@db:5432/app" {
		t.Errorf("DATABASE_URL = %q, want the composed value", env["DATABASE_URL"])
	}
	if env["PLAIN"] != "no references here" {
		t.Errorf("PLAIN = %q, want it untouched", env["PLAIN"])
	}
}

// Acceptance §9.2: an environment-scoped row shadows the project-scoped one of
// the same key, and only for that environment.
func TestEnvironmentScopeShadowsProjectScope(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.environments["env_stage"] = domain.Environment{ID: "env_stage", ProjectID: "prj_1", Name: "staging"}

	prod := fs.addApp("app_prod", "srv_1") // env_1, named "production"
	stage := fs.addApp("app_stage", "srv_1")
	stage.EnvironmentID = "env_stage"
	fs.apps["app_stage"] = stage
	_ = prod

	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("SMTP_HOST", "mail.internal", nil)}
	envID := "env_1"
	fs.sharedVars["prj_1|env_1"] = []domain.SharedVariable{sharedVar("SMTP_HOST", "smtp.sendgrid.net", &envID)}

	ref := []domain.EnvVar{envVar("SMTP_HOST", "{{shared.SMTP_HOST}}", "SMTP_HOST")}
	fs.envVars["app_prod"] = ref
	fs.envVars["app_stage"] = ref

	s := newScheduler(fs, fb)
	if got := rolloutSpec(t, s, fs, fb, "app_prod").GetEnv()["SMTP_HOST"]; got != "smtp.sendgrid.net" {
		t.Errorf("production SMTP_HOST = %q, want the environment-scoped value", got)
	}
	if got := rolloutSpec(t, s, fs, fb, "app_stage").GetEnv()["SMTP_HOST"]; got != "mail.internal" {
		t.Errorf("staging SMTP_HOST = %q, want the project-scoped value", got)
	}
}

// Acceptance §9.4: an unresolvable reference fails the deploy EARLY — the
// deployment is failed with a detail naming the missing key, and nothing was
// published on work.*. No build minutes, no container touched.
func TestUnresolvableReferenceFailsTheDeployBeforeAnyWork(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.envVars["app_1"] = []domain.EnvVar{envVar("SENTRY_DSN", "{{shared.NOPE}}", "NOPE")}
	s := newScheduler(fs, fb)

	dep, err := s.Deploy(context.Background(), "app_1", "manual", "")
	if err == nil {
		t.Fatal("Deploy accepted an unresolvable reference")
	}
	var unresolved *UnresolvedReferenceError
	if !errors.As(err, &unresolved) {
		t.Fatalf("Deploy = %v, want UnresolvedReferenceError", err)
	}
	if fb.count() != 0 {
		t.Fatalf("%d work items published; want none", fb.count())
	}
	got, gerr := fs.GetDeployment(context.Background(), dep.ID)
	if gerr != nil {
		t.Fatalf("GetDeployment: %v", gerr)
	}
	if got.Status != domain.DeployFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	for _, want := range []string{"SENTRY_DSN", "NOPE", "production"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not name %q", got.Detail, want)
		}
	}
}

// The same failure for a rollout-first deploy (rollback, image-source app):
// resolution happens before the rev.Image branch, so both shapes fail
// identically rather than one of them slipping past the gate.
func TestUnresolvableReferenceFailsARollbackToo(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("K", "v", nil)}
	fs.envVars["app_1"] = []domain.EnvVar{envVar("X", "{{shared.K}}", "K")}
	s := newScheduler(fs, fb)

	// One successful deploy so there is a built revision to roll back to.
	first := rolloutSpec(t, s, fs, fb, "app_1")
	if first.GetEnv()["X"] != "v" {
		t.Fatalf("setup: X = %q", first.GetEnv()["X"])
	}
	dep1, _ := fs.latestDeployment("app_1")
	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId: "app_1", RevisionId: dep1.RevisionID, State: domain.AppRunning,
	})

	// Now the shared variable disappears underneath it (the §9.4 forcing move).
	fs.sharedVars["prj_1|"] = nil
	before := fb.count()

	dep, err := s.Rollback(context.Background(), dep1.ID)
	if err == nil {
		t.Fatal("Rollback accepted an unresolvable reference")
	}
	if fb.count() != before {
		t.Fatalf("work published during a failing rollback: %d new items", fb.count()-before)
	}
	got, _ := fs.GetDeployment(context.Background(), dep.ID)
	if got.Status != domain.DeployFailed {
		t.Fatalf("rollback status = %s, want failed", got.Status)
	}
}

// A converge push propagates the error and publishes nothing: the container is
// already running the environment it was deployed with, so nothing is lost (§4).
func TestConvergeRefusesAnUnresolvableReference(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("K", "v", nil)}
	fs.envVars["app_1"] = []domain.EnvVar{envVar("X", "{{shared.K}}", "K")}
	s := newScheduler(fs, fb)
	rolloutSpec(t, s, fs, fb, "app_1")

	fs.sharedVars["prj_1|"] = nil
	before := fb.count()
	if err := s.ConvergeApp(context.Background(), "app_1"); err == nil {
		t.Fatal("ConvergeApp accepted an unresolvable reference")
	}
	if fb.count() != before {
		t.Fatalf("converge published %d items despite the error", fb.count()-before)
	}
}

// A sync reply is the complete desired set, so absence means REMOVE (ADR-005).
// The offending KEY is omitted; the APPLICATION stays advertised (§4).
func TestDesiredStateOmitsTheKeyNotTheApplication(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("K", "v", nil)}
	fs.envVars["app_1"] = []domain.EnvVar{
		envVar("X", "{{shared.K}}", "K"),
		envVar("KEEP", "plain"),
	}
	s := newScheduler(fs, fb)
	rolloutSpec(t, s, fs, fb, "app_1")

	fs.sharedVars["prj_1|"] = nil

	data, err := s.DesiredStateFor(context.Background(), "srv_1")
	if err != nil {
		t.Fatalf("DesiredStateFor: %v", err)
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ds.Specs) != 1 || ds.Specs[0].GetAppId() != "app_1" {
		t.Fatalf("desired = %+v, want the application still advertised", ds.Specs)
	}
	env := ds.Specs[0].GetEnv()
	if _, present := env["X"]; present {
		t.Errorf("X = %q; an unresolvable key must be omitted, never emptied", env["X"])
	}
	if env["KEEP"] != "plain" {
		t.Errorf("KEEP = %q, want the unaffected variable untouched", env["KEEP"])
	}
}

// §5: startRollout stamps the deployment, and only an OBSERVED running rollout
// copies the stamp onto the application — a failed rollout can never mark an
// app clean.
func TestEnvStampMovesOnlyOnAnObservedRollout(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("K", "v", nil)}
	fs.envVars["app_1"] = []domain.EnvVar{envVar("X", "{{shared.K}}", "K")}
	s := newScheduler(fs, fb)

	rolloutSpec(t, s, fs, fb, "app_1")
	dep, _ := fs.latestDeployment("app_1")
	if _, stamped := fs.envResolved[dep.ID]; !stamped {
		t.Fatal("startRollout did not stamp the deployment's resolved environment")
	}
	if _, applied := fs.envApplied["app_1"]; applied {
		t.Fatal("the application was marked clean before the rollout was observed")
	}

	s.HandleAppStatus(context.Background(), "srv_1", &agentv1.AppStatus{
		AppId: "app_1", RevisionId: dep.RevisionID, State: domain.AppRunning,
	})
	if fs.envApplied["app_1"] != fs.envResolved[dep.ID] {
		t.Fatalf("applied stamp = %v, want the deployment's resolved stamp %v",
			fs.envApplied["app_1"], fs.envResolved[dep.ID])
	}
}

// A failed rollout leaves the application's stamp exactly where it was.
func TestFailedRolloutDoesNotMoveTheEnvStamp(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	s := newScheduler(fs, fb)

	rolloutSpec(t, s, fs, fb, "app_1")
	dep, _ := fs.latestDeployment("app_1")
	s.HandleDeployEvent(context.Background(), "srv_1", &agentv1.DeployEvent{
		DeploymentId: dep.ID,
		Stage:        agentv1.DeployEvent_STAGE_ROLLOUT,
		Outcome:      agentv1.DeployEvent_OUTCOME_FAILED,
		Detail:       "health gate never passed",
	})
	if _, applied := fs.envApplied["app_1"]; applied {
		t.Fatal("a failed rollout marked the application's environment applied")
	}
}

// An application with no references costs exactly what it did before: the
// shared table is never read.
func TestNoReferencesReadsNoSharedVariables(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.addApp("app_1", "srv_1")
	fs.envVars["app_1"] = []domain.EnvVar{envVar("PLAIN", "value")}
	// A poisoned shared row: if resolution reads it, the unseal convention still
	// works, but the count below proves the read never happened.
	fs.sharedVars["prj_1|"] = []domain.SharedVariable{sharedVar("K", "v", nil)}
	s := newScheduler(fs, fb)

	env := rolloutSpec(t, s, fs, fb, "app_1").GetEnv()
	if env["PLAIN"] != "value" {
		t.Fatalf("PLAIN = %q", env["PLAIN"])
	}
	if fs.scopeReads != 0 {
		t.Fatalf("shared variables were read %d times for an app with no references", fs.scopeReads)
	}
}
