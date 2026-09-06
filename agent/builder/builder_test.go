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
	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/registryauth"
)

type fakeEngine struct {
	builtImage      string
	builtDockerfile string
	buildAuth       string
	tagged          [][2]string
	pushed          []string
	pushAuth        string
	pushErr         error
}

func (f *fakeEngine) BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, registryConfig string, onLog func(line string)) error {
	f.builtImage = tag
	f.builtDockerfile = dockerfile
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

// ─── pack builds (pack-builds.md §5) ────────────────────────────────────────

// fakePack stands in for Nixpacks: what matters here is that the builder hands
// the checkout over and then builds what came back, not what the pack decides.
type fakePack struct {
	available bool
	called    int
	contextIn string
	tagIn     string
	writes    string // the file it pretends to generate
	// frontend, when set, makes it produce a BuildKit frontend plan rather
	// than a Dockerfile — the Railpack shape.
	frontend string
	err      error
}

func (p *fakePack) Available() bool { return p.available }

func (p *fakePack) Generate(_ context.Context, contextDir, imageTag string, onLog func(string)) (builder.Plan, error) {
	p.called++
	p.contextIn, p.tagIn = contextDir, imageTag
	if p.err != nil {
		return builder.Plan{}, p.err
	}
	onLog("pack: planned this repository")
	generated := filepath.Join(contextDir, p.writes)
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		return builder.Plan{}, err
	}
	if err := os.WriteFile(generated, []byte("FROM scratch\n"), 0o644); err != nil {
		return builder.Plan{}, err
	}
	if p.frontend != "" {
		return builder.Plan{PlanFile: p.writes, Frontend: p.frontend}, nil
	}
	return builder.Plan{Dockerfile: p.writes}, nil
}

// fakeBuildKit stands in for the second transport.
type fakeBuildKit struct {
	called int
	req    builder.BuildKitRequest
	err    error
}

func (b *fakeBuildKit) Build(_ context.Context, req builder.BuildKitRequest, onLog func(string)) error {
	b.called++
	b.req = req
	onLog("buildkit: built from the frontend plan")
	return b.err
}

