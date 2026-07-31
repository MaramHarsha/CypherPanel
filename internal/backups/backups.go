// Package backups is CypherAgent's backup engine. Scheduled backups are
// always incremental and chunk-level deduplicated (restic) — never repeated
// full tar/zip archives, which would blow the disk and CPU budget at
// cPanel-scale account counts (plan.md §4A/4C).
//
// Raw archive export (tar/zip) is reserved for one-off manual downloads and
// deliberately lives in the file manager, not here.
//
// Secrets never travel in task payloads: the repository password and backend
// credentials are fetched from Core over the mTLS gRPC channel at execution
// time and passed to restic through the process environment, never argv.
package backups

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported is returned when no backup engine binary is available on this
// server (restic not installed), so Core can surface a clear, actionable error
// instead of a generic exec failure.
var ErrUnsupported = errors.New("backups: no backup engine available (restic not installed)")

// Repo identifies and unlocks a restic repository. It is assembled at
// execution time from credentials fetched over gRPC — never from a task
// payload, and never logged.
type Repo struct {
	Repository string            // restic repo URL or absolute path
	Password   string            // repository encryption key
	Env        map[string]string // backend credentials (AWS_*, B2_*, ...)
}

// Spec describes one backup operation.
type Spec struct {
	Repo Repo
	// Paths are absolute directories to snapshot (account home, DB dump dir).
	Paths []string
	// Excludes are restic exclude patterns applied to every path — caches and
	// regenerable data that would otherwise bloat every snapshot.
	Excludes []string
	// Tags label the snapshot so per-account snapshots can be listed and
	// pruned independently inside a shared repository.
	Tags []string
	// RunAs is the system user to read account data as. Empty means run as the
	// agent's own user (repository maintenance operations).
	RunAs string
}

// Snapshot is one point-in-time backup in the repository.
type Snapshot struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Paths     []string  `json:"paths"`
	Tags      []string  `json:"tags"`
	SizeBytes int64     `json:"size_bytes"`
}

// Retention is a restic forget policy. Zero values mean "keep none at that
// granularity"; a policy that is entirely zero is rejected rather than applied,
// because it would delete every snapshot.
type Retention struct {
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// IsZero reports whether the policy would prune everything.
func (r Retention) IsZero() bool { return r.Daily <= 0 && r.Weekly <= 0 && r.Monthly <= 0 }

// Engine executes backup operations against a repository. Every method is
// idempotent: JetStream may redeliver a task, and a rerun must reconcile the
// repository rather than duplicate data.
type Engine interface {
	// EnsureRepo initialises the repository if it does not already exist.
	EnsureRepo(ctx context.Context, repo Repo) error
	// Backup creates one snapshot and returns it.
	Backup(ctx context.Context, spec Spec) (Snapshot, error)
	// Snapshots lists snapshots, optionally filtered by the spec's tags.
	Snapshots(ctx context.Context, spec Spec) ([]Snapshot, error)
	// Restore writes snapshot contents into target.
	Restore(ctx context.Context, spec Spec, snapshotID, target string) error
	// Forget applies a retention policy and prunes unreferenced data.
	Forget(ctx context.Context, spec Spec, keep Retention) error
}
