package planebackup

// The restore (plane-disaster-recovery.md §6). It runs with the plane STOPPED,
// as a subcommand of the same binary — one artifact to build, sign and ship.
//
// There is no restore button in the panel, on purpose: a panel that can rewind
// itself from its own UI is a panel one compromised session can rewind.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrTargetNotEmpty is the refusal that stops a fleet ending up with two
// half-planes.
var ErrTargetNotEmpty = errors.New("planebackup: the target database is not empty")

// ErrNewerSnapshot is a snapshot from a panel newer than this binary. That
// restore cannot work, and the honest answer is one line long.
var ErrNewerSnapshot = errors.New("planebackup: this snapshot was taken by a newer panel")

// Migrator replays the embedded migrations to a version.
type Migrator interface {
	// UpTo migrates to exactly version, which is how the schema is rebuilt to
	// match the snapshot's data before a single row is loaded.
	UpTo(ctx context.Context, version int64) error
	// Up migrates the rest of the way to this binary's own version, through
	// the ordinary path — the one exercised on every boot.
	Up(ctx context.Context) error
	// Current is the newest migration this binary carries.
	Current() int64
}

// RestoreOptions is one restore.
type RestoreOptions struct {
	DB       Copier
	Enc      Encryptor
	Migrate  Migrator
	Identity string
	// Force allows a non-empty target. Restoring over a live panel is how a
	// fleet ends up with two half-planes, so it is never the default.
	Force bool
	Log   func(format string, args ...any)
}

// RestoreResult is what the operator is told afterwards.
type RestoreResult struct {
	Manifest  Manifest
	MasterKey string
	Loaded    int
}

// Restore decrypts an archive and loads it. Every step refuses rather than
// half-succeeds, and the ORDER is the design: nothing is written until the
// manifest has been read and printed, so an operator can see they grabbed the
// right night before anything is irreversible.
func Restore(ctx context.Context, r io.Reader, o RestoreOptions) (RestoreResult, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	plain, err := o.Enc.Unwrap(r, o.Identity)
	if err != nil {
		return RestoreResult{}, err
	}

	// The whole archive is read into a member map first, because a tar is
	// sequential and the manifest is written LAST — it names the row counts the
	// export actually produced, so it cannot come first.
	members := map[string][]byte{}
	tr := tar.NewReader(plain)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return RestoreResult{}, fmt.Errorf("planebackup: reading the archive: %w", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return RestoreResult{}, fmt.Errorf("planebackup: reading %s: %w", h.Name, err)
		}
		members[h.Name] = body
	}

	raw, ok := members[manifestMember]
	if !ok {
		return RestoreResult{}, fmt.Errorf("planebackup: the archive has no manifest — it may be truncated")
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return RestoreResult{}, fmt.Errorf("planebackup: the manifest will not parse: %w", err)
	}

	// Printed BEFORE anything is written.
	logf("snapshot from %s, panel %s, schema %d, %d tables",
		man.CreatedAt.Format(time.RFC3339), man.PanelVersion, man.SchemaVersion, len(man.Tables))

	if man.SchemaVersion > o.Migrate.Current() {
		return RestoreResult{}, fmt.Errorf("%w: it needs schema %d and this binary carries %d — install %s or newer and try again",
			ErrNewerSnapshot, man.SchemaVersion, o.Migrate.Current(), man.PanelVersion)
	}

	empty, err := o.DB.IsEmpty(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	if !empty && !o.Force {
		return RestoreResult{}, fmt.Errorf("%w — restoring over a live panel leaves a fleet with two half-planes. Point at an empty database, or pass --force if you are certain", ErrTargetNotEmpty)
	}

	// The schema is rebuilt to the SNAPSHOT's version, not to this binary's:
	// the data belongs to that shape, and migrating the rest of the way after
	// the load is what carries it forward — through the ordinary path,
	// exercised on every boot.
	logf("migrating to schema %d", man.SchemaVersion)
	if err := o.Migrate.UpTo(ctx, man.SchemaVersion); err != nil {
		return RestoreResult{}, fmt.Errorf("planebackup: rebuilding the schema: %w", err)
	}

	loaded := 0
	for _, t := range man.Tables {
		body, ok := members["tables/"+t.Name+".copy.gz"]
		if !ok {
			// A table in the manifest with no member is a truncated archive,
			// and continuing would load a partial panel.
			return RestoreResult{}, fmt.Errorf("planebackup: the archive is missing %s", t.Name)
		}
		gz, err := gzip.NewReader(strings.NewReader(string(body)))
		if err != nil {
			return RestoreResult{}, fmt.Errorf("planebackup: decompressing %s: %w", t.Name, err)
		}
		if err := o.DB.CopyFrom(ctx, gz, t.Name); err != nil {
			return RestoreResult{}, fmt.Errorf("planebackup: loading %s: %w", t.Name, err)
		}
		_ = gz.Close()
		loaded++
		logf("loaded %s (%d rows)", t.Name, t.Rows)
	}

	logf("migrating forward to this build")
	if err := o.Migrate.Up(ctx); err != nil {
		return RestoreResult{}, fmt.Errorf("planebackup: migrating forward: %w", err)
	}

	return RestoreResult{Manifest: man, MasterKey: string(members[masterKeyMember]), Loaded: loaded}, nil
}
