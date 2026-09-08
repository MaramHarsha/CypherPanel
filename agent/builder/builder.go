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
	// packs are the build packs this builder can reach, by build kind
	// (pack-builds.md). Never nil: NewBuilder installs the real ones, and each
	// pack's Available() is what decides whether detection may reach for it.
	packs map[string]Pack
	// buildKit is the second transport, for a pack whose output is a frontend
	// plan rather than a Dockerfile (§2).
	buildKit BuildKitBuilder
}

func NewBuilder(engine EngineClient, workDir string) *Builder {
	return NewBuilderWithPacks(engine, workDir, BuildxCLI{}, map[string]Pack{
		kindNixpacks: Nixpacks{},
		kindRailpack: Railpack{},
	})
}

// NewBuilderWithPacks is NewBuilder with the packs and the second transport
// supplied, so the builder's routing is testable without either installed.
func NewBuilderWithPacks(engine EngineClient, workDir string, bk BuildKitBuilder, packs map[string]Pack) *Builder {
	return &Builder{engine: engine, workDir: workDir, packs: packs, buildKit: bk}
}

// packFor returns the pack for a build kind, or nil when the kind is not one.
func (b *Builder) packFor(kind string) Pack { return b.packs[kind] }

// availablePacks reports which packs this builder could actually use, which is
// part of `auto`'s condition (pack-builds.md §4).
func (b *Builder) availablePacks() map[string]bool {
	out := make(map[string]bool, len(b.packs))
	for kind, p := range b.packs {
		out[kind] = p.Available()
	}
	return out
}

