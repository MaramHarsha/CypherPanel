package store

// The COPY surface a plane snapshot is built from
// (plane-disaster-recovery.md §3.2).
//
// The plane already speaks the Postgres wire protocol, and pgx exposes the
// exact primitive pg_dump uses for table data. So the schema is not exported at
// ALL — it comes from the binary's own embedded migrations — and only data
// moves, in COPY TEXT format: the same representation pg_dump writes in plain
// format, with fully specified escaping and no version or architecture
// dependence. Binary format was rejected for exactly that portability
// weakness; a snapshot has to open on a machine that is not this one.

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupConn is the COPY adapter over the pool.
type BackupConn struct{ pool *pgxpool.Pool }

// BackupSurface exposes the pool for a snapshot or a restore.
func (s *Store) BackupSurface() *BackupConn { return &BackupConn{pool: s.pool} }

// NewBackupConn wraps a pool directly, for the restore subcommand, which runs
// with no Store around it.
func NewBackupConn(pool *pgxpool.Pool) *BackupConn { return &BackupConn{pool: pool} }

// tableCatalog reads the tables to export AND the foreign key graph that orders
// them. `goose_db_version` is deliberately excluded: goose rebuilds its own
// bookkeeping when the migrations replay, and copying it would put that in two
// places.
const tableCatalog = `
SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r' AND n.nspname = 'public' AND c.relname <> 'goose_db_version'
ORDER BY c.relname`

const fkCatalog = `
SELECT child.relname, parent.relname
FROM pg_constraint k
JOIN pg_class child ON child.oid = k.conrelid
JOIN pg_class parent ON parent.oid = k.confrelid
JOIN pg_namespace n ON n.oid = child.relnamespace
WHERE k.contype = 'f' AND n.nspname = 'public'`

// Tables returns every table in LOAD ORDER, topologically sorted by foreign
// key. Derived from the live catalog, not a hand-kept list: a release that adds
// a table gets it in the archive the day it exists, with no second place to
// forget.
func (b *BackupConn) Tables(ctx context.Context) ([]string, error) {
	rows, err := b.pool.Query(ctx, tableCatalog)
	if err != nil {
		return nil, fmt.Errorf("store: listing tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	deps := map[string][]string{}
	frows, err := b.pool.Query(ctx, fkCatalog)
	if err != nil {
		return nil, fmt.Errorf("store: reading the foreign key graph: %w", err)
	}
	defer frows.Close()
	for frows.Next() {
		var child, parent string
		if err := frows.Scan(&child, &parent); err != nil {
			return nil, err
		}
		deps[child] = append(deps[child], parent)
	}
	if err := frows.Err(); err != nil {
		return nil, err
	}
	return sortTables(names, deps)
}

// sortTables is a thin indirection so the sort itself lives beside the archive
// that depends on it and can be tested without a database.
var sortTables = func(names []string, deps map[string][]string) ([]string, error) {
	return planebackupSort(names, deps)
}

// planebackupSort is set by the planebackup package's init-free wiring, so the
// store does not import it and create a cycle.
var planebackupSort func([]string, map[string][]string) ([]string, error)

// SetTableSorter injects the topological sort.
func SetTableSorter(fn func([]string, map[string][]string) ([]string, error)) {
	planebackupSort = fn
}

// CopyTo streams one table out in COPY TEXT format.
func (b *BackupConn) CopyTo(ctx context.Context, w io.Writer, table string) (int64, error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	tag, err := conn.Conn().PgConn().CopyTo(ctx, w, `COPY "`+table+`" TO STDOUT`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CopyFrom streams one table back in.
func (b *BackupConn) CopyFrom(ctx context.Context, r io.Reader, table string) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Conn().PgConn().CopyFrom(ctx, r, `COPY "`+table+`" FROM STDIN`)
	return err
}

// SchemaVersion is goose's own bookkeeping — the version the snapshot's data
// belongs to, and the version a restore replays migrations up to before it
// loads a single row.
func (b *BackupConn) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	err := b.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: reading the schema version: %w", err)
	}
	return v, nil
}

// IsEmpty reports whether the target database has no panel tables yet.
// Restoring over a live panel is how a fleet ends up with two half-planes, so
// the restore refuses a non-empty target unless the operator says otherwise.
func (b *BackupConn) IsEmpty(ctx context.Context) (bool, error) {
	var n int64
	err := b.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'r' AND n.nspname = 'public'`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// Pool exposes the pool for the restore's single transaction.
func (b *BackupConn) Pool() *pgxpool.Pool { return b.pool }
