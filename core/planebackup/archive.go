// Package planebackup is the control plane backing itself up
// (plane-disaster-recovery.md).
//
// THIS IS NOT THE HA CONTROL PLANE, and the difference is the whole design.
// There is still exactly one plane: no second node runs, no leader is elected,
// nothing replicates continuously and nothing fails over. The archive is an
// object in a bucket, and a bucket is not a node. Recovery is human-initiated
// and cold — somebody notices, fetches an object, runs a command.
//
// WHY NOT pg_dump. panel-updates chose it for the LOCAL upgrade snapshot and
// that reasoning is sound on-host, one version before the restore. It does not
// survive the move off-host: it would make disaster recovery depend on
// postgresql-client being present AND major-version-matched, and the operator
// finds out it is missing on the day their panel is gone. The runtime image
// carries no pg_dump and no socket to exec through, and the systemd unit's
// hardening is deliberate. A recovery procedure with an undeclared prerequisite
// is not a recovery procedure.
//
// So the plane uses the primitive pg_dump itself uses — COPY over the wire it
// already speaks — and does NOT export a schema at all: the schema comes from
// the binary's own embedded migrations, replayed to the manifest's version.
// Rebuilding it that way uses the mechanism exercised on every boot, rather
// than a second one exercised on the worst day of the year.
package planebackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// Manifest is what a restore reads before it writes anything, so an operator
// can see they grabbed the right night.
type Manifest struct {
	PanelVersion  string         `json:"panel_version"`
	SchemaVersion int64          `json:"schema_version"`
	CreatedAt     time.Time      `json:"created_at"`
	Tables        []TableSummary `json:"tables"`
}

// TableSummary is one table's name and size, in LOAD ORDER — derived from the
// catalog at export time rather than kept by hand, so a release that adds a
// table gets it in the archive the day it exists.
type TableSummary struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// Copier is the Postgres COPY surface (consumer-defined; pgx's PgConn
// satisfies it through a thin adapter).
type Copier interface {
	// Tables returns every table to export, already topologically sorted by
	// foreign key so a restore can load them in order.
	Tables(ctx context.Context) ([]string, error)
	CopyTo(ctx context.Context, w io.Writer, table string) (int64, error)
	CopyFrom(ctx context.Context, r io.Reader, table string) error
	SchemaVersion(ctx context.Context) (int64, error)
	IsEmpty(ctx context.Context) (bool, error)
}

// Encryptor wraps a writer in the archive's encryption. Asymmetric on purpose
// (§4): the plane holds only a RECIPIENT and can only ever write. The private
// half is generated once, shown once, and never stored by the panel — not in
// Postgres, not in the data directory, not in a log line, not in an audit
// detail, and there is no API that returns it.
type Encryptor interface {
	Wrap(w io.Writer, recipient string) (io.WriteCloser, error)
	Unwrap(r io.Reader, identity string) (io.Reader, error)
}

// masterKeyMember is the archive member carrying the panel's master key.
//
// THE KEY IS IN THE ARCHIVE, and that is the crux. A snapshot of this database
// is WORTHLESS without the master key — the CA that signs every agent
// certificate is sealed with it, and the plane fails closed on a wrong key
// rather than quietly minting a new CA — and CATASTROPHIC with it, because the
// archive plus the key is every asset in one file.
//
// Leaving it out and telling the operator to keep it is what the panel already
// does, and nobody does it: the failure is silent, total, and discovered on the
// one day it matters. So the key travels, and the ARCHIVE is what is protected
// — by a private key the panel cannot lose because it never had it.
const masterKeyMember = "master.key"

const manifestMember = "manifest.json"

