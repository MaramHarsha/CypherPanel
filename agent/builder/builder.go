package builder

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/url"
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

func (b *Builder) Build(ctx context.Context, work *agentv1.BuildWork, onLog func(string)) (string, error) {
	if work.DeploymentId == "" || filepath.IsAbs(work.DeploymentId) || strings.Contains(work.DeploymentId, "..") {
		return "", fmt.Errorf("invalid deployment ID")
	}
	if filepath.IsAbs(work.BuildContext) || strings.Contains(work.BuildContext, "..") {
		return "", fmt.Errorf("invalid build context path")
	}

	buildDir := filepath.Join(b.workDir, work.DeploymentId)
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return "", fmt.Errorf("creating build directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()

	displayURL := work.RepoUrl
	if parsed, err := url.Parse(work.RepoUrl); err == nil {
		displayURL = parsed.Redacted()
	}
	onLog(fmt.Sprintf("Cloning repository %s at %s...", displayURL, work.CommitSha))

	cmd := exec.CommandContext(ctx, "git", "clone", work.RepoUrl, buildDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	cmd = exec.CommandContext(ctx, "git", "checkout", work.CommitSha)
	cmd.Dir = buildDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return "", fmt.Errorf("git checkout failed: %w", err)
	}

	cmd = exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = buildDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	resolvedCommitSha := strings.TrimSpace(string(out))

	onLog("Repository cloned successfully. Preparing build context...")

	contextDir := buildDir
	if work.BuildContext != "" && work.BuildContext != "." {
		contextDir = filepath.Join(buildDir, work.BuildContext)
	}

	tarPipeR, tarPipeW := io.Pipe()

	ignoreRules := parseDockerIgnore(contextDir)

	go func() {
		var walkErr error
		defer func() {
			tarPipeW.CloseWithError(walkErr)
		}()
		tw := tar.NewWriter(tarPipeW)
		defer func() {
			if err := tw.Close(); err != nil && walkErr == nil {
				walkErr = err
			}
		}()

		walkErr = filepath.Walk(contextDir, func(path string, info os.FileInfo, err error) error {
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
			if relPath == ".git" || strings.HasPrefix(relPath, ".git"+string(filepath.Separator)) {
				return nil
			}
			if isIgnored(relPath, ignoreRules) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}
			header, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relPath)
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				_, copyErr := io.Copy(tw, f)
				_ = f.Close()
				if copyErr != nil {
					return copyErr
				}
			}
			return nil
		})
	}()

	onLog(fmt.Sprintf("Building image %s...", work.Image))
	parts := strings.Split(work.Image, ":")
	revID := ""
	if len(parts) > 1 {
		revID = parts[1]
	}
	labels := map[string]string{
		"cypherpanel.managed":     "docker",
		"cypherpanel.app-id":      work.AppId,
		"cypherpanel.revision-id": revID,
	}

	defer func() { _ = tarPipeR.Close() }()
	if err := b.engine.BuildImage(ctx, tarPipeR, work.Image, work.DockerfilePath, labels, onLog); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	onLog("Build completed successfully.")
	return resolvedCommitSha, nil
}

func parseDockerIgnore(contextDir string) []string {
	var rules []string
	b, err := os.ReadFile(filepath.Join(contextDir, ".dockerignore"))
	if err != nil {
		return rules
	}
	lines := strings.Split(string(b), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, filepath.Clean(line))
	}
	return rules
}

func isIgnored(relPath string, rules []string) bool {
	ignored := false
	for _, rule := range rules {
		invert := strings.HasPrefix(rule, "!")
		if invert {
			rule = rule[1:]
		}
		match, _ := filepath.Match(rule, relPath)
		if !match && strings.HasPrefix(relPath, rule+string(filepath.Separator)) {
			match = true
		}
		if match {
			ignored = !invert
		}
	}
	return ignored
}
