package scheduler

// Private-registry credentials on the wire (registries.md §5).
//
// The property under test is where the credential appears and where it must
// not: on the work item that needs it, assembled at publish time, and nowhere
// else — never in desired state for an image that arrived some other way, and
// never resolved at all for the applications that name no registry.

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/registries"
)

// fakeCredentials is the plane-side registry lookup. It counts calls so
// "resolved per work item, never cached" is provable.
type fakeCredentials struct {
	byID  map[string]registries.Credential
	calls int
	err   error
}

func (f *fakeCredentials) Credential(_ context.Context, id string) (registries.Credential, error) {
	f.calls++
	if f.err != nil {
		return registries.Credential{}, f.err
	}
	c, ok := f.byID[id]
	if !ok {
		return registries.Credential{}, errors.New("store: not found")
	}
	return c, nil
}

func newCredentials() *fakeCredentials {
	return &fakeCredentials{byID: map[string]registries.Credential{
		"reg_pull": {URL: "ghcr.io", Username: "acme", Token: "ghp_pull"},
		"reg_push": {URL: "ghcr.io/acme", Username: "acme", Token: "ghp_push"},
	}}
}

func regPtr(s string) *string { return &s }

// revisionOf snapshots the application exactly as a deploy would, so whether
// the spec pulls is decided by the source kind rather than asserted by the
// test.
func revisionOf(t *testing.T, fs *fakeStore, app domain.Application, revID, image string) domain.Revision {
	t.Helper()
	snap, err := snapshotOf(app)
	if err != nil {
		t.Fatalf("snapshotOf: %v", err)
	}
	rev := domain.Revision{ID: revID, ApplicationID: app.ID, Image: image, ConfigSnapshot: snap}
	fs.revisions[revID] = rev
	return rev
}

func TestPullSpecCarriesTheCredential(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghcr.io/acme/web:1", RegistryID: regPtr("reg_pull")}
	fs.apps["app_1"] = app
	rev := revisionOf(t, fs, app, "rev_1", "ghcr.io/acme/web:1")

	creds := newCredentials()
	s := newScheduler(fs, fb)
	s.SetRegistries(creds)

	spec, err := s.buildSpec(context.Background(), app, rev, envStrict)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	auth := spec.GetRegistryAuth()
	if auth.GetServerAddress() != "ghcr.io" || auth.GetUsername() != "acme" || auth.GetToken() != "ghp_pull" {
		t.Fatalf("registry_auth = %+v, want the resolved credential", auth)
	}
}

// A built or relayed image has nothing to authenticate to, so putting a token
// on that spec would be a secret travelling for no reason.
func TestANonPullingSpecCarriesNoCredential(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source.RegistryID = regPtr("reg_pull")
	fs.apps["app_1"] = app
	rev := revisionOf(t, fs, app, "rev_1", "cypher/app_1:rev_1")

	creds := newCredentials()
	s := newScheduler(fs, fb)
	s.SetRegistries(creds)

	spec, err := s.buildSpec(context.Background(), app, rev, envStrict)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.GetRegistryAuth() != nil {
		t.Fatalf("registry_auth = %+v, want none on a locally-built image", spec.GetRegistryAuth())
	}
	if creds.calls != 0 {
		t.Fatalf("credential lookups = %d, want none", creds.calls)
	}
}

// A named registry that cannot be resolved is an error, not an anonymous
// attempt: falling back would fail later at the daemon with a "manifest
// unknown" nobody can act on.
func TestAnUnresolvableCredentialFailsTheSpec(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghcr.io/acme/web:1", RegistryID: regPtr("reg_gone")}
	fs.apps["app_1"] = app
	rev := revisionOf(t, fs, app, "rev_1", "ghcr.io/acme/web:1")

	s := newScheduler(fs, fb)
	s.SetRegistries(newCredentials())

	if _, err := s.buildSpec(context.Background(), app, rev, envStrict); err == nil {
		t.Fatal("buildSpec succeeded, want a failure rather than an anonymous pull")
	}
}

