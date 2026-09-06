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

// Pack runs a build pack over a checkout. Consumer-defined so the builder's
// decisions are testable without the binary installed.
type Pack interface {
	// Available reports whether the pack can actually run here. It is part of
	// `auto`'s condition, so a builder that has not opted in behaves exactly as
	// it did before packs existed (pack-builds.md §4).
	Available() bool
	// Generate writes a Dockerfile into contextDir and returns its path
	// relative to that directory.
	//
	// It takes no environment. Nixpacks accepts build-time variables as
	// `--env KEY=VALUE`, and argv is world-readable through `ps` on the
	// builder — a sealed value the rollout carries over mTLS must not be
	// handed to the process table on the way past (pack-builds.md §5). The
	// pack reads nixpacks.toml from the repository instead.
	Generate(ctx context.Context, contextDir, imageTag string, onLog func(string)) (string, error)
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
func (Nixpacks) Generate(ctx context.Context, contextDir, imageTag string, onLog func(string)) (string, error) {
	if _, err := exec.LookPath("nixpacks"); err != nil {
		return "", ErrNixpacksUnavailable
	}
	args := []string{"build", contextDir, "--out", contextDir}
	if imageTag != "" {
		args = append(args, "--name", imageTag)
	}
	onLog("No Dockerfile found — planning this repository with Nixpacks...")
	cmd := exec.CommandContext(ctx, "nixpacks", args...)
	out, err := cmd.CombinedOutput()
	// The pack's own output is the operator's best diagnostic, and it names
	// packages and versions rather than values — nothing sealed is passed to
	// it, so there is nothing here to redact.
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			onLog(line)
		}
	}
	if err != nil {
		return "", fmt.Errorf("nixpacks could not plan this repository: %w", err)
	}

	generated := filepath.Join(contextDir, nixpacksDockerfile)
	if st, serr := os.Stat(generated); serr != nil || st.IsDir() {
		return "", fmt.Errorf("nixpacks reported success but wrote no Dockerfile at %s", nixpacksDockerfile)
	}
	onLog("Nixpacks wrote a Dockerfile; building it.")
	return nixpacksDockerfile, nil
}
