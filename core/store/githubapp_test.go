package store

// The GitHub App push match, against a real PostgreSQL — because the whole
// question is what a Postgres regexp does, and a fake would be asserting my
// reading of the docs rather than the database's behaviour.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// A push names `owner/repo`; an application stores what git clones. Those are
// different strings for the same repository, and comparing them literally is
// how every App push became a silent no-op — 202 with `{"deployments": 0}`,
// a green tick in GitHub's Recent Deliveries, and nothing deployed.
func TestPushMatchesEveryCloneURLShapeForTheSameRepository(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	srv, err := s.CreateServerWithToken(ctx, ids.New(ids.PrefixServer), "box", ids.New(ids.PrefixJoinToken), []byte(ids.Secret()), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateServerWithToken: %v", err)
	}
	_, env, err := s.CreateProjectWithEnvironment(ctx, ids.New(ids.PrefixProject), "proj", "tm_default", projSlug("push"), ids.New(ids.PrefixEnvironment), "production")
	if err != nil {
		t.Fatalf("CreateProjectWithEnvironment: %v", err)
	}

	// One unique owner/repo for this run, so the assertions can be exact
	// counts rather than "at least one" against a shared database.
	owner := "acme-" + ids.Secret()[:8]
	full := owner + "/web"

	shouldMatch := []string{
		"https://github.com/" + full,
		"https://github.com/" + full + ".git",
		"https://github.com/" + full + "/",
		"http://github.com/" + full,
		"ssh://git@github.com/" + full + ".git",
		"git@github.com:" + full + ".git",
		"git@github.com:" + full,
		full, // a legacy row written before repository shapes were validated
	}
	shouldNotMatch := []string{
		"https://github.com/" + owner + "/website", // a different repository
		"https://github.com/other/web",             // a different owner
		"https://gitlab.com/grp/sub/" + full,       // a nested path that merely ENDS the same
	}

	seed := func(repo, branch string) string {
		t.Helper()
		app, cerr := s.CreateApplicationWithEnv(ctx, domain.Application{
			ID:                 ids.New(ids.PrefixApplication),
			EnvironmentID:      env.ID,
			Name:               "app-" + ids.Secret()[:8],
			Source:             domain.AppSource{Kind: "github", Repo: repo, Branch: branch},
			Build:              domain.AppBuild{Kind: "dockerfile", DockerfilePath: "./Dockerfile", Context: "."},
			Runtime:            domain.AppRuntime{ServerID: srv.ID, Port: 8080, Replicas: 1},
			Health:             domain.AppHealth{Path: "/", IntervalSeconds: 10, TimeoutSeconds: 5, Retries: 3},
			WebhookID:          ids.New(ids.PrefixWebhook),
			WebhookSecretCT:    []byte("ct"),
			WebhookSecretNonce: []byte("nonce"),
		}, nil)
		if cerr != nil {
			t.Fatalf("CreateApplicationWithEnv(%q): %v", repo, cerr)
		}
		return app.ID
	}

	want := map[string]string{}
	for _, repo := range shouldMatch {
		want[seed(repo, "main")] = repo
	}
	for _, repo := range shouldNotMatch {
		seed(repo, "main")
	}
	// The same repository on a branch nobody pushed to must not deploy either:
	// the branch is half the match and a regression could drop it.
	seed("https://github.com/"+full, "release")

	got, err := s.ListApplicationsByRepo(ctx, full, "main")
	if err != nil {
		t.Fatalf("ListApplicationsByRepo: %v", err)
	}
	found := map[string]bool{}
	for _, a := range got {
		found[a.ID] = true
	}
	for id, repo := range want {
		if !found[id] {
			t.Errorf("a push to %q did not match the application whose repo is %q — that push would deploy nothing and report success", full, repo)
		}
	}
	if len(got) != len(want) {
		var extra []string
		for _, a := range got {
			if _, ok := want[a.ID]; !ok {
				extra = append(extra, a.Source.Repo)
			}
		}
		t.Errorf("matched %d applications, want %d; unexpected: %v", len(got), len(want), extra)
	}

	// GitHub treats owner and repository names case-insensitively, so a push
	// that differs only in case is the same repository.
	upper, err := s.ListApplicationsByRepo(ctx, strings.ToUpper(full), "main")
	if err != nil {
		t.Fatalf("ListApplicationsByRepo (upper): %v", err)
	}
	if len(upper) != len(want) {
		t.Errorf("a case-different push matched %d applications, want %d", len(upper), len(want))
	}
}
