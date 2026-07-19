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

	"github.com/MaramHarsha/cypherpanel/agent/driver"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// gitEnv hardens git invocations: never prompt for credentials (a private
// repo without a key fails fast instead of hanging), and allow only the
// transports a clone legitimately needs. The critical exclusion is the `ext`
// transport, whose URL form (`ext::sh -c …`) runs an arbitrary command — the
// remote-code-execution path a crafted RepoUrl would otherwise open
// (threat-model: malicious source config). `file` stays allowed: it reads a
// local path but cannot execute commands.
var gitEnv = append(os.Environ(),
	"GIT_TERMINAL_PROMPT=0",
	"GIT_ALLOW_PROTOCOL=https:http:git:ssh:file",
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

	repoURL := work.RepoUrl
	displayURL := repoURL
	if parsed, err := url.Parse(repoURL); err == nil {
		displayURL = parsed.Redacted()
	}

	cloneEnv := append([]string(nil), gitEnv...) // copy base env

	if work.DeployKeyPem != "" {
		// Write the deploy key to a temporary file.
		keyFile := filepath.Join(b.workDir, ".deploy-key-"+work.DeploymentId)
		if err := os.WriteFile(keyFile, []byte(work.DeployKeyPem), 0600); err != nil {
			return "", fmt.Errorf("writing deploy key: %w", err)
		}
		defer func() { _ = os.Remove(keyFile) }()

		// Force SSH transport instead of asking for credentials if it's an HTTPS github URL.
		if strings.HasPrefix(repoURL, "https://github.com/") {
			repoURL = strings.Replace(repoURL, "https://github.com/", "git@github.com:", 1)
			if !strings.HasSuffix(repoURL, ".git") {
				repoURL += ".git"
			}
		}

		// Inject SSH command to use the key and ignore unknown hosts (we don't
		// maintain a known_hosts file for the agent).
		sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null", keyFile)
		cloneEnv = append(cloneEnv, "GIT_SSH_COMMAND="+sshCmd)
		onLog(fmt.Sprintf("Cloning private repository using deploy key..."))
	} else {
		onLog(fmt.Sprintf("Cloning repository %s...", displayURL))
	}

	cmd := exec.CommandContext(ctx, "git", "clone", repoURL, buildDir)
	cmd.Env = cloneEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		onLog(string(out))
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	// Check out the requested ref (a commit SHA, or a branch name when the
	// deploy named no explicit commit). Empty means "whatever the clone left
	// on HEAD" — the default branch — so there is nothing to check out.
	if ref := strings.TrimSpace(work.CommitSha); ref != "" {
		cmd = exec.CommandContext(ctx, "git", "checkout", "--detach", ref)
		cmd.Dir = buildDir
		cmd.Env = gitEnv
		out, err = cmd.CombinedOutput()
		if err != nil {
			onLog(string(out))
			return "", fmt.Errorf("git checkout failed: %w", err)
		}
	}

	cmd = exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = buildDir
	cmd.Env = gitEnv
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
	// The revision id is the image tag's ":<rev>" suffix (cypher/<app>:<rev>).
	// LastIndex, not Split, so a registry host:port in the ref can't confuse it.
	revID := ""
	if i := strings.LastIndex(work.Image, ":"); i >= 0 {
		revID = work.Image[i+1:]
	}
	// Stamp the same management labels the driver discovers its set by, from
	// the shared constants — a build whose labels drift would leave images the
	// driver's GC can never reclaim (reconciler-development skill).
	labels := map[string]string{
		driver.LabelManaged:    "docker",
		driver.LabelAppID:      work.AppId,
		driver.LabelRevisionID: revID,
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