// The same holds when the panel has no registry service at all: an application
// naming one must not deploy as though it had not.
func TestANamedRegistryWithNoServiceWiredFails(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Source = domain.AppSource{Kind: "image", Image: "ghcr.io/acme/web:1", RegistryID: regPtr("reg_pull")}
	fs.apps["app_1"] = app
	rev := revisionOf(t, fs, app, "rev_1", "ghcr.io/acme/web:1")

	s := newScheduler(fs, fb) // no SetRegistries

	if _, err := s.buildSpec(context.Background(), app, rev, envStrict); err == nil {
		t.Fatal("buildSpec succeeded with no registry service wired")
	}
}

func TestBuildWorkCarriesTheSourceCredentialAndThePushTarget(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Name = "My API"
	app.Source.RegistryID = regPtr("reg_pull")
	app.Build.PushRegistryID = regPtr("reg_push")
	fs.apps["app_1"] = app
	rev := domain.Revision{ID: "rev_1", ApplicationID: "app_1", SourceCommit: "abc123"}

	s := newScheduler(fs, fb)
	s.SetRegistries(newCredentials())

	work, err := s.buildWork(context.Background(), domain.Deployment{ID: "dep_1", ApplicationID: "app_1"}, app, rev)
	if err != nil {
		t.Fatalf("buildWork: %v", err)
	}
	if work.GetSourceAuth().GetToken() != "ghp_pull" {
		t.Fatalf("source_auth = %+v, want the base-image credential", work.GetSourceAuth())
	}
	// The registry URL already carries a namespace, the repository defaults to
	// the application's name reduced to a legal path, and the tag is the
	// revision id — what a rollback names.
	if got := work.GetPush().GetImage(); got != "ghcr.io/acme/my-api:rev_1" {
		t.Fatalf("push image = %q", got)
	}
	if work.GetPush().GetAuth().GetToken() != "ghp_push" {
		t.Fatalf("push auth = %+v", work.GetPush().GetAuth())
	}
}

func TestBuildWorkUsesTheConfiguredRepository(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	app.Build.PushRegistryID = regPtr("reg_push")
	app.Build.PushRepository = "platform/web"
	fs.apps["app_1"] = app

	s := newScheduler(fs, fb)
	s.SetRegistries(newCredentials())

	work, err := s.buildWork(context.Background(), domain.Deployment{ID: "dep_1"}, app,
		domain.Revision{ID: "rev_9", ApplicationID: "app_1"})
	if err != nil {
		t.Fatalf("buildWork: %v", err)
	}
	if got := work.GetPush().GetImage(); got != "ghcr.io/acme/platform/web:rev_9" {
		t.Fatalf("push image = %q", got)
	}
}

func TestBuildWorkCarriesNothingForAnOrdinaryApplication(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	app := fs.addApp("app_1", "srv_1")
	creds := newCredentials()

	s := newScheduler(fs, fb)
	s.SetRegistries(creds)

	work, err := s.buildWork(context.Background(), domain.Deployment{ID: "dep_1"}, app,
		domain.Revision{ID: "rev_1", ApplicationID: "app_1"})
	if err != nil {
		t.Fatalf("buildWork: %v", err)
	}
	if work.GetSourceAuth() != nil || work.GetPush() != nil {
		t.Fatalf("work carries %+v / %+v, want neither", work.GetSourceAuth(), work.GetPush())
	}
	if creds.calls != 0 {
		t.Fatalf("credential lookups = %d, want none", creds.calls)
	}
}

// An application named entirely in punctuation still has to push somewhere a
// registry will accept.
func TestPushRepositoryFallsBackToTheApplicationID(t *testing.T) {
	app := domain.Application{ID: "app_2m9", Name: "!!!"}
	if got := pushRepository(app); got != "app_2m9" {
		t.Fatalf("pushRepository = %q, want the id", got)
	}
}

func TestPushRepositoryReducesTheNameToALegalPath(t *testing.T) {
	cases := map[string]string{
		"My API":      "my-api",
		"web":         "web",
		"  Spaced  ":  "spaced",
		"a__b":        "a-b",
		"Ünicode API": "nicode-api",
	}
	for name, want := range cases {
		if got := pushRepository(domain.Application{ID: "app_1", Name: name}); got != want {
			t.Errorf("pushRepository(%q) = %q, want %q", name, got, want)
		}
	}
}
