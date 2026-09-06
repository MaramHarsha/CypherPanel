// Package compose reconciles Compose Stacks on one host (compose-stacks.md §4).
//
// It is a reconciler, not a command runner, and the distinction is the whole
// design. The plane sends a FILE; this package owns exactly one invocation of
// its own and runs it against whatever file desired state currently names:
//
//	docker compose --project-name cypher-<stack id> --file <spec> \
//	  --env-file <env> up --detach --remove-orphans --wait
//
// `up -d` is itself a convergence — given the same file it makes the host match
// and does nothing on a second run — so the reconciler contract holds in
// Docker's vocabulary instead of ours, and converging twice mutates nothing.
//
// The other half of the contract is absence-means-remove, in two nested
// senses: `--remove-orphans` drops a service deleted from the file, and a
// `cypher-` project on the host that no spec names is brought down entirely.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Status vocabulary. The agent must not import core, so the strings the plane's
// domain constants carry are repeated here — the same arrangement the database
// reconciler already uses.
const (
	stateRunning  = "running"
	stateDegraded = "degraded"
	stateStopped  = "stopped"
	stateError    = "error"
)

// projectPrefix scopes every project this reconciler owns. Identity lives on
// the host, so a freshly-started agent can find and converge stacks it has
// never seen — and can tell them apart from projects a human started.
const projectPrefix = "cypher-"

// convergeTimeout bounds one `up`. Images may need pulling, so it is generous;
// a stack that has not converged in this long is one an operator needs to hear
// about rather than wait on.
const convergeTimeout = 15 * time.Minute

