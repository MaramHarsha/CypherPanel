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
	"github.com/MaramHarsha/cypherpanel/pkg/registryauth"
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

// EngineClient is the interface needed from the docker engine client to build
// images — and, when the deploy asked for it, to push the result somewhere the
// operator already runs a registry (ADR-008 path 3).
type EngineClient interface {
	BuildImage(ctx context.Context, buildContext io.Reader, tag, dockerfile string, labels map[string]string, registryConfig string, onLog func(line string)) error
	TagImage(ctx context.Context, source, target string) error
	PushImage(ctx context.Context, ref, registryAuth string) error
}

type Builder struct {
	engine  EngineClient
	workDir string
	// pack turns a repository with no Dockerfile into one (pack-builds.md).
	// Never nil: NewBuilder installs the real pack, and its Available() is what
	// decides whether detection may reach for it.
	pack Pack
}

func NewBuilder(engine EngineClient, workDir string) *Builder {
	return NewBuilderWithPack(engine, workDir, Nixpacks{})
}

// NewBuilderWithPack is NewBuilder with the build pack supplied, so the
// builder's routing is testable without the binary installed.
func NewBuilderWithPack(engine EngineClient, workDir string, pack Pack) *Builder {
	return &Builder{engine: engine, workDir: workDir, pack: pack}
}

// sshCloneURL rewrites an https://github.com/ repository URL to its SSH form
// so the deploy key — an SSH credential — can authenticate the clone
// (deploy-key-private-repos.md §4). Every other URL passes through unchanged:
// an SSH URL already works, and other hosts' HTTPS forms are the operator's
// to configure as SSH remotes.
func sshCloneURL(repoURL string) string {
	const httpsGitHub = "https://github.com/"
	if !strings.HasPrefix(repoURL, httpsGitHub) {
		return repoURL
	}
	out := "git@github.com:" + strings.TrimPrefix(repoURL, httpsGitHub)
	if !strings.HasSuffix(out, ".git") {
		out += ".git"
	}
	return out
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

	cloneEnv := gitEnv
	if work.DeployKeyPem != "" {
		// The key lives beside (not inside) the clone target — git needs an
		// empty destination — under 0600, and is removed on every exit path
		// (deploy-key-private-repos.md §4). The PEM itself is never logged
		// (ENGINEERING rule 20).
		keyFile := filepath.Join(b.workDir, ".deploy-key-"+work.DeploymentId)
		if err := os.WriteFile(keyFile, []byte(work.DeployKeyPem), 0o600); err != nil {
			return "", fmt.Errorf("writing deploy key: %w", err)
		}
		defer func() { _ = os.Remove(keyFile) }()

		repoURL = sshCloneURL(repoURL)

		// accept-new with no persistent known_hosts: the agent keeps no
		// host-key state, so pinning is trust-on-first-use per clone
		// (deploy-key-private-repos.md §4).
		cloneEnv = append(append([]string(nil), gitEnv...),
			`GIT_SSH_COMMAND=ssh -i "`+keyFile+`" -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null`)
		onLog(fmt.Sprintf("Cloning private repository %s at %s using deploy key...", displayURL, work.CommitSha))
	} else {
		onLog(fmt.Sprintf("Cloning repository %s at %s...", displayURL, work.CommitSha))
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

	// Decide how to build before tarring: a static site has its Dockerfile
	// synthesized into the context, so it has to exist before the walk.
	dockerfilePath := work.DockerfilePath
	kind, err := resolveBuildKind(work.BuildKind, contextDir, dockerfilePath, b.pack.Available())
	if err != nil {
		onLog(err.Error())
		return "", err
	}
	if kind == kindNixpacks {
		// The pack writes a Dockerfile; from here the ordinary path takes over,
		// so there is one place labels are stamped, one place a private base
		// image's credential applies, and one place logs stream from.
		generated, gerr := b.pack.Generate(ctx, contextDir, work.Image, onLog)
		if gerr != nil {
			return "", gerr
		}
		dockerfilePath = generated
	}
	if kind == kindStatic {
		onLog("No Dockerfile found — detected a static site; building an nginx image to serve it.")
		generated, wErr := writeStaticBuild(contextDir, work.RuntimePort)
		if wErr != nil {
			return "", wErr
		}
		dockerfilePath = generated
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

	// A private base image authenticates through the same credential the app's
	// image source would use. The plane sends it per work item; nothing is
	// written to disk, so there is no daemon-level `docker login` state on the
	// builder for a later build to inherit by accident.
	buildAuth, err := registryauth.EncodeConfig(
		work.GetSourceAuth().GetServerAddress(),
		work.GetSourceAuth().GetUsername(),
		work.GetSourceAuth().GetToken(),
	)
	if err != nil {
		return "", err
	}

	defer func() { _ = tarPipeR.Close() }()
	if err := b.engine.BuildImage(ctx, tarPipeR, work.Image, dockerfilePath, labels, buildAuth, onLog); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	if err := b.push(ctx, work, onLog); err != nil {
		return "", err
	}

	onLog("Build completed successfully.")
	return resolvedCommitSha, nil
}

// push sends the built image to the registry the application asked for, in
// addition to leaving it in the local daemon (ADR-008 path 3).
//
// A failure here fails the deployment. The alternative — warn and roll out
// anyway — would report success for an image that is not where the operator
// was told it would be, and the next thing to look for it (a rollback on
// another host, something outside the panel) finds nothing.
//
// The rollout itself never reads this copy: it runs the local build or the
// relayed one, so the ADR-008 contract that no registry is required is intact
// even for an application that has configured one.
func (b *Builder) push(ctx context.Context, work *agentv1.BuildWork, onLog func(string)) error {
	target := work.GetPush()
	if target.GetImage() == "" {
		return nil
	}
	auth, err := registryauth.Encode(
		target.GetAuth().GetServerAddress(),
		target.GetAuth().GetUsername(),
		target.GetAuth().GetToken(),
	)
	if err != nil {
		return err
	}
	onLog(fmt.Sprintf("Pushing %s...", target.GetImage()))
	if err := b.engine.TagImage(ctx, work.Image, target.GetImage()); err != nil {
		return fmt.Errorf("tagging for push: %w", err)
	}
	if err := b.engine.PushImage(ctx, target.GetImage(), auth); err != nil {
		// The registry's own words; the credential is not in them.
		return fmt.Errorf("pushing %s: %w", target.GetImage(), err)
	}
	onLog("Pushed successfully.")
	return nil
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