// seedRepoWith is seedRepo with an extra file, so a repository can look like
// an application rather than a directory of files to serve.
func seedRepoWith(t *testing.T, extra string) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Skip("git not installed or failed to init")
	}
	if err := os.WriteFile(filepath.Join(dir, extra), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write %s: %v", extra, err)
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

// The pack writes a Dockerfile and the ORDINARY path builds it: one build path,
// so labels, credentials and log streaming all stay in one place.
func TestAPackBuildGoesThroughTheOrdinaryBuildPath(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	engine := &fakeEngine{}
	pack := &fakePack{available: true, writes: ".nixpacks/Dockerfile"}
	b := builder.NewBuilderWithPacks(engine, t.TempDir(), nil, map[string]builder.Pack{"nixpacks": pack})

	work := buildWorkFor(repo, commit)
	work.DockerfilePath = "Dockerfile"
	work.BuildKind = "auto"

	var logs []string
	if _, err := b.Build(context.Background(), work, func(l string) { logs = append(logs, l) }); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if pack.called != 1 {
		t.Fatalf("pack called %d times, want once", pack.called)
	}
	if engine.builtImage != "cypher/app1:rev1" {
		t.Fatalf("built image = %q, want the ordinary tag", engine.builtImage)
	}
	if engine.builtDockerfile != ".nixpacks/Dockerfile" {
		t.Fatalf("built dockerfile = %q, want the generated one", engine.builtDockerfile)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "pack: planned this repository") {
		t.Fatalf("logs = %v, want the pack's own output streamed", logs)
	}
}

// Without the pack installed, a repository that has only a manifest is a build
// we cannot infer — the pre-feature answer, not a silent pack build.
func TestWithoutThePackAManifestOnlyRepoStillFails(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	pack := &fakePack{available: false}
	b := builder.NewBuilderWithPacks(&fakeEngine{}, t.TempDir(), nil, map[string]builder.Pack{"nixpacks": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "auto"

	if _, err := b.Build(context.Background(), work, func(string) {}); err == nil {
		t.Fatal("Build succeeded with no pack and nothing to infer")
	}
	if pack.called != 0 {
		t.Fatal("the pack was invoked despite being unavailable")
	}
}

// A pack that cannot plan the repository fails the build with its own words.
func TestAFailedPackFailsTheBuild(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	pack := &fakePack{available: true, err: errors.New("no start command could be found")}
	b := builder.NewBuilderWithPacks(&fakeEngine{}, t.TempDir(), nil, map[string]builder.Pack{"nixpacks": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "nixpacks"

	_, err := b.Build(context.Background(), work, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "no start command") {
		t.Fatalf("err = %v, want the pack's own words", err)
	}
}

// ─── the second transport (pack-builds.md §2) ───────────────────────────────

// A pack whose output is a frontend plan takes the BuildKit path, and the
// ordinary engine build never runs — the classic endpoint cannot resolve a
// gateway frontend at all.
func TestAFrontendPlanTakesTheBuildKitTransport(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	engine := &fakeEngine{}
	bk := &fakeBuildKit{}
	pack := &fakePack{available: true, writes: "railpack-plan.json", frontend: "ghcr.io/railwayapp/railpack-frontend"}
	b := builder.NewBuilderWithPacks(engine, t.TempDir(), bk, map[string]builder.Pack{"railpack": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "railpack"

	var logs []string
	if _, err := b.Build(context.Background(), work, func(l string) { logs = append(logs, l) }); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bk.called != 1 {
		t.Fatalf("buildkit called %d times, want once", bk.called)
	}
	if engine.builtImage != "" {
		t.Fatalf("the classic endpoint also built %q — it cannot resolve a frontend", engine.builtImage)
	}
	if bk.req.Frontend != "ghcr.io/railwayapp/railpack-frontend" || bk.req.PlanFile != "railpack-plan.json" {
		t.Fatalf("request = %+v, want the frontend and its plan", bk.req)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "buildkit: built from the frontend plan") {
		t.Fatalf("logs = %v, want the transport's output streamed", logs)
	}
}

// Both transports produce the same tag with the same management labels: that is
// what makes having two acceptable, because nothing downstream can tell them
// apart.
func TestBothTransportsProduceTheSameTagAndLabels(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	bk := &fakeBuildKit{}
	pack := &fakePack{available: true, writes: "railpack-plan.json", frontend: "f"}
	b := builder.NewBuilderWithPacks(&fakeEngine{}, t.TempDir(), bk, map[string]builder.Pack{"railpack": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "railpack"
	if _, err := b.Build(context.Background(), work, func(string) {}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bk.req.Tag != "cypher/app1:rev1" {
		t.Fatalf("tag = %q, want the ordinary managed tag", bk.req.Tag)
	}
	for _, label := range []string{driver.LabelManaged, driver.LabelAppID, driver.LabelRevisionID} {
		if bk.req.Labels[label] == "" {
			t.Fatalf("labels = %v, want %s stamped — GC discovers its set by these", bk.req.Labels, label)
		}
	}
	if bk.req.Labels[driver.LabelRevisionID] != "rev1" {
		t.Fatalf("revision label = %q, want rev1", bk.req.Labels[driver.LabelRevisionID])
	}
}

// A frontend plan with no transport wired fails with the message that names
// what is missing, rather than silently falling back to a path that cannot
// build it.
func TestAFrontendPlanWithNoTransportFails(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	pack := &fakePack{available: true, writes: "railpack-plan.json", frontend: "f"}
	b := builder.NewBuilderWithPacks(&fakeEngine{}, t.TempDir(), nil, map[string]builder.Pack{"railpack": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "railpack"
	if _, err := b.Build(context.Background(), work, func(string) {}); err == nil {
		t.Fatal("Build succeeded with no BuildKit transport wired")
	}
}

func TestAFailedBuildKitBuildFailsTheBuild(t *testing.T) {
	repo, commit := seedRepoWith(t, "package.json")
	bk := &fakeBuildKit{err: errors.New("failed to solve: no start command")}
	pack := &fakePack{available: true, writes: "railpack-plan.json", frontend: "f"}
	b := builder.NewBuilderWithPacks(&fakeEngine{}, t.TempDir(), bk, map[string]builder.Pack{"railpack": pack})

	work := buildWorkFor(repo, commit)
	work.BuildKind = "railpack"
	_, err := b.Build(context.Background(), work, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "no start command") {
		t.Fatalf("err = %v, want the transport's own words", err)
	}
}