// Runner executes one compose invocation. Consumer-defined so the reconciler's
// decisions are testable without a Docker daemon.
type Runner interface {
	// Run executes `docker compose` with args, returning its combined output.
	// A non-zero exit is an error whose message carries that output.
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Endpoints resolves where a stack's routed service actually answers.
// Consumer-defined; *engine.Client satisfies it.
type Endpoints interface {
	ContainerNetwork(ctx context.Context, containerID string) (network, ip string, err error)
}

// Router is the proxy surface a stack's route needs — the same fragment writer
// an Application uses, because a compose file's own Traefik labels cannot work
// (ADR-004 keeps the docker provider off; spec §5).
type Router interface {
	AttachNetwork(ctx context.Context, network string) error
	SetRoute(ctx context.Context, key string, route *agentv1.RouteSpec, upstream string) error
	RemoveRoute(ctx context.Context, key string) error
}

// Reconciler converges this host's Compose Stacks.
type Reconciler struct {
	runner    Runner
	endpoints Endpoints
	router    Router
	// workDir holds the rendered file and env file for one converge. Both are
	// written per run and removed on every exit path.
	workDir string
	log     *slog.Logger
}

// New wires the reconciler. endpoints and router may be nil, which makes it
// converge stacks without publishing routes — the shape a test uses, and what a
// node with no Proxy would do.
func New(runner Runner, endpoints Endpoints, router Router, workDir string, log *slog.Logger) *Reconciler {
	return &Reconciler{runner: runner, endpoints: endpoints, router: router, workDir: workDir, log: log}
}

// Reconcile converges the host toward desired and reports what it observes.
//
// A stack that fails to converge does not stop the others: each is independent,
// and one bad compose file must not take a node's whole set with it.
func (r *Reconciler) Reconcile(ctx context.Context, desired []*agentv1.ComposeSpec) ([]*agentv1.ComposeStatus, error) {
	running, err := r.projects(ctx)
	if err != nil {
		return nil, err
	}

	statuses := make([]*agentv1.ComposeStatus, 0, len(desired))
	wanted := make(map[string]bool, len(desired))
	for _, spec := range desired {
		wanted[projectName(spec.GetStackId())] = true
		statuses = append(statuses, r.converge(ctx, spec))
	}

	// Absence means remove, across stacks. Volumes are kept: a stack that
	// vanished from desired state because of a plane-side mistake must not take
	// its data with it (spec §4).
	for _, project := range running {
		if wanted[project] || !strings.HasPrefix(project, projectPrefix) {
			continue
		}
		r.log.Info("removing absent compose stack", "project", project)
		if err := r.down(ctx, project, false); err != nil {
			r.log.Error("removing absent compose stack", "project", project, "error", err)
		}
	}
	return statuses, nil
}

// converge brings one stack to its desired state.
//
// It first asks whether there is anything to do, so converging twice makes no
// mutating call — the reconciler invariant every driver here holds. The answer
// comes from the HOST, not from memory: the revision last converged is a marker
// file beside the work directory, and what is actually running is `compose ps`.
// A freshly-started agent therefore reaches the same conclusion as one that has
// been up for a week, and a service that died since is re-upped because the
// second half of the condition stops being true.
func (r *Reconciler) converge(ctx context.Context, spec *agentv1.ComposeSpec) *agentv1.ComposeStatus {
	project := projectName(spec.GetStackId())
	if r.converged(ctx, spec, project) {
		st := status(spec, stateRunning, "")
		// The route is re-asserted even on the converged path, for the reason
		// the Application reconciler re-asserts its own: a crash between `up`
		// and the route flip would otherwise leave a stack running and
		// unreachable until something else changed.
		r.applyRoute(ctx, spec, project)
		return st
	}
	files, err := r.write(spec)
	if err != nil {
		return status(spec, stateError, err.Error())
	}
	defer files.cleanup(r.log)

	runCtx, cancel := context.WithTimeout(ctx, convergeTimeout)
	defer cancel()

	args := []string{
		"--project-name", project,
		"--file", files.compose,
	}
	if files.env != "" {
		args = append(args, "--env-file", files.env)
	}
	args = append(args, "up", "--detach", "--remove-orphans", "--wait")

	if out, err := r.runner.Run(runCtx, args...); err != nil {
		// Compose's own words. They name services and images, never values:
		// the env file is referenced by path and its contents are not echoed.
		return status(spec, stateError, tail(string(out), err))
	}
	st := r.observe(runCtx, spec, project)
	// The route is applied only when the stack actually came up. Pointing the
	// Proxy at a service that failed to start would publish a domain that
	// answers 502, which is worse than one that answers nothing.
	if st.GetState() == stateRunning || st.GetState() == stateDegraded {
		r.applyRoute(runCtx, spec, project)
	}
	// Recorded only on a full success, so a degraded stack is retried on the
	// next pass rather than remembered as done.
	if st.GetState() == stateRunning {
		r.markConverged(spec)
	}
	return st
}

// converged reports whether this stack is already at its desired revision with
// everything running — the one case in which `up` would change nothing.
func (r *Reconciler) converged(ctx context.Context, spec *agentv1.ComposeSpec, project string) bool {
	if r.marker(spec.GetStackId()) != spec.GetRevisionId() {
		return false
	}
	out, err := r.runner.Run(ctx, "--project-name", project, "ps", "--format", "json", "--all")
	if err != nil {
		return false
	}
	total, up := countServices(out)
	return total > 0 && up == total
}

// markerPath is where the last converged revision is recorded. On the host, so
// the reconciler keeps no memory of its own (reconciler-development: truth is
// on the host).
func (r *Reconciler) markerPath(stackID string) string {
	return filepath.Join(r.workDir, stackID+".revision")
}

func (r *Reconciler) marker(stackID string) string {
	b, err := os.ReadFile(r.markerPath(stackID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (r *Reconciler) markConverged(spec *agentv1.ComposeSpec) {
	if err := os.MkdirAll(r.workDir, 0o700); err != nil {
		r.log.Warn("creating compose work directory", "error", err)
		return
	}
	// Best-effort: a lost marker costs one redundant `up`, which is a no-op.
	// Failing the converge over it would be strictly worse.
	if err := os.WriteFile(r.markerPath(spec.GetStackId()), []byte(spec.GetRevisionId()), 0o600); err != nil {
		r.log.Warn("recording converged compose revision", "stack_id", spec.GetStackId(), "error", err)
	}
}

// applyRoute points the Proxy at the service the stack named, or removes the
// fragment when it names none. Best-effort and logged: a routing failure is
// reported through the Proxy's own surface, and must not turn a stack that IS
// running into one the plane believes failed.
func (r *Reconciler) applyRoute(ctx context.Context, spec *agentv1.ComposeSpec, project string) {
	if r.router == nil {
		return
	}
	route := spec.GetRoute()
	key := spec.GetStackId()
	if route.GetDomain() == "" {
		if err := r.router.RemoveRoute(ctx, key); err != nil {
			r.log.Warn("removing compose route", "stack_id", key, "error", err)
		}
		return
	}
	upstream, network, err := r.upstream(ctx, project, route)
	if err != nil {
		r.log.Warn("resolving compose upstream", "stack_id", key, "service", route.GetService(), "error", err)
		return
	}
	// The Proxy has to be on the stack's network to reach it; compose named
	// that network, not the plane, so it is discovered rather than assumed.
	if err := r.router.AttachNetwork(ctx, network); err != nil {
		r.log.Warn("attaching proxy to compose network", "network", network, "error", err)
		return
	}
	if err := r.router.SetRoute(ctx, key, &agentv1.RouteSpec{
		Domain:     route.GetDomain(),
		Https:      route.GetHttps(),
		PathPrefix: route.GetPathPrefix(),
	}, upstream); err != nil {
		r.log.Warn("applying compose route", "stack_id", key, "error", err)
	}
}

// upstream finds the container backing the routed service and the address the
// Proxy should send to.
func (r *Reconciler) upstream(ctx context.Context, project string, route *agentv1.ComposeRoute) (upstream, network string, err error) {
	if r.endpoints == nil {
		return "", "", fmt.Errorf("no endpoint resolver wired")
	}
	out, err := r.runner.Run(ctx, "--project-name", project, "ps", "--format", "json")
	if err != nil {
		return "", "", fmt.Errorf("listing services: %s", tail(string(out), err))
	}
	id := containerFor(out, route.GetService())
	if id == "" {
		return "", "", fmt.Errorf("service %q is not running in this stack", route.GetService())
	}
	network, ip, err := r.endpoints.ContainerNetwork(ctx, id)
	if err != nil {
		return "", "", err
	}
	return net.JoinHostPort(ip, strconv.Itoa(int(route.GetPort()))), network, nil
}

// containerFor picks the container id backing one compose service.
func containerFor(out []byte, service string) string {
	for _, line := range splitJSON(out) {
		var row struct {
			ID      string `json:"ID"`
			Service string `json:"Service"`
			State   string `json:"State"`
		}
		if json.Unmarshal(line, &row) != nil || row.Service != service {
			continue
		}
		if row.State == "running" {
			return row.ID
		}
	}
	return ""
}

// observe asks the host what is actually running, rather than inferring success
// from the fact that `up` returned (ADR-005).
func (r *Reconciler) observe(ctx context.Context, spec *agentv1.ComposeSpec, project string) *agentv1.ComposeStatus {
	out, err := r.runner.Run(ctx, "--project-name", project, "ps", "--format", "json", "--all")
	if err != nil {
		return status(spec, stateError, tail(string(out), err))
	}
	total, up := countServices(out)
	switch {
	case total == 0:
		return status(spec, stateStopped, "no services are running")
	case up == total:
		return status(spec, stateRunning, "")
	case up == 0:
		return status(spec, stateError, "no service is running")
	default:
		return status(spec, stateDegraded, fmt.Sprintf("%d of %d services running", up, total))
	}
}

// Remove brings one stack down, optionally with its volumes. Idempotent: a
// project that is already gone is not an error, which is what makes a redelivered
// removal harmless (rule 13).
func (r *Reconciler) Remove(ctx context.Context, stackID string, deleteVolumes bool) error {
	// Forget first: a stack re-created under the same id must not be mistaken
	// for one that is already converged.
	if err := os.Remove(r.markerPath(stackID)); err != nil && !os.IsNotExist(err) {
		r.log.Warn("clearing converged marker", "stack_id", stackID, "error", err)
	}
	return r.down(ctx, projectName(stackID), deleteVolumes)
}

func (r *Reconciler) down(ctx context.Context, project string, deleteVolumes bool) error {
	args := []string{"--project-name", project, "down", "--remove-orphans"}
	if deleteVolumes {
		args = append(args, "--volumes")
	}
	if out, err := r.runner.Run(ctx, args...); err != nil {
		return fmt.Errorf("compose down %s: %s", project, tail(string(out), err))
	}
	return nil
}

// projects lists the compose projects this host currently has.
func (r *Reconciler) projects(ctx context.Context) ([]string, error) {
	out, err := r.runner.Run(ctx, "ls", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("compose: listing projects: %s", tail(string(out), err))
	}
	var rows []struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &rows); err != nil {
		return nil, fmt.Errorf("compose: reading project list: %w", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names, nil
}

// renderedFiles are the two files one converge needs, both removed afterwards.
type renderedFiles struct {
	compose string
	env     string
}

func (f renderedFiles) cleanup(log *slog.Logger) {
	for _, p := range []string{f.compose, f.env} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Warn("removing rendered compose file", "path", p, "error", err)
		}
	}
}

// write renders the spec to disk. The env file is 0600 and short-lived: a
// secret is on this host only for as long as one converge takes, and never
// inside the compose file the plane stores (spec §2).
func (r *Reconciler) write(spec *agentv1.ComposeSpec) (renderedFiles, error) {
	if err := os.MkdirAll(r.workDir, 0o700); err != nil {
		return renderedFiles{}, fmt.Errorf("creating compose work directory: %w", err)
	}
	stack := spec.GetStackId()
	// The id is plane-generated and prefixed, but it names a path, so a
	// traversal in it would write outside the work directory.
	if stack == "" || strings.ContainsAny(stack, "/\\") || strings.Contains(stack, "..") {
		return renderedFiles{}, fmt.Errorf("invalid stack id %q", stack)
	}

	var files renderedFiles
	files.compose = filepath.Join(r.workDir, stack+".yml")
	if err := os.WriteFile(files.compose, []byte(spec.GetComposeYaml()), 0o600); err != nil {
		return renderedFiles{}, fmt.Errorf("writing compose file: %w", err)
	}
	if env := spec.GetEnv(); len(env) > 0 {
		files.env = filepath.Join(r.workDir, stack+".env")
		if err := os.WriteFile(files.env, envFile(env), 0o600); err != nil {
			files.cleanup(r.log)
			return renderedFiles{}, fmt.Errorf("writing compose env file: %w", err)
		}
	}
	return files, nil
}

// envFile renders the variables compose interpolates. Sorted so the same spec
// produces the same bytes, and values are written raw: the plane's key
// validation already refuses anything that could open a second assignment.
func envFile(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// countServices reads `compose ps --format json`. Compose emits either a JSON
// array or newline-delimited objects depending on version, so both are read.
func countServices(out []byte) (total, up int) {
	for _, line := range splitJSON(out) {
		var row struct {
			State string `json:"State"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		total++
		if row.State == "running" {
			up++
		}
	}
	return total, up
}

func splitJSON(out []byte) [][]byte {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if json.Unmarshal(trimmed, &rows) != nil {
			return nil
		}
		lines := make([][]byte, 0, len(rows))
		for _, r := range rows {
			lines = append(lines, r)
		}
		return lines
	}
	var lines [][]byte
	for _, l := range bytes.Split(trimmed, []byte("\n")) {
		if l = bytes.TrimSpace(l); len(l) > 0 {
			lines = append(lines, l)
		}
	}
	return lines
}

func projectName(stackID string) string { return projectPrefix + stackID }

func status(spec *agentv1.ComposeSpec, state, detail string) *agentv1.ComposeStatus {
	return &agentv1.ComposeStatus{
		StackId:    spec.GetStackId(),
		RevisionId: spec.GetRevisionId(),
		State:      state,
		Detail:     detail,
		ObservedAt: timestamppb.Now(),
	}
}

// tail keeps the last few lines of compose's output, which is where its verdict
// is. A whole pull log would flood the status column for no gain.
func tail(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	msg := strings.TrimSpace(strings.Join(lines, "; "))
	if msg == "" {
		return err.Error()
	}
	return msg
}

// ─── the runner ─────────────────────────────────────────────────────────────

// CLI runs the real `docker compose`.
type CLI struct{}

// ErrUnavailable reports that the compose plugin is missing, rather than
// letting an exec failure surface as an opaque status. Every host the panel's
// own installer touched has it: get.docker.com ships docker-compose-plugin.
var ErrUnavailable = fmt.Errorf("docker compose is not available on this host — install the docker-compose-plugin package")

func (CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil && bytes.Contains(out, []byte("is not a docker command")) {
		return out, ErrUnavailable
	}
	return out, err
}
