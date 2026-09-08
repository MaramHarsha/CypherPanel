package store

// Compose Stacks against the real database (ENGINEERING rule 29). What the
// fakes cannot prove lives here: the per-environment unique index, the cascades
// that make a delete complete, and the RESTRICT that stops a server going out
// from under a running stack.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

const testComposeFile = "services:\n  web:\n    image: nginx:1.27\n"

func seedStack(t *testing.T, s *Store, envID, serverID, name string) domain.ComposeStack {
	t.Helper()
	stack, err := s.CreateComposeStack(context.Background(), domain.ComposeStack{
		ID: ids.New(ids.PrefixComposeStack), EnvironmentID: envID, Name: name, ServerID: serverID,
	})
	if err != nil {
		t.Fatalf("CreateComposeStack: %v", err)
	}
	return stack
}

func TestStoreComposeStackRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)

	stack, err := s.CreateComposeStack(ctx, domain.ComposeStack{
		ID: ids.New(ids.PrefixComposeStack), EnvironmentID: env.ID,
		Name: "monitoring-" + ids.Secret()[:8], ServerID: srv.ID,
		Route: domain.ComposeRoute{Domain: "grafana.example.com", Service: "grafana", Port: 3000, HTTPS: true},
	})
	if err != nil {
		t.Fatalf("CreateComposeStack: %v", err)
	}
	got, err := s.GetComposeStack(ctx, stack.ID)
	if err != nil {
		t.Fatalf("GetComposeStack: %v", err)
	}
	if got.Route.Service != "grafana" || got.Route.Port != 3000 || !got.Route.HTTPS {
		t.Fatalf("route = %+v", got.Route)
	}
	// Born stopped, with nothing deployed: creating is not deploying.
	if got.Status != domain.AppStopped || got.DesiredRevisionID != nil {
		t.Fatalf("stack = %+v, want it born stopped with no revision", got)
	}
}

func TestStoreComposeStackMissingIsNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.GetComposeStack(context.Background(), "cs_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Two stacks in one environment may not share a name — that is what an operator
// picks them by.
func TestStoreComposeStackNameIsUniquePerEnvironment(t *testing.T) {
	s := testStore(t)
	srv, _, env, _ := seedApp(t, s)
	name := "dup-" + ids.Secret()[:8]
	seedStack(t, s, env.ID, srv.ID, name)

	_, err := s.CreateComposeStack(context.Background(), domain.ComposeStack{
		ID: ids.New(ids.PrefixComposeStack), EnvironmentID: env.ID, Name: name, ServerID: srv.ID,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// Revisions are what rollback restores, so they must survive independently of
// the row's current shape and come back newest-first.
func TestStoreComposeRevisionsAreOrderedAndScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "revs-"+ids.Secret()[:8])
	other := seedStack(t, s, env.ID, srv.ID, "other-"+ids.Secret()[:8])

	var last domain.ComposeRevision
	for i := 0; i < 3; i++ {
		rev, err := s.CreateComposeRevision(ctx, domain.ComposeRevision{
			ID: ids.New(ids.PrefixComposeRevision), StackID: stack.ID,
			ComposeYAML: testComposeFile + "# " + ids.Secret()[:6] + "\n",
		})
		if err != nil {
			t.Fatalf("CreateComposeRevision: %v", err)
		}
		last = rev
	}
	if _, err := s.CreateComposeRevision(ctx, domain.ComposeRevision{
		ID: ids.New(ids.PrefixComposeRevision), StackID: other.ID, ComposeYAML: testComposeFile,
	}); err != nil {
		t.Fatalf("CreateComposeRevision: %v", err)
	}

	list, err := s.ListComposeRevisions(ctx, stack.ID, 50)
	if err != nil {
		t.Fatalf("ListComposeRevisions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("revisions = %d, want only this stack's three", len(list))
	}
	latest, err := s.LatestComposeRevision(ctx, stack.ID)
	if err != nil {
		t.Fatalf("LatestComposeRevision: %v", err)
	}
	if latest.ID != last.ID || list[0].ID != last.ID {
		t.Fatalf("latest = %s and list[0] = %s, want the newest %s", latest.ID, list[0].ID, last.ID)
	}
}

func TestStoreLatestComposeRevisionIsNotFoundBeforeAnyDeploy(t *testing.T) {
	s := testStore(t)
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "bare-"+ids.Secret()[:8])

	if _, err := s.LatestComposeRevision(context.Background(), stack.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStoreComposeStackObservationAndDesiredRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "obs-"+ids.Secret()[:8])
	rev, err := s.CreateComposeRevision(ctx, domain.ComposeRevision{
		ID: ids.New(ids.PrefixComposeRevision), StackID: stack.ID, ComposeYAML: testComposeFile,
	})
	if err != nil {
		t.Fatalf("CreateComposeRevision: %v", err)
	}

	moved, err := s.SetComposeStackDesiredRevision(ctx, stack.ID, rev.ID)
	if err != nil {
		t.Fatalf("SetComposeStackDesiredRevision: %v", err)
	}
	if moved.DesiredRevisionID == nil || *moved.DesiredRevisionID != rev.ID {
		t.Fatalf("desired revision = %v", moved.DesiredRevisionID)
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetComposeStackObservedStatus(ctx, stack.ID, domain.AppRunning, "", rev.ID, at); err != nil {
		t.Fatalf("SetComposeStackObservedStatus: %v", err)
	}
	got, err := s.GetComposeStack(ctx, stack.ID)
	if err != nil {
		t.Fatalf("GetComposeStack: %v", err)
	}
	if got.Status != domain.AppRunning || got.ObservedRevisionID != rev.ID || got.StatusObservedAt == nil {
		t.Fatalf("stack = %+v, want the observation recorded", got)
	}
}

// Deleting the stack takes its revisions and variables with it — a delete that
// left either behind would leave a secret with nothing to explain it.
func TestStoreDeletingAStackCascades(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "cascade-"+ids.Secret()[:8])
	if _, err := s.CreateComposeRevision(ctx, domain.ComposeRevision{
		ID: ids.New(ids.PrefixComposeRevision), StackID: stack.ID, ComposeYAML: testComposeFile,
	}); err != nil {
		t.Fatalf("CreateComposeRevision: %v", err)
	}
	if err := s.UpsertComposeEnvVar(ctx, stack.ID, domain.ComposeEnvVar{
		Key: "TOKEN", ValueCT: []byte("ct"), ValueNonce: []byte("n"),
	}); err != nil {
		t.Fatalf("UpsertComposeEnvVar: %v", err)
	}

	if err := s.DeleteComposeStack(ctx, stack.ID); err != nil {
		t.Fatalf("DeleteComposeStack: %v", err)
	}
	revs, err := s.ListComposeRevisions(ctx, stack.ID, 50)
	if err != nil {
		t.Fatalf("ListComposeRevisions: %v", err)
	}
	vars, err := s.ListComposeEnvVars(ctx, stack.ID)
	if err != nil {
		t.Fatalf("ListComposeEnvVars: %v", err)
	}
	if len(revs) != 0 || len(vars) != 0 {
		t.Fatalf("left %d revisions and %d variables behind", len(revs), len(vars))
	}
}

// A variable is upserted, so setting the same key twice rotates it rather than
// failing or duplicating.
func TestStoreComposeEnvVarUpserts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "env-"+ids.Secret()[:8])

	for _, ct := range []string{"first", "second"} {
		if err := s.UpsertComposeEnvVar(ctx, stack.ID, domain.ComposeEnvVar{
			Key: "TOKEN", ValueCT: []byte(ct), ValueNonce: []byte("n"),
		}); err != nil {
			t.Fatalf("UpsertComposeEnvVar: %v", err)
		}
	}
	vars, err := s.ListComposeEnvVars(ctx, stack.ID)
	if err != nil {
		t.Fatalf("ListComposeEnvVars: %v", err)
	}
	if len(vars) != 1 || string(vars[0].ValueCT) != "second" {
		t.Fatalf("vars = %+v, want one, rotated", vars)
	}

	if err := s.DeleteComposeEnvVar(ctx, stack.ID, "TOKEN"); err != nil {
		t.Fatalf("DeleteComposeEnvVar: %v", err)
	}
	if vars, _ = s.ListComposeEnvVars(ctx, stack.ID); len(vars) != 0 {
		t.Fatalf("vars = %+v, want none", vars)
	}
}

// Desired state is assembled per server, so this query is what decides which
// stacks a node is told to run — and, by absence, which to bring down.
func TestStoreListComposeStacksByServer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	mine := seedStack(t, s, env.ID, srv.ID, "mine-"+ids.Secret()[:8])

	list, err := s.ListComposeStacksByServer(ctx, srv.ID)
	if err != nil {
		t.Fatalf("ListComposeStacksByServer: %v", err)
	}
	var found bool
	for _, st := range list {
		if st.ID == mine.ID {
			found = true
		}
		if st.ServerID != srv.ID {
			t.Fatalf("listed a stack from server %q", st.ServerID)
		}
	}
	if !found {
		t.Fatalf("the seeded stack is missing from %d rows", len(list))
	}
}

// A server cannot be deleted out from under the stack it is running.
func TestStoreServerWithAStackCannotBeDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	stack := seedStack(t, s, env.ID, srv.ID, "holds-"+ids.Secret()[:8])

	if err := s.DeleteServer(ctx, srv.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse while a stack runs on it", err)
	}
	if err := s.DeleteComposeStack(ctx, stack.ID); err != nil {
		t.Fatalf("DeleteComposeStack: %v", err)
	}
}