// Export writes one encrypted archive to w.
//
// Members are compressed INDIVIDUALLY rather than the tar as a whole, which is
// why the object is .tar.age and not .tar.gz.age: `age -d … | tar t` lists the
// table names, and a restore streams one table at a time instead of holding the
// archive in memory.
func Export(ctx context.Context, db Copier, enc Encryptor, w io.Writer, recipient, masterKey, panelVersion string) (Manifest, error) {
	if recipient == "" {
		return Manifest{}, fmt.Errorf("planebackup: no recovery recipient is configured")
	}
	schema, err := db.SchemaVersion(ctx)
	if err != nil {
		return Manifest{}, fmt.Errorf("planebackup: reading the schema version: %w", err)
	}
	tables, err := db.Tables(ctx)
	if err != nil {
		return Manifest{}, fmt.Errorf("planebackup: listing tables: %w", err)
	}

	sealed, err := enc.Wrap(w, recipient)
	if err != nil {
		return Manifest{}, fmt.Errorf("planebackup: opening the encrypted writer: %w", err)
	}
	tw := tar.NewWriter(sealed)

	man := Manifest{PanelVersion: panelVersion, SchemaVersion: schema, CreatedAt: time.Now().UTC()}

	// The key first, so an operator who lists the archive sees immediately that
	// it is a complete recovery rather than a database they cannot open.
	if err := writeMember(tw, masterKeyMember, []byte(masterKey)); err != nil {
		return Manifest{}, err
	}

	for _, t := range tables {
		var buf gzipBuffer
		gz := gzip.NewWriter(&buf)
		rows, err := db.CopyTo(ctx, gz, t)
		if err != nil {
			return Manifest{}, fmt.Errorf("planebackup: copying %s: %w", t, err)
		}
		if err := gz.Close(); err != nil {
			return Manifest{}, fmt.Errorf("planebackup: compressing %s: %w", t, err)
		}
		if err := writeMember(tw, "tables/"+t+".copy.gz", buf.Bytes()); err != nil {
			return Manifest{}, err
		}
		man.Tables = append(man.Tables, TableSummary{Name: t, Rows: rows})
	}

	// The manifest last, because it names the row counts the export produced —
	// a manifest written first would describe what we intended rather than what
	// is in the file.
	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := writeMember(tw, manifestMember, body); err != nil {
		return Manifest{}, err
	}

	if err := tw.Close(); err != nil {
		return Manifest{}, fmt.Errorf("planebackup: closing the archive: %w", err)
	}
	if err := sealed.Close(); err != nil {
		return Manifest{}, fmt.Errorf("planebackup: closing the encrypted writer: %w", err)
	}
	return man, nil
}

func writeMember(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(body)), Format: tar.FormatUSTAR,
	}); err != nil {
		return fmt.Errorf("planebackup: tar header for %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("planebackup: writing %s: %w", name, err)
	}
	return nil
}

// gzipBuffer is a growable sink. One table at a time is held, never the whole
// archive: the largest member is the audit log, and holding one gzipped table
// is what keeps this inside the plane's 300 MB budget.
type gzipBuffer struct{ b []byte }

func (g *gzipBuffer) Write(p []byte) (int, error) {
	g.b = append(g.b, p...)
	return len(p), nil
}
func (g *gzipBuffer) Bytes() []byte { return g.b }

// SortTables is the load order: a topological sort of the foreign key graph, so
// a new table or a new foreign key needs no edit anywhere. A CYCLE breaks the
// sort, and it is detected at EXPORT time — in CI, rather than during a
// recovery.
func SortTables(tables []string, deps map[string][]string) ([]string, error) {
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var out []string
	var visit func(string) error
	visit = func(t string) error {
		switch state[t] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("planebackup: the foreign key graph has a cycle at %q — a restore could not order the load", t)
		}
		state[t] = 1
		parents := append([]string(nil), deps[t]...)
		sort.Strings(parents) // deterministic output for a given schema
		for _, p := range parents {
			if p == t {
				continue // a self-reference orders within one COPY
			}
			if _, known := state[p]; !known {
				// A dependency outside the export set (a view, a table the
				// catalog query excluded) is not an ordering constraint.
				continue
			}
			if err := visit(p); err != nil {
				return err
			}
		}
		state[t] = 2
		out = append(out, t)
		return nil
	}
	names := append([]string(nil), tables...)
	sort.Strings(names)
	for _, t := range names {
		state[t] = 0
	}
	for _, t := range names {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return out, nil
}
