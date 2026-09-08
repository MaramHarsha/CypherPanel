package upgrade

// The fallback snapshot (panel-updates.md §5): a pg_dump of the panel's
// database, written 0600 beside the data directory.
//
// WHY pg_dump AND NOT GO. Writing a logical dump over the pool was considered
// and rejected: it means owning foreign-key ordering, sequences, extensions and
// constraint deferral for a schema that grows every release, to reimplement a
// tool that already handles all four.
//
// THE DATA DIRECTORY IS DELIBERATELY NOT IN IT. JetStream's WORK stream and the
// runtime-log spool live there, and ENGINEERING rule 15 makes them transient by
// rule: anything that must survive a restart is in Postgres before it is
// published. Snapshotting a message spool would capture in-flight work in a
// state that no longer matches the database it is restored beside.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// installPostgresContainer is the container install.sh starts. Its name is a
// constant in install.sh, which is what lets the panel guess it here.
const installPostgresContainer = "cypherpanel-postgres"

// ResolvePgDump finds a usable dump tool, in a fixed order. If none resolves,
// the pre-flight's snapshot line is a REFUSAL, not a warning: an upgrade with
// no fallback is the upgrade this whole feature exists to avoid.
//
// The order is: an explicit override (a command line, so it can be
// "docker exec -i <container> pg_dump"), then pg_dump on PATH, then a docker
// exec into the container Postgres runs in. The default install is exactly
// that last case — install.sh starts Postgres in a container and installs no
// client on the host — and the first version of this only tried docker when
// the URL's host was NOT loopback, which the default install's URL always is.
// So on every install.sh host this resolved to nothing, and every guided
// upgrade was refused at pre-flight.
func ResolvePgDump(databaseURL string) (tool string, args []string, err error) {
	return resolveTool("CYPHERD_SNAPSHOT_PGDUMP", "pg_dump", databaseURL)
}

// ResolvePgRestore mirrors ResolvePgDump.
func ResolvePgRestore(databaseURL string) (tool string, args []string, err error) {
	return resolveTool("CYPHERD_SNAPSHOT_PGRESTORE", "pg_restore", databaseURL)
}

func resolveTool(envName, name, databaseURL string) (string, []string, error) {
	if override := strings.Fields(os.Getenv(envName)); len(override) > 0 {
		return override[0], override[1:], nil
	}
	if path, lookErr := exec.LookPath(name); lookErr == nil {
		return path, nil, nil
	}
	if docker, lookErr := exec.LookPath("docker"); lookErr == nil {
		container := hostOf(databaseURL)
		if container == "" || container == "localhost" || container == "127.0.0.1" {
			container = installPostgresContainer
		}
		return docker, []string{"exec", "-i", container, name}, nil
	}
	return "", nil, fmt.Errorf("no %s found: install postgresql-client on this host, or set %s (e.g. \"docker exec -i %s %s\")",
		name, envName, installPostgresContainer, name)
}

// hostOf pulls the host out of a postgres URL without importing net/url's
// whole surface for one field — and without ever logging the URL, which
// carries the password.
func hostOf(databaseURL string) string {
	s := databaseURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// snapshot dumps the database into the snapshot directory, 0600.
func (h *Helper) snapshot(ctx context.Context) (path string, size int64, err error) {
	tool, args, err := ResolvePgDump(h.o.DatabaseURL)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(h.o.SnapshotDir, 0o700); err != nil {
		return "", 0, err
	}
	name := fmt.Sprintf("panel-%s-%s.dump",
		strings.TrimPrefix(h.o.FromVersion, "v"),
		h.o.Now().UTC().Format("20060102T150405Z"))
	path = filepath.Join(h.o.SnapshotDir, name)

	// Custom format, so pg_restore can order the restore itself rather than us
	// hoping a plain SQL script applies cleanly. Streamed through stdout
	// rather than written with --file: the tool may be running inside the
	// Postgres container, where this host's snapshot directory does not exist.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	full := append(append([]string{}, args...), "--format=custom", h.o.DatabaseURL)
	stderr, runErr := h.o.Runner.RunIO(ctx, nil, f, tool, full...)
	closeErr := f.Close()
	if runErr != nil {
		_ = os.Remove(path)
		// The URL carries a password, so only the tool's own last line is
		// surfaced — never the command line (ENGINEERING rule 20).
		return "", 0, fmt.Errorf("pg_dump failed: %s", lastLine(string(stderr)))
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Size() == 0 {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("the dump produced no data")
	}
	return path, info.Size(), nil
}

// restore puts a snapshot back. --clean --if-exists because the restore has to
// replace a schema that is already there, and single-transaction so a restore
// that fails half-way leaves the database as it was rather than as neither.
func (h *Helper) restore(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("no snapshot to restore")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("the snapshot is missing: %w", err)
	}
	tool, args, err := ResolvePgRestore(h.o.DatabaseURL)
	if err != nil {
		return err
	}
	// The snapshot goes in on stdin, for the same reason the dump came out on
	// stdout: the tool may be inside the Postgres container.
	f, err := os.Open(path) //nolint:gosec // the helper's own snapshot directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	full := append(append([]string{}, args...),
		"--clean", "--if-exists", "--single-transaction", "--dbname="+h.o.DatabaseURL)
	if stderr, err := h.o.Runner.RunIO(ctx, f, io.Discard, tool, full...); err != nil {
		return fmt.Errorf("pg_restore failed: %s", lastLine(string(stderr)))
	}
	return nil
}

// PruneSnapshots removes snapshots past their retention. Zero days keeps them
// forever, which is the operator's choice to make and must not be made for
// them — the fallback is the only thing standing between a bad release and a
// lost panel.
func PruneSnapshots(dir string, retention time.Duration, now time.Time) ([]string, error) {
	if retention <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var removed []string
	cutoff := now.Add(-retention)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".dump") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed, nil
}