// cloneFailure turns git's exit status into something an operator can act on.
//
// The panel had every piece of information needed to explain this and threw it
// away: a private repository cloned with no credential fails with "could not
// read Username for 'https://github.com'", which reads like a terminal problem,
// and the deployment then showed "git clone failed: exit status 128". The most
// common cause by far is a deploy key that exists in the panel and was never
// attached to the application — so say that, with the remedy.
func cloneFailure(output string, credentialled bool, err error) string {
	lower := strings.ToLower(output)
	authFailed := strings.Contains(lower, "could not read username") ||
		strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "repository not found") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "please make sure you have the correct access rights")

	// git's answer to a schemeless remote. `git clone github.com/acme/web` treats
	// the string as a LOCAL directory, and the only thing that reaches the
	// deployment row otherwise is `exit status 128` — which names neither the
	// field nor the mistake. The API refuses this shape now; this is what an
	// application configured before it did still gets to read.
	notARemote := strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "does not appear to be a git repository")

	switch {
	case notARemote:
		return "the repository is not a git remote git could reach — set it to a full " +
			"https:// URL, or the SSH form git@host:owner/repo.git. A value with no " +
			"scheme is read as a directory on the builder, which is why this says " +
			"the repository does not exist"
	case authFailed && !credentialled:
		return "the repository needs a credential and none was attached — " +
			"if it is private, attach a deploy key to this application (Settings → Source), " +
			"or connect the panel's GitHub App and pick the repository"
	case authFailed:
		return "the credential was refused — check the deploy key is still on the repository, " +
			"or that the GitHub App is still installed on it"
	default:
		// Anything else is git's own problem to describe, and its output is
		// already in the build log above.
		return err.Error()
	}
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
	// Whether this clone carried ANY credential. It is what turns "exit status
	// 128" into a sentence naming the likely cause: git's own message for a
	// private repository reached anonymously is "could not read Username",
	// which reads like a terminal problem rather than a missing key.
	credentialled := false
	if cred := work.GetGitCredential(); cred.GetPassword() != "" {
		credentialled = true
		// An HTTPS clone credential — a GitHub App installation token, minted
		// for this build and valid about an hour (github-app.md §4).
		//
		// It goes in an ASKPASS helper, never in the URL. A credential in the
		// remote URL is written into .git/config inside the build context, and
		// that directory becomes the Docker build context moments later — so
		// the token would be baked into an image layer. It would also reach
		// `ps` and any git error message that echoes the remote.
		askpass := filepath.Join(b.workDir, ".git-askpass-"+work.DeploymentId)
		script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s' \"$GIT_CRED_USER\" ;;\n*) printf '%s' \"$GIT_CRED_PASS\" ;;\nesac\n"
		if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
			return "", fmt.Errorf("writing the git credential helper: %w", err)
		}
		defer func() { _ = os.Remove(askpass) }()

		cloneEnv = append(append([]string(nil), gitEnv...),
			"GIT_ASKPASS="+askpass,
			"GIT_CRED_USER="+cred.GetUsername(),
			"GIT_CRED_PASS="+cred.GetPassword(),
			// Refuse an interactive prompt: without this a rejected token
			// hangs the build on a terminal read nobody is watching.
			"GIT_TERMINAL_PROMPT=0",
		)
		onLog(fmt.Sprintf("Cloning %s at %s through the GitHub App...", displayURL, work.CommitSha))
	} else if work.DeployKeyPem != "" {
		// The key lives beside (not inside) the clone target — git needs an
		// empty destination — under 0600, and is removed on every exit path
		// (deploy-key-private-repos.md §4). The PEM itself is never logged
		// (ENGINEERING rule 20).
		keyFile := filepath.Join(b.workDir, ".deploy-key-"+work.DeploymentId)
		if err := os.WriteFile(keyFile, []byte(work.DeployKeyPem), 0o600); err != nil {
			return "", fmt.Errorf("writing deploy key: %w", err)
		}
		defer func() { _ = os.Remove(keyFile) }()

		credentialled = true
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
		return "", fmt.Errorf("git clone failed: %s", cloneFailure(string(out), credentialled, err))
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
	kind, err := resolveBuildKind(work.BuildKind, contextDir, dockerfilePath, b.availablePacks())
	if err != nil {
		onLog(err.Error())
		return "", err
	}
	// plan is what a pack produced, when one ran. Its shape decides the
	// transport: a Dockerfile goes through the ordinary path, a frontend plan
	// through BuildKit (pack-builds.md §2).
	var plan Plan
	if pack := b.packFor(kind); pack != nil {
		plan, err = pack.Generate(ctx, contextDir, work.Image, onLog)
		if err != nil {
			return "", err
		}
		if plan.Dockerfile != "" {
			// From here the ordinary path takes over, so there is one place
			// labels are stamped, one place a private base image's credential
			// applies, and one place logs stream from.
			dockerfilePath = plan.Dockerfile
		}
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
	if plan.NeedsBuildKit() {
		// The second transport. Same tag, same labels — everything downstream
		// (rollout, relay, rollback, garbage collection) cannot tell which
		// transport produced an image, which is what makes having two
		// acceptable (pack-builds.md §2).
		//
		// The tar pipe is not used here: buildx reads the context from disk
		// itself. It is still closed by the deferred Close above, which stops
		// the walking goroutine.
		if b.buildKit == nil {
			if plan.Frontend != "" {
				return "", ErrRailpackUnavailable
			}
			return "", ErrBuildKitUnavailable
		}
		// A frontend plan names its own file; a Dockerfile that merely needs
		// BuildKit is built from the Dockerfile the pack wrote.
		planFile := plan.PlanFile
		if planFile == "" {
			planFile = dockerfilePath
		}
		if err := b.buildKit.Build(ctx, BuildKitRequest{
			ContextDir: contextDir,
			// Under the agent's own work directory, which is writable by
			// construction — the one place this process is certain to be able
			// to write on a host it does not own.
			StateDir: filepath.Join(b.workDir, ".docker"),
			PlanFile: planFile,
			Frontend: plan.Frontend,
			Tag:      work.Image,
			Labels:   labels,
		}, onLog); err != nil {
			return "", fmt.Errorf("build failed: %w", err)
		}
	} else if err := b.engine.BuildImage(ctx, tarPipeR, work.Image, dockerfilePath, labels, buildAuth, onLog); err != nil {
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
