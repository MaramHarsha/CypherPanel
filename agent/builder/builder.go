package builder

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// EngineClient is the interface needed from the docker engine client to build images.
type EngineClient interface {
	BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, onLog func(line string)) error
}

type Builder struct {
	engine  EngineClient
	workDir string
}

func NewBuilder(engine EngineClient, workDir string) *Builder {
	return &Builder{
		engine:  engine,
		workDir: workDir,
	}
}

func (b *Builder) Build(ctx context.Context, work *agentv1.BuildWork, onLog func(string)) error {
	buildDir := filepath.Join(b.workDir, work.DeploymentId)
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("creating build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	onLog(fmt.Sprintf("Cloning repository %s at %s...", work.RepoUrl, work.CommitSha))

	cmd := exec.CommandContext(ctx, "git", "clone", work.RepoUrl, buildDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return fmt.Errorf("git clone failed: %w", err)
	}

	cmd = exec.CommandContext(ctx, "git", "checkout", work.CommitSha)
	cmd.Dir = buildDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return fmt.Errorf("git checkout failed: %w", err)
	}

	onLog("Repository cloned successfully. Preparing build context...")

	contextDir := buildDir
	if work.BuildContext != "" && work.BuildContext != "." {
		contextDir = filepath.Join(buildDir, work.BuildContext)
	}

	tarPipeR, tarPipeW := io.Pipe()

	go func() {
		defer tarPipeW.Close()
		tw := tar.NewWriter(tarPipeW)
		defer tw.Close()

		filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return err
			}
			relPath, err := filepath.Rel(contextDir, path)
			if err != nil {
				return err
			}
			if relPath == "." {
				return nil
			}
			if strings.HasPrefix(relPath, ".git/") || relPath == ".git" {
				return nil
			}
			header, err := tar.FileInfoHeader(info, info.Name())
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relPath)
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			if !info.IsDir() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, _ = io.Copy(tw, f)
			}
			return nil
		})
	}()

	onLog(fmt.Sprintf("Building image %s...", work.Image))
	labels := map[string]string{
		"cypherpanel.managed": "docker",
		"cypherpanel.app.id":  work.AppId,
	}

	if err := b.engine.BuildImage(ctx, tarPipeR, work.Image, work.DockerfilePath, labels, onLog); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	onLog("Build completed successfully.")
	return nil
}
