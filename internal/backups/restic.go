package backups

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// binary is the engine executable. It is resolved from PATH (or the
// CYPHER_RESTIC_BIN override) rather than hardcoded — the no-hardcoded-paths
// rule applies to tool locations as much as to config directories.
func binary() string {
	if v := os.Getenv("CYPHER_RESTIC_BIN"); v != "" {
		return v
	}
	return "restic"
}

// Restic is the restic-backed Engine.
type Restic struct{}

// NewRestic returns a restic engine, or ErrUnsupported when the binary is not
// installed on this server.
func NewRestic() (*Restic, error) {
	if _, err := exec.LookPath(binary()); err != nil {
		return nil, ErrUnsupported
	}
	return &Restic{}, nil
}

// run executes a restic subcommand. Credentials are supplied through the
// environment — never as argv — so they never appear in the process table,
// in `ps` output, or in any command echo. stdout is returned; stderr is folded
// into the error so failures stay diagnosable.
func (r *Restic) run(ctx context.Context, repo Repo, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary(), args...)

	// Start from a minimal environment: restic needs only PATH/HOME plus its
	// own credentials. Inheriting the agent's full environment would leak
	// unrelated secrets (DB DSNs, NATS creds) into a third-party process.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"RESTIC_REPOSITORY=" + repo.Repository,
		"RESTIC_PASSWORD=" + repo.Password,
	}
	for k, v := range repo.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// The repository URL is safe to surface (it is not a secret and the
		// operator needs it to debug); the password and env never are.
		return stdout.Bytes(), fmt.Errorf("backups: restic %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// EnsureRepo initialises the repository, treating an already-initialised
// repository as success so the operation is idempotent under task redelivery.
func (r *Restic) EnsureRepo(ctx context.Context, repo Repo) error {
	if _, err := r.run(ctx, repo, "cat", "config"); err == nil {
		return nil // already initialised
	}
	if _, err := r.run(ctx, repo, "init"); err != nil {
		if strings.Contains(err.Error(), "already initialized") ||
			strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

// backupSummary is restic's final --json message for a backup run.
type backupSummary struct {
	MessageType         string  `json:"message_type"`
	SnapshotID          string  `json:"snapshot_id"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	DataAdded           int64   `json:"data_added"`
	TotalDuration       float64 `json:"total_duration"`
}

// Backup creates one incremental snapshot of spec.Paths.
//
// restic does not follow symlinks — it archives the link itself — so an
// account cannot smuggle files from outside its home into a snapshot by
// planting a symlink. That property is why this can safely run as the agent's
// (root) user, which is required to read root-owned files inside an account
// home that the account user itself cannot read.
func (r *Restic) Backup(ctx context.Context, spec Spec) (Snapshot, error) {
	if len(spec.Paths) == 0 {
		return Snapshot{}, fmt.Errorf("backups: no paths to back up")
	}
	if err := r.EnsureRepo(ctx, spec.Repo); err != nil {
		return Snapshot{}, err
	}

	args := []string{"backup", "--json"}
	for _, t := range spec.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range spec.Excludes {
		args = append(args, "--exclude", e)
	}
	args = append(args, spec.Paths...)

	out, err := r.run(ctx, spec.Repo, args...)
	if err != nil {
		return Snapshot{}, err
	}

	// --json emits newline-delimited progress messages; the summary is last.
	var sum backupSummary
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m backupSummary
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.MessageType == "summary" {
			sum = m
		}
	}
	if sum.SnapshotID == "" {
		return Snapshot{}, fmt.Errorf("backups: restic backup reported no snapshot id")
	}
	return Snapshot{
		ID:        sum.SnapshotID,
		Time:      time.Now().UTC(),
		Paths:     spec.Paths,
		Tags:      spec.Tags,
		SizeBytes: sum.TotalBytesProcessed,
	}, nil
}

// resticSnapshot is restic's `snapshots --json` element.
type resticSnapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Paths   []string  `json:"paths"`
	Tags    []string  `json:"tags"`
}

// Snapshots lists the repository's snapshots, filtered to the spec's tags when
// set so a shared repository can hold many accounts without cross-listing.
func (r *Restic) Snapshots(ctx context.Context, spec Spec) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	for _, t := range spec.Tags {
		args = append(args, "--tag", t)
	}
	out, err := r.run(ctx, spec.Repo, args...)
	if err != nil {
		return nil, err
	}
	var raw []resticSnapshot
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("backups: parsing snapshot list: %w", err)
	}
	snaps := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		id := s.ShortID
		if id == "" {
			id = s.ID
		}
		snaps = append(snaps, Snapshot{ID: id, Time: s.Time, Paths: s.Paths, Tags: s.Tags})
	}
	return snaps, nil
}

// Restore writes a snapshot's contents into target. Restore is a first-class
// operation, not an afterthought — a backup that has never been restored is
// not a backup.
func (r *Restic) Restore(ctx context.Context, spec Spec, snapshotID, target string) error {
	if snapshotID == "" || target == "" {
		return fmt.Errorf("backups: snapshot id and target are required")
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return fmt.Errorf("backups: preparing restore target: %w", err)
	}
	_, err := r.run(ctx, spec.Repo, "restore", snapshotID, "--target", target)
	return err
}

// Forget applies the retention policy and prunes unreferenced data. A policy
// that would keep nothing is rejected: silently deleting every snapshot
// because of an unset config is not a recoverable mistake.
func (r *Restic) Forget(ctx context.Context, spec Spec, keep Retention) error {
	if keep.IsZero() {
		return fmt.Errorf("backups: refusing to apply an empty retention policy")
	}
	args := []string{"forget", "--prune"}
	for _, t := range spec.Tags {
		args = append(args, "--tag", t)
	}
	if keep.Daily > 0 {
		args = append(args, "--keep-daily", fmt.Sprint(keep.Daily))
	}
	if keep.Weekly > 0 {
		args = append(args, "--keep-weekly", fmt.Sprint(keep.Weekly))
	}
	if keep.Monthly > 0 {
		args = append(args, "--keep-monthly", fmt.Sprint(keep.Monthly))
	}
	_, err := r.run(ctx, spec.Repo, args...)
	return err
}
