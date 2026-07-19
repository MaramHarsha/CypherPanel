package builder_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MaramHarsha/cypherpanel/agent/builder"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

type fakeEngine struct {
	builtImage string
}

func (f *fakeEngine) BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, onLog func(line string)) error {
	f.builtImage = tag
	onLog("Docker daemon building...")
	_, _ = io.Copy(io.Discard, buildContext)
	return nil
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
