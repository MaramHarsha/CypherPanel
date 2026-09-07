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
	"strings"

	"github.com/jackc/pgx/v5"
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

// deferrableFKs names every foreign key that is not already deferrable. The
// restore makes these deferrable for the length of its transaction and puts
// them back before it commits, so a restored panel's schema is the same shape a
// fresh install has.
const deferrableFKs = `
SELECT child.relname, k.conname
FROM pg_constraint k
JOIN pg_class child ON child.oid = k.conrelid
JOIN pg_namespace n ON n.oid = child.relnamespace
WHERE k.contype = 'f' AND n.nspname = 'public' AND NOT k.condeferrable
ORDER BY child.relname, k.conname`

// BackupTx is the restore's single transaction: every table loads inside it,
// with foreign keys deferred, and nothing is visible until it commits.
//
// WHY DEFERRED, and this is the whole reason this type exists. Three pairs of
// tables in this schema reference each other — an Application names its desired
// Revision while a Revision names its Application, and Databases and Projects
// do the same — so NO load order satisfies every constraint row by row. The
// first version of the snapshot refused to run at all rather than answer that,
// which meant the plane could never back itself up.
//
// Deferring is also what makes the spec's own promise true: all or nothing. A
// failed restore leaves an EMPTY database rather than half a panel, because
// every COPY happened in one transaction that rolled back.
type BackupTx struct {
	tx   pgx.Tx
	undo [][2]string // (table, constraint) to put back before commit
	done bool
}

// BeginLoad opens the restore transaction and defers every foreign key in it.
func (b *BackupConn) BeginLoad(ctx context.Context) (*BackupTx, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: starting the restore transaction: %w", err)
	}
	lt := &BackupTx{tx: tx}
	rows, err := tx.Query(ctx, deferrableFKs)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: reading the foreign keys to defer: %w", err)
	}
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return nil, err
		}
		lt.undo = append(lt.undo, [2]string{table, name})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	for _, fk := range lt.undo {
		stmt := "ALTER TABLE " + ident(fk[0]) + " ALTER CONSTRAINT " + ident(fk[1]) + " DEFERRABLE INITIALLY DEFERRED"
		if _, err := tx.Exec(ctx, stmt); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("store: deferring %s on %s: %w", fk[1], fk[0], err)
		}
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: deferring constraints: %w", err)
	}
	return lt, nil
}

// ClearAll empties every table the restore is about to load, inside the same
// transaction.
//
// WHY THIS IS NEEDED AT ALL. "Restore into an empty database" is not what the
// target looks like by the time the load starts: the restore has just replayed
// the migrations, and migrations SEED — the default team, both release
// channels. Loading the snapshot's own copies of those rows on top is a
// duplicate key, which is how a first restore failed on `teams` after the
// snapshot itself was finally fixed.
//
// It truncates rather than deletes, in one statement so foreign keys between
// the tables do not order it, and `goose_db_version` is excluded because the
// schema the migrations just built is the one being loaded into.
func (t *BackupTx) ClearAll(ctx context.Context) error {
	rows, err := t.tx.Query(ctx, tableCatalog)
	if err != nil {
		return fmt.Errorf("store: listing the tables to clear: %w", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, ident(n))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if _, err := t.tx.Exec(ctx, "TRUNCATE TABLE "+strings.Join(names, ", ")+" RESTART IDENTITY CASCADE"); err != nil {
		return fmt.Errorf("store: clearing the target: %w", err)
	}
	return nil
}

// CopyFrom streams one table in, inside the transaction.
func (t *BackupTx) CopyFrom(ctx context.Context, r io.Reader, table string) error {
	_, err := t.tx.Conn().PgConn().CopyFrom(ctx, r, `COPY `+ident(table)+` FROM STDIN`)
	return err
}

// Commit checks every deferred constraint, puts the constraints back the way
// they were, and commits. The explicit SET CONSTRAINTS ALL IMMEDIATE is what
// makes a violation attributable: it fails HERE, naming the constraint, rather
// than inside COMMIT where the error has no step to blame.
func (t *BackupTx) Commit(ctx context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if _, err := t.tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		_ = t.tx.Rollback(ctx)
		return fmt.Errorf("store: the restored rows do not satisfy the schema: %w", err)
	}
	for _, fk := range t.undo {
		stmt := "ALTER TABLE " + ident(fk[0]) + " ALTER CONSTRAINT " + ident(fk[1]) + " NOT DEFERRABLE"
		if _, err := t.tx.Exec(ctx, stmt); err != nil {
			_ = t.tx.Rollback(ctx)
			return fmt.Errorf("store: restoring %s on %s: %w", fk[1], fk[0], err)
		}
	}
	return t.tx.Commit(ctx)
}

// Rollback discards everything. Safe to call after Commit.
func (t *BackupTx) Rollback(ctx context.Context) {
	if t.done {
		return
	}
	t.done = true
	_ = t.tx.Rollback(ctx)
}

// ident quotes a catalog-supplied identifier. The names come from pg_class and
// pg_constraint rather than from a request, but quoting them is what keeps that
// true of the next caller too.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

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
