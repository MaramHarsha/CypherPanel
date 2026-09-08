package store

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/planebackup"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// THE TEST THAT WAS MISSING, and its absence is why the plane could never back
// itself up.
//
// `SortTables` had a unit test proving a cyclic graph was refused, on a
// two-table graph written for the test. Nobody ran it against THIS schema,
// which has three cycles by design — an Application names its desired Revision
// while a Revision names its Application, and Databases and Projects do the
// same with their revisions and their default Environment. So every snapshot
// the panel attempted failed at `Tables`, with a message about a cycle, before
// a byte was written.
//
// This runs the real query against the real schema, which is the only place
// that could have caught it.
func TestStoreBackupOrdersTheRealSchema(t *testing.T) {
	s := testStore(t)
	SetTableSorter(planebackup.SortTables)
	ctx := context.Background()

	order, err := s.BackupSurface().Tables(ctx)
	if err != nil {
		t.Fatalf("the real schema would not order for a snapshot: %v", err)
	}
	if len(order) < 40 {
		t.Fatalf("only %d tables in the load order — the catalog query is wrong: %v", len(order), order)
	}
	seen := map[string]bool{}
	for _, n := range order {
		if seen[n] {
			t.Fatalf("%s appears twice in the load order", n)
		}
		seen[n] = true
	}
	// goose's own bookkeeping is deliberately excluded: the migrations rebuild
	// it, and copying it would put that in two places.
	if seen["goose_db_version"] {
		t.Error("goose_db_version is in the load order")
	}
	// Spot-check the ordering the sort still buys, on edges that are not part
	// of any cycle.
	at := func(name string) int {
		for i, n := range order {
			if n == name {
				return i
			}
		}
		t.Fatalf("%s is not in the load order: %v", name, order)
		return -1
	}
	if at("teams") > at("projects") {
		t.Error("teams loads after projects, which references it")
	}
	if at("servers") > at("applications") {
		t.Error("servers loads after applications, which references it")
	}
}

// The restore's transaction must actually defer the constraints, or a load in
// any order fails the moment it reaches the first table of a cycle.
func TestStoreBackupLoadDefersEveryForeignKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	tx, err := s.BackupSurface().BeginLoad(ctx)
	if err != nil {
		t.Fatalf("BeginLoad: %v", err)
	}
	defer tx.Rollback(ctx)

	// A project pointing at an environment that does not exist yet — the exact
	// shape of the cycle that made snapshots impossible. Outside the
	// transaction this is an immediate violation; inside it, it must wait.
	row := copyRow(t, s, "projects", map[string]string{
		"id":                     "p_deferred",
		"name":                   "deferred",
		"team_id":                "tm_default",
		"slug":                   "deferred-" + ids.Secret()[:8],
		"default_environment_id": "e_does_not_exist",
	})
	if err := tx.CopyFrom(ctx, strings.NewReader(row), "projects"); err != nil {
		t.Fatalf("a deferred foreign key was enforced during the load: %v", err)
	}

	// And it must NOT be forgiven: committing without the environment has to
	// fail rather than leave a dangling pointer behind.
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("the restore committed a row whose foreign key was never satisfied")
	}
}

// copyRow builds one COPY TEXT line for a table from the LIVE column list, so a
// migration that adds a column does not silently turn this test into an
// assertion about the wrong field.
func copyRow(t *testing.T, s *Store, table string, values map[string]string) string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			t.Fatalf("scanning columns: %v", err)
		}
		if v, ok := values[name]; ok {
			out = append(out, v)
			continue
		}
		switch {
		case strings.Contains(kind, "timestamp"):
			out = append(out, "2026-01-01 00:00:00+00")
		case kind == "boolean":
			out = append(out, "f")
		case strings.Contains(kind, "int") || strings.Contains(kind, "numeric"):
			out = append(out, "0")
		case kind == "jsonb" || kind == "json":
			out = append(out, "{}")
		case strings.Contains(kind, "char") || kind == "text":
			out = append(out, "")
		default:
			out = append(out, `\N`)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the columns of %s: %v", table, err)
	}
	return strings.Join(out, "\t") + "\n"
}

// Two first-run claims that arrive together must not both create an owner.
// The lock is what the onboarding service counts and creates under.
func TestStoreSetupLockSerialisesClaims(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var inside int32
	var overlapped int32
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = s.WithSetupLock(ctx, func(context.Context) error {
				if atomic.AddInt32(&inside, 1) > 1 {
					atomic.StoreInt32(&overlapped, 1)
				}
				time.Sleep(20 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if atomic.LoadInt32(&overlapped) == 1 {
		t.Fatal("two claims ran inside the setup lock at once")
	}
}
