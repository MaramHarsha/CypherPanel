package builder_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/builder"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/registryauth"
)

type fakeEngine struct {
	builtImage string
	buildAuth  string
	tagged     [][2]string
	pushed     []string
	pushAuth   string
	pushErr    error
}

func (f *fakeEngine) BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, registryConfig string, onLog func(line string)) error {
	f.builtImage = tag
	f.buildAuth = registryConfig
	onLog("Docker daemon building...")
	_, _ = io.Copy(io.Discard, buildContext)
	return nil
}

func (f *fakeEngine) TagImage(_ context.Context, source, target string) error {
	f.tagged = append(f.tagged, [2]string{source, target})
	return nil
}

func (f *fakeEngine) PushImage(_ context.Context, ref, registryAuth string) error {
	f.pushed = append(f.pushed, ref)
	f.pushAuth = registryAuth
	return f.pushErr
}

func TestBuilder(t *testing.T) {
	// Create a dummy local git repo to clone from
	repoDir := t.TempDir()
	cmd := exec.Command("git", "init", repoDir)
	if err := cmd.Run(); err != nil {
		t.Skip("git not installed or failed to init")
	}
	if err := os.WriteFile(filepath.Join(repoDir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config email: %v", err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config name: %v", err)
	}

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Skip("git rev-parse failed")
	}
	commitSha := string(out[:len(out)-1])

	engine := &fakeEngine{}
	workDir := t.TempDir()
	b := builder.NewBuilder(engine, workDir)

	work := &agentv1.BuildWork{
		DeploymentId:   "dep1",
		AppId:          "app1",
		RepoUrl:        repoDir,
		CommitSha:      commitSha,
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
		Image:          "cypher/app1:rev1",
	}

	var logLines []string
	onLog := func(line string) {
		logLines = append(logLines, line)
	}

	if _, err := b.Build(context.Background(), work, onLog); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if engine.builtImage != "cypher/app1:rev1" {
		t.Fatalf("Expected built image cypher/app1:rev1, got %s", engine.builtImage)
	}

	if len(logLines) == 0 {
		t.Fatal("Expected log lines, got none")
	}
}

// ─── private registries (registries.md; ADR-008 path 3) ─────────────────────

// seedRepo makes a one-commit git repository a build can clone, and returns
// the commit. Skips rather than fails where git is unavailable, matching the
// existing builder test.
func seedRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skip("git not installed or failed to init")
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Skip("git rev-parse failed")
	}
	return dir, strings.TrimSpace(string(out))
}

func buildWorkFor(dir, commit string) *agentv1.BuildWork {
	return &agentv1.BuildWork{
		DeploymentId:   "dep1",
		AppId:          "app1",
		RepoUrl:        dir,
		CommitSha:      commit,
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
		Image:          "cypher/app1:rev1",
	}
}

// A private base image authenticates through X-Registry-Config on /build,
// which is a different header from the one a pull uses.
func TestBuildSendsTheSourceCredentialToTheDaemon(t *testing.T) {
	dir, commit := seedRepo(t)
	engine := &fakeEngine{}
	b := builder.NewBuilder(engine, t.TempDir())

	work := buildWorkFor(dir, commit)
	work.SourceAuth = &agentv1.RegistryAuth{ServerAddress: "ghcr.io", Username: "acme", Token: "ghp_base"}

	if _, err := b.Build(context.Background(), work, func(string) {}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	want, err := registryauth.EncodeConfig("ghcr.io", "acme", "ghp_base")
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if engine.buildAuth != want {
		t.Fatalf("build credentials = %q, want the encoded config", engine.buildAuth)
	}
}

func TestBuildSendsNoCredentialForAPublicBaseImage(t *testing.T) {
	dir, commit := seedRepo(t)
	engine := &fakeEngine{}
	b := builder.NewBuilder(engine, t.TempDir())

	if _, err := b.Build(context.Background(), buildWorkFor(dir, commit), func(string) {}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if engine.buildAuth != "" {
		t.Fatalf("build credentials = %q, want none", engine.buildAuth)
	}
	if len(engine.pushed) != 0 {
		t.Fatalf("pushed %v, want nothing without a push target", engine.pushed)
	}
}

// The push is an additional copy: the image is tagged with the target
// reference and that reference is pushed, leaving the local build in place for
// the rollout to run.
func TestBuildPushesWhenTheDeployAskedForIt(t *testing.T) {
	dir, commit := seedRepo(t)
	engine := &fakeEngine{}
	b := builder.NewBuilder(engine, t.TempDir())

	work := buildWorkFor(dir, commit)
	work.Push = &agentv1.RegistryPush{
		Image: "ghcr.io/acme/web:rev1",
		Auth:  &agentv1.RegistryAuth{ServerAddress: "ghcr.io", Username: "acme", Token: "ghp_push"},
	}

	var logs []string
	if _, err := b.Build(context.Background(), work, func(l string) { logs = append(logs, l) }); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(engine.tagged) != 1 || engine.tagged[0] != [2]string{"cypher/app1:rev1", "ghcr.io/acme/web:rev1"} {
		t.Fatalf("tagged = %v, want the local build tagged with the push reference", engine.tagged)
	}
	if len(engine.pushed) != 1 || engine.pushed[0] != "ghcr.io/acme/web:rev1" {
		t.Fatalf("pushed = %v", engine.pushed)
	}
	want, _ := registryauth.Encode("ghcr.io", "acme", "ghp_push")
	if engine.pushAuth != want {
		t.Fatalf("push credentials = %q, want the encoded credential", engine.pushAuth)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "Pushing ghcr.io/acme/web:rev1") {
		t.Fatalf("logs = %v, want the push narrated", logs)
	}
}

// Warning and rolling out anyway would report success for an image that is not
// where the operator was told it would be.
func TestAFailedPushFailsTheBuild(t *testing.T) {
	dir, commit := seedRepo(t)
	engine := &fakeEngine{pushErr: errors.New("denied: requested access to the resource is denied")}
	b := builder.NewBuilder(engine, t.TempDir())

	work := buildWorkFor(dir, commit)
	work.Push = &agentv1.RegistryPush{
		Image: "ghcr.io/acme/web:rev1",
		Auth:  &agentv1.RegistryAuth{ServerAddress: "ghcr.io", Username: "acme", Token: "ghp_push"},
	}

	_, err := b.Build(context.Background(), work, func(string) {})
	if err == nil {
		t.Fatal("Build succeeded despite a failed push")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want the registry's own words", err)
	}
	if strings.Contains(err.Error(), "ghp_push") {
		t.Fatalf("the credential reached the error: %v", err)
	}
}
