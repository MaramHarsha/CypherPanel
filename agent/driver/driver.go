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
	"encoding/base32"
	"strings"

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
	// LabelRestartToken carries the restart token the container was created
	// under (deployment-control.md §3). It is part of the container's identity
	// so a NEW token reads as drift — the same way a new revision does — and
	// the ordinary recreate path closes it. Absent on containers created
	// before the feature, and on every application that has never restarted,
	// which both compare equal to an empty spec token.
	LabelRestartToken = "cypherpanel.restart-token"
)

// PullMarkerPrefix is the repository namespace of the marker reference that
// records "our own pull created this registry reference".
//
// A pulled image cannot carry our labels — those are baked in by whoever built
// it — so a *reference* is the only thing a driver can attach to one. That is
// what this namespace is for: ownership of the floating reference a pull
// creates is knowable only at pull time (afterwards nothing distinguishes it
// from a tag the operator made), and it has to survive everything that can
// happen next — a container that is never created, a rollout discarded at the
// health gate, an agent restart, the application's own deletion. The image
// survives all of them, so the record lives on the image.
const PullMarkerPrefix = "cypher-pull/"

// maxReferenceName is Docker's limit on a repository name, tag excluded.
const maxReferenceName = 255

// refEncoding renders an arbitrary registry reference as a legal repository
// path component. Base32 lowercased is [a-z2-7], which a repository name
// accepts as a single component; base64's +/= are not legal there, and a
// readable escaping cannot survive the uppercase a *tag* may contain. Hex
// would also be legal but spends a third more of the 255-character name
// budget, which is what bounds the references this can record at all.
var refEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// PullMarkerRef is the marker reference recording that appID's pull created
// source. The reference itself is encoded into the repository path so the
// exact string to drop is recoverable from the daemon alone — with no spec to
// consult and no container to read a label off — which is what lets garbage
// collection finish the job for an application that no longer exists.
//
// ok is false for a reference too long to encode within Docker's name limit
// (~151 bytes of reference); the caller drops it immediately and, failing
// that, leaves it, exactly as it would leave one the operator made.
func PullMarkerRef(appID, source string) (ref string, ok bool) {
	if appID == "" || source == "" || strings.ContainsAny(appID, ":/") {
		return "", false
	}
	name := PullMarkerPrefix + strings.ToLower(refEncoding.EncodeToString([]byte(source)))
	if len(name) > maxReferenceName {
		return "", false
	}
	return name + ":" + appID, true
}

// ParsePullMarker recovers the application and the registry reference a marker
// records. Anything that is not one of ours returns ok=false and is left alone.
func ParsePullMarker(ref string) (appID, source string, ok bool) {
	rest, found := strings.CutPrefix(ref, PullMarkerPrefix)
	if !found {
		return "", "", false
	}
	encoded, appID, found := strings.Cut(rest, ":")
	if !found || encoded == "" || appID == "" || strings.Contains(encoded, "/") {
		return "", "", false
	}
	raw, err := refEncoding.DecodeString(strings.ToUpper(encoded))
	if err != nil || len(raw) == 0 {
		return "", "", false
	}
	return appID, string(raw), true
}

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
	//
	// retain names the revisions whose images must survive garbage collection
	// (disk-management.md §2). It is a SEPARATE list rather than a field on
	// AppSpec because its absence means something different: an application
	// missing from desired is removed, while one missing from retain is simply
	// left alone.
	Reconcile(ctx context.Context, desired []*agentv1.AppSpec, retain []*agentv1.RetainSpec) ([]*agentv1.AppStatus, error)
}

// DbReconciler manages database resource reconciliation on this server
// (managed-databases.md §6). Backup/restore execution is a separate follow-up
// (stripped first-cut recoverable at commit 1d83f0a) and will extend this seam then.
type DbReconciler interface {
	ReconcileDatabases(ctx context.Context, desired []*agentv1.DbSpec) ([]*agentv1.DbStatus, error)
	RemoveDatabase(ctx context.Context, dbID string, deleteVolume bool) error
}

// ComposeReconciler converges this host's Compose Stacks (consumer-defined;
// *compose.Reconciler satisfies it — compose-stacks.md §4). Optional: nil on a
// builder-role agent and wherever the feature is unwired, which makes a node
// behave exactly as it did before Compose Stacks existed.
type ComposeReconciler interface {
	Reconcile(ctx context.Context, desired []*agentv1.ComposeSpec) ([]*agentv1.ComposeStatus, error)
	Remove(ctx context.Context, stackID string, deleteVolumes bool) error
}
