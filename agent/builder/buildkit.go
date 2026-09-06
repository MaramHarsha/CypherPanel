package builder

// The second build transport (pack-builds.md §2).
//
// Every build here goes through the daemon's classic `/build` endpoint, which
// parses a Dockerfile. A BuildKit *gateway frontend* — the shape Railpack
// publishes — is something that endpoint cannot resolve at all: the frontend is
// an image BuildKit fetches and hands the plan to, and the classic builder has
// no concept of one.
//
// So this is a second way to make an image, and it is deliberately narrow: one
// command, only for a plan that names a frontend, producing the same tag with
// the same management labels as every other build. Everything downstream —
// rollout, relay, rollback, garbage collection — cannot tell which transport
// produced an image, which is the property that makes having two acceptable.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// BuildKitBuilder builds a frontend plan by shelling out to `docker buildx`.
// Consumer-defined at its use site so the routing is testable without a daemon.
type BuildKitBuilder interface {
	Build(ctx context.Context, req BuildKitRequest, onLog func(string)) error
}

// BuildKitRequest is one frontend build.
type BuildKitRequest struct {
	ContextDir string
	// PlanFile is the frontend's input, relative to ContextDir — what `-f`
	// points at, standing where a Dockerfile normally would.
	PlanFile string
	// Frontend is the gateway image BuildKit fetches to interpret the plan.
	Frontend string
	Tag      string
	Labels   map[string]string
}

// BuildxCLI is the real transport.
type BuildxCLI struct{}

// Build runs the frontend build.
//
// `--load` is explicit rather than assumed: with buildx's container driver the
// result stays in the builder unless asked for, and an image that never reached
// the daemon would fail at rollout with nothing to explain it.
func (BuildxCLI) Build(ctx context.Context, req BuildKitRequest, onLog func(string)) error {
	if !buildKitAvailable() {
		return ErrRailpackUnavailable
	}
	args := []string{
		"buildx", "build",
		// The frontend, passed the way Railpack's own reference documents it.
		"--build-arg", "BUILDKIT_SYNTAX=" + req.Frontend,
		"--file", req.PlanFile,
		"--tag", req.Tag,
		"--load",
		// Plain progress: this output is streamed to an operator reading a
		// build log, not to a terminal that can redraw itself.
		"--progress", "plain",
	}
	// Sorted, so the same inputs produce the same invocation — a build that
	// differs only in argument order is one nobody can reason about.
	for _, k := range sortedKeys(req.Labels) {
		args = append(args, "--label", k+"="+req.Labels[k])
	}
	args = append(args, ".")

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = req.ContextDir

	// Streamed rather than collected: a frontend build pulls base images and
	// runs the whole build, and an operator watching it needs the output as it
	// happens rather than in one block at the end.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("attaching to buildx output: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	var tail lineTail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting buildx: %w", err)
	}
	scanner := bufio.NewScanner(io.LimitReader(pipe, 1<<20))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		tail.add(line)
		onLog(line)
	}
	if err := cmd.Wait(); err != nil {
		// buildx's own verdict, which is the last thing it printed. A whole
		// build log in a status field helps nobody.
		return fmt.Errorf("buildkit build failed: %s", tail.String())
	}
	return nil
}

// lineTail keeps the last few lines of a stream, which is where a build's
// verdict is.
type lineTail struct {
	lines []string
}

func (t *lineTail) add(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > 6 {
		t.lines = t.lines[1:]
	}
}

func (t *lineTail) String() string {
	if len(t.lines) == 0 {
		return "no output"
	}
	var b bytes.Buffer
	for i, l := range t.lines {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(strings.TrimSpace(l))
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
