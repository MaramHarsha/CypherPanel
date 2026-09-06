package store

// Registry credentials against the real database (ENGINEERING rule 29). What
// the fakes elsewhere cannot prove lives here: the team-scoped unique index,
// the partial COALESCE update, and the ON DELETE RESTRICT that stops a
// credential disappearing from under a deploy.

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

func seedRegistry(t *testing.T, s *Store, name string, canPull, canPush bool) domain.Registry {
	t.Helper()
	reg, err := s.CreateRegistry(context.Background(), domain.Registry{
		ID: ids.New(ids.PrefixRegistry), TeamID: "tm_default", Name: name,
		URL: "ghcr.io", Username: "acme",
		TokenCT: []byte("ct"), TokenNonce: []byte("nonce"),
		CanPull: canPull, CanPush: canPush,
	})
	if err != nil {
		t.Fatalf("CreateRegistry: %v", err)
	}
	return reg
}

func TestStoreRegistryRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	reg := seedRegistry(t, s, "ghcr-"+ids.Secret()[:8], true, true)
	got, err := s.GetRegistry(ctx, reg.ID)
	if err != nil {
		t.Fatalf("GetRegistry: %v", err)
	}
	if got.URL != "ghcr.io" || got.Username != "acme" || string(got.TokenCT) != "ct" {
		t.Fatalf("registry = %+v", got)
	}
	if !got.CanPull || !got.CanPush || got.LastTestOK {
		t.Fatalf("capabilities/test state = %+v, want pull+push and no test recorded yet", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at was not defaulted")
	}
}

func TestStoreRegistryMissingIsNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.GetRegistry(context.Background(), "reg_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Two registries in one team may not share a name — that is what an operator
// picks them by.
func TestStoreRegistryNameIsUniquePerTeam(t *testing.T) {
	s := testStore(t)
	name := "dup-" + ids.Secret()[:8]
	seedRegistry(t, s, name, true, false)

	_, err := s.CreateRegistry(context.Background(), domain.Registry{
		ID: ids.New(ids.PrefixRegistry), TeamID: "tm_default", Name: name,
		URL: "ghcr.io", TokenCT: []byte("ct"), TokenNonce: []byte("n"), CanPull: true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict on a duplicate name", err)
	}
}

// A partial update leaves untouched columns alone, so rotating a token does
// not mean re-sending the URL — and renaming does not mean re-sending the
// token.
func TestStoreRegistryUpdateIsPartial(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reg := seedRegistry(t, s, "part-"+ids.Secret()[:8], true, false)

	name := "renamed-" + ids.Secret()[:8]
	renamed, err := s.UpdateRegistry(ctx, reg.ID, UpdateRegistryFields{Name: &name})
	if err != nil {
		t.Fatalf("UpdateRegistry: %v", err)
	}
	if renamed.Name != name || renamed.URL != "ghcr.io" || string(renamed.TokenCT) != "ct" {
		t.Fatalf("after rename = %+v, want only the name changed", renamed)
	}

	rotated, err := s.UpdateRegistry(ctx, reg.ID, UpdateRegistryFields{
		TokenCT: []byte("ct2"), TokenNonce: []byte("nonce2"),
	})
	if err != nil {
		t.Fatalf("UpdateRegistry: %v", err)
	}
	if string(rotated.TokenCT) != "ct2" || rotated.Name != name || rotated.URL != "ghcr.io" {
		t.Fatalf("after rotation = %+v, want only the credential changed", rotated)
	}
}

func TestStoreRegistryRecordsTheLastTest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reg := seedRegistry(t, s, "test-"+ids.Secret()[:8], true, false)

	got, err := s.RecordRegistryTest(ctx, reg.ID, true, "Authenticated to ghcr.io.")
	if err != nil {
		t.Fatalf("RecordRegistryTest: %v", err)
	}
	if !got.LastTestOK || got.LastTestAt == nil || got.LastTestDetail == "" {
		t.Fatalf("after test = %+v, want the outcome and its timestamp stored", got)
	}
}

// Listing filters to the caller's teams rather than refusing, so a caller in
// no teams gets an empty list — and never another team's rows.
func TestStoreListRegistriesByTeams(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	mine := seedRegistry(t, s, "mine-"+ids.Secret()[:8], true, false)

	list, err := s.ListRegistriesByTeams(ctx, []string{"tm_default"})
	if err != nil {
		t.Fatalf("ListRegistriesByTeams: %v", err)
	}
	var found bool
	for _, r := range list {
		if r.ID == mine.ID {
			found = true
		}
		if r.TeamID != "tm_default" {
			t.Fatalf("listed a registry from team %q", r.TeamID)
		}
	}
	if !found {
		t.Fatalf("the seeded registry is missing from %d rows", len(list))
	}

	empty, err := s.ListRegistriesByTeams(ctx, nil)
	if err != nil {
		t.Fatalf("ListRegistriesByTeams(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("a caller in no teams got %d rows", len(empty))
	}
}

// Deleting a registry that applications depend on would break their next
// deploy at the moment nobody is looking, so the foreign key holds it.
func TestStoreRegistryDeleteIsRestrictedByApplications(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)
	reg := seedRegistry(t, s, "used-"+ids.Secret()[:8], true, true)

	app, err := s.CreateApplicationWithEnv(ctx, domain.Application{
		ID:            ids.New(ids.PrefixApplication),
		EnvironmentID: env.ID,
		Name:          "puller",
		Source: domain.AppSource{
			Kind: "image", Image: "ghcr.io/acme/web:1", RegistryID: &reg.ID,
		},
		Build: domain.AppBuild{
			Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: ".",
			PushRegistryID: &reg.ID, PushRepository: "acme/web",
		},
		Runtime:            domain.AppRuntime{ServerID: srv.ID, Port: 8080, Replicas: 1},
		Route:              domain.AppRoute{Domain: "puller.example.com", HTTPS: true},
		Health:             domain.AppHealth{Path: "/", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
		WebhookID:          ids.New(ids.PrefixWebhook),
		WebhookSecretCT:    []byte("ct"),
		WebhookSecretNonce: []byte("nonce"),
	}, nil)
	if err != nil {
		t.Fatalf("CreateApplicationWithEnv: %v", err)
	}

	// The reference survives the round trip: a rollback of an image revision
	// still has to know which credential to pull with.
	read, err := s.GetApplication(ctx, app.ID)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	if read.Source.RegistryID == nil || *read.Source.RegistryID != reg.ID {
		t.Fatalf("source registry = %v, want %s", read.Source.RegistryID, reg.ID)
	}
	if read.Build.PushRegistryID == nil || read.Build.PushRepository != "acme/web" {
		t.Fatalf("build push = %v / %q", read.Build.PushRegistryID, read.Build.PushRepository)
	}

	// The refusal names what is holding it, rather than counting.
	uses, err := s.ApplicationsUsingRegistry(ctx, reg.ID)
	if err != nil {
		t.Fatalf("ApplicationsUsingRegistry: %v", err)
	}
	if len(uses) != 1 || uses[0].ApplicationName != "puller" || !uses[0].Pulls || !uses[0].Pushes {
		t.Fatalf("uses = %+v, want the one application named as both", uses)
	}
	if uses[0].EnvironmentName == "" || uses[0].ProjectName == "" {
		t.Fatalf("uses = %+v, want the ownership chain named", uses)
	}

	if err := s.DeleteRegistry(ctx, reg.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse while an application references it", err)
	}

	// Detach, and the delete goes through.
	read.Source.RegistryID = nil
	read.Build.PushRegistryID = nil
	read.Build.PushRepository = ""
	if _, err := s.UpdateApplicationConfig(ctx, read); err != nil {
		t.Fatalf("UpdateApplicationConfig: %v", err)
	}
	if err := s.DeleteRegistry(ctx, reg.ID); err != nil {
		t.Fatalf("DeleteRegistry after detaching: %v", err)
	}
	if _, err := s.GetRegistry(ctx, reg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want the registry gone", err)
	}
}
