package builder

// Build packs (pack-builds.md): handing a repository with no Dockerfile to a
// program whose entire job is working out how it builds.
//
// The integration is deliberately shallow. Nixpacks is asked to WRITE a
// Dockerfile and not to build; the ordinary tar-and-build path then consumes
// it. That keeps one build path, so labels, private base-image credentials and
// log streaming all stay in one place, and it keeps the pack replaceable — it
// is a program that produces a Dockerfile, not a second way to make an image.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// nixpacksDockerfile is where `nixpacks build --out <dir>` leaves what it
// generated, relative to that directory.
const nixpacksDockerfile = ".nixpacks/Dockerfile"

// railpackPlan is the build plan `railpack prepare --plan-out` writes, and
// railpackFrontend is the BuildKit gateway frontend that consumes it. Both are
// Railpack's own published contract for platforms (its frontend reference).
const (
	railpackPlan     = "railpack-plan.json"
	railpackFrontend = "ghcr.io/railwayapp/railpack-frontend"
)

// Plan is what a pack produced, and how it must be built.
//
// The two shapes are not a detail — they are the whole reason there are two
// transports. Nixpacks emits a Dockerfile, which the daemon's classic /build
// endpoint parses. Railpack emits an LLB plan that only a BuildKit gateway
// frontend understands, and the classic endpoint cannot resolve a frontend at
// all. A pack says which it produced; the builder picks the path.
type Plan struct {
	// Dockerfile is a path relative to the build context. Set when the pack
	// produced something the ordinary build path can consume.
	Dockerfile string
	// PlanFile and Frontend are set together when the result must be built by a
	// BuildKit gateway frontend. PlanFile is relative to the context.
	PlanFile string
	Frontend string
}

// NeedsBuildKit reports whether this plan requires the second transport.
func (p Plan) NeedsBuildKit() bool { return p.Frontend != "" }

// Pack runs a build pack over a checkout. Consumer-defined so the builder's
// decisions are testable without the binary installed.
type Pack interface {
	// Available reports whether the pack can actually run here. It is part of
	// `auto`'s condition, so a builder that has not opted in behaves exactly as
	// it did before packs existed (pack-builds.md §4).
	Available() bool
	// Generate writes the pack's output into contextDir and returns how to
	// build it.
	//
	// It takes no environment. Both packs accept build-time variables as
	// `--env KEY=VALUE`, and argv is world-readable through `ps` on the
	// builder — a sealed value the rollout carries over mTLS must not be
	// handed to the process table on the way past (pack-builds.md §5). A pack
	// reads its own config file from the repository instead.
	Generate(ctx context.Context, contextDir, imageTag string, onLog func(string)) (Plan, error)
}

// Nixpacks is the real pack.
type Nixpacks struct{}

// ErrNixpacksUnavailable reports the binary is missing, rather than letting an
// exec failure surface as an opaque build error.
var ErrNixpacksUnavailable = fmt.Errorf("nixpacks is not installed on this builder")

func (Nixpacks) Available() bool {
	_, err := exec.LookPath("nixpacks")
	return err == nil
}

// Generate asks Nixpacks to plan the repository and write the result, without
// building it: `--out` is what makes this fit the existing path.
func (Nixpacks) Generate(ctx context.Context, contextDir, imageTag string, onLog func(string)) (Plan, error) {
	if _, err := exec.LookPath("nixpacks"); err != nil {
		return Plan{}, ErrNixpacksUnavailable
	}
	args := []string{"build", contextDir, "--out", contextDir}
	if imageTag != "" {
		args = append(args, "--name", imageTag)
	}
	onLog("No Dockerfile found — planning this repository with Nixpacks...")
	out, err := exec.CommandContext(ctx, "nixpacks", args...).CombinedOutput()
	// The pack's own output is the operator's best diagnostic, and it names
	// packages and versions rather than values — nothing sealed is passed to
	// it, so there is nothing here to redact.
	streamOutput(out, onLog)
	if err != nil {
		return Plan{}, fmt.Errorf("nixpacks could not plan this repository: %w", err)
	}

	generated := filepath.Join(contextDir, nixpacksDockerfile)
	if st, serr := os.Stat(generated); serr != nil || st.IsDir() {
		return Plan{}, fmt.Errorf("nixpacks reported success but wrote no Dockerfile at %s", nixpacksDockerfile)
	}
	onLog("Nixpacks wrote a Dockerfile; building it.")
	return Plan{Dockerfile: nixpacksDockerfile}, nil
}

// Railpack is the second pack. Unlike Nixpacks it does not emit a Dockerfile:
// `railpack prepare` writes an LLB build plan that only its own BuildKit
// gateway frontend understands, which is why supporting it meant adding a
// second build transport rather than another build kind (pack-builds.md §2).
type Railpack struct{}

// ErrRailpackUnavailable reports that the pack, or the BuildKit transport it
// needs, is missing. Both are named because either one alone is not enough.
var ErrRailpackUnavailable = fmt.Errorf(
	"railpack builds need both the railpack binary and docker buildx on this builder")

// Available requires BOTH halves. Railpack that cannot be built is not
// available in any sense the operator cares about, and reporting it as
// available would turn a missing buildx into a failure at build time rather
// than a clear refusal at configuration time.
func (Railpack) Available() bool {
	if _, err := exec.LookPath("railpack"); err != nil {
		return false
	}
	return buildKitAvailable()
}

// Generate asks Railpack to prepare a build plan, without building it.
func (Railpack) Generate(ctx context.Context, contextDir, _ string, onLog func(string)) (Plan, error) {
	if !(Railpack{}).Available() {
		return Plan{}, ErrRailpackUnavailable
	}
	planPath := filepath.Join(contextDir, railpackPlan)

	onLog("No Dockerfile found — planning this repository with Railpack...")
	out, err := exec.CommandContext(ctx, "railpack", "prepare", contextDir,
		"--plan-out", planPath).CombinedOutput()
	streamOutput(out, onLog)
	if err != nil {
		return Plan{}, fmt.Errorf("railpack could not plan this repository: %w", err)
	}
	if st, serr := os.Stat(planPath); serr != nil || st.IsDir() {
		return Plan{}, fmt.Errorf("railpack reported success but wrote no plan at %s", railpackPlan)
	}
	onLog("Railpack wrote a build plan; building it with its BuildKit frontend.")
	return Plan{PlanFile: railpackPlan, Frontend: railpackFrontend}, nil
}

// buildKitAvailable reports whether `docker buildx` can be invoked here.
func buildKitAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "buildx", "version").Run() == nil
}

// streamOutput hands a pack's own words to the build log. They name packages
// and versions rather than values — nothing sealed is passed to a pack, so
// there is nothing here to redact.
func streamOutput(out []byte, onLog func(string)) {
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			onLog(line)
		}
	}
}
