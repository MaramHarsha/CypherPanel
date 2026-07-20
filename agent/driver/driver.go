// Package driver defines the orchestrator driver contract — the modularity
// seam that eliminates orchestration coupling (architecture.md; ADR-006).
//
// A driver owns everything orchestrator-specific about running Applications on
// this server. The contract is pure desired-state (ADR-005): the agent hands a
// driver the full set of AppSpecs that should exist here, and the driver makes
// reality match — starting what's missing, replacing what changed (health-gated,
// zero-downtime), removing what's absent. There is no start/stop/exec verb
// vocabulary to drift into imperative scripts, and nothing outside a driver
// implementation may touch the orchestrator's API (project-structure rule 2:
// a feature needing `if swarm` is a design error).
//
// The desired-state schema is the generated proto (pkg/proto — one source of
// truth shared with the wire, ENGINEERING rule 18). Drivers are constructed
// explicitly in main and injected (rule 5) — there is no global registry.
package driver

import (
	"context"
	"io"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Management labels every driver stamps on the resources it creates. They are
// how a driver discovers its own managed set on a host it has never seen —
// which is what makes absence-means-remove convergence and desired-state GC
// (threat-model §5.9) possible after a crash or reinstall.
const (
	// LabelManaged marks a resource as CypherPanel-managed; value is the
	// driver name that owns it.
	LabelManaged = "cypherpanel.managed"
	// LabelAppID carries the Application id the resource belongs to.
	LabelAppID = "cypherpanel.app-id"
	// LabelRevisionID carries the revision the resource was created from.
	LabelRevisionID = "cypherpanel.revision-id"
)

// Reconciler is the driver contract. Implementations: driver/docker (launch),
// driver/swarm (V1.x, ADR-006), k8s (post-v1).
//
// Reconcile converges this server toward exactly the desired set:
//
//   - An app in desired but not running here is rolled out.
//   - An app whose running revision differs from desired is replaced with the
//     zero-downtime sequence (start new → health-gate → flip route → drain old).
//   - A resource carrying this driver's management labels whose app is absent
//     from desired is removed, and images no spec references become
//     GC-eligible.
//
// Reconcile is idempotent — converging twice equals converging once (rule 13;
// every driver ships that test) — and safe under work-item redelivery. It
// returns the observed status of every app it manages after convergence; the
// control plane asserts deployment outcomes only from these observations,
// never from work-item completion (ADR-005).
type Reconciler interface {
	// Name identifies the driver ("docker", "swarm") — reported in heartbeats
	// and used to route work.
	Name() string

	// Reconcile converges local reality toward desired and reports what is
	// actually true afterward. A partial failure converges everything it can
	// and reports per-app state; the returned error is reserved for total
	// inability to reconcile (e.g. orchestrator unreachable).
	Reconcile(ctx context.Context, desired []*agentv1.AppSpec) ([]*agentv1.AppStatus, error)
}

// DbReconciler manages database resource reconciliation on this server.
type DbReconciler interface {
	ReconcileDatabases(ctx context.Context, desired []*agentv1.DbSpec) ([]*agentv1.DbStatus, error)
	RemoveDatabase(ctx context.Context, dbID string, deleteVolume bool) error
	Exec(ctx context.Context, containerID string, cmd []string) (io.Reader, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeout time.Duration) error
	WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error
}
