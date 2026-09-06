package store

// Real-Postgres tests for restore records (managed-databases.md §"Restoring").
//
// The rule worth pinning: only a running restore moves. Both the progress
// update and the finish are scoped to status='running', so a redelivered event
// — an agent that reconnected, a message replayed — cannot reopen a restore
// that already ended or re-decide its outcome.

import (
	"context"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// seedRestorable creates a database and a succeeded backup record to restore
// from, returning both ids.
func seedRestorable(t *testing.T, s *Store) (dbID, recordID string) {
	t.Helper()
	ctx := context.Background()
	srv, _, env, _ := seedApp(t, s)

	id := ids.New(ids.PrefixDatabase)
	db, err := s.CreateDatabaseWithRevision(ctx, domain.Database{
		ID:            id,
		EnvironmentID: env.ID,
		Name:          "pg-" + ids.Secret()[:6],
		Engine:        domain.EnginePostgreSQL,
		Version:       "16",
		ServerID:      srv.ID,
		VolumeName:    "cypher-db-" + id,
		DataPath:      "/var/lib/postgresql/data",
		Network:       "cypher-" + env.ID,
		RootUser:      "postgres",
		Status:        domain.DbStopped,
	}, domain.DatabaseRevision{
		ID:             ids.New(ids.PrefixDatabaseRevision),
		DatabaseID:     id,
		ConfigSnapshot: []byte(`{"version": "16"}`),
	})
	if err != nil {
		t.Fatalf("CreateDatabaseWithRevision: %v", err)
	}
	// The backup record is left empty: these tests are about the restore
	// record's own lifecycle, and the column is nullable precisely because a
	// restore outlives the backup it came from.
	return db.ID, ""
}

func TestStoreRestoreLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	dbID, recordID := seedRestorable(t, s)

	rec, err := s.CreateDatabaseRestore(ctx, ids.New(ids.PrefixDatabaseRestore), dbID, recordID, domain.RestoreStepFetching)
	if err != nil {
		t.Fatalf("CreateDatabaseRestore: %v", err)
	}
	if rec.Status != domain.RestoreRunning || rec.Step != domain.RestoreStepFetching {
		t.Fatalf("new restore = %+v, want running/fetching", rec)
	}
	if rec.FinishedAt != nil {
		t.Fatal("a new restore already has a finished_at")
	}

	// It is findable as the database's running restore, which is what a
	// reopened page asks for.
	running, err := s.RunningDatabaseRestore(ctx, dbID)
	if err != nil || running.ID != rec.ID {
		t.Fatalf("RunningDatabaseRestore = %+v, %v; want %s", running, err, rec.ID)
	}

	advanced, err := s.AdvanceDatabaseRestore(ctx, rec.ID, domain.RestoreStepApplying, 148, 212)
	if err != nil {
		t.Fatalf("AdvanceDatabaseRestore: %v", err)
	}
	if advanced.Step != domain.RestoreStepApplying || advanced.BytesDone != 148 || advanced.BytesTotal != 212 {
		t.Fatalf("advanced = %+v, want applying 148/212", advanced)
	}

	done, err := s.FinishDatabaseRestore(ctx, rec.ID, domain.RestoreSucceeded, "")
	if err != nil {
		t.Fatalf("FinishDatabaseRestore: %v", err)
	}
	if done.Status != domain.RestoreSucceeded || done.FinishedAt == nil {
		t.Fatalf("finished = %+v, want succeeded with a finished_at", done)
	}

	// Nothing is running any more.
	if _, err := s.RunningDatabaseRestore(ctx, dbID); err == nil {
		t.Fatal("a finished restore is still reported as running")
	}
}

// A redelivered terminal event must not re-decide the outcome, and a late
// progress event must not reopen a closed restore.
func TestStoreRestoreIgnoresLateEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	dbID, recordID := seedRestorable(t, s)

	rec, err := s.CreateDatabaseRestore(ctx, ids.New(ids.PrefixDatabaseRestore), dbID, recordID, domain.RestoreStepFetching)
	if err != nil {
		t.Fatalf("CreateDatabaseRestore: %v", err)
	}
	if _, err := s.FinishDatabaseRestore(ctx, rec.ID, domain.RestoreSucceeded, ""); err != nil {
		t.Fatalf("FinishDatabaseRestore: %v", err)
	}

	// A second finish, with a different answer, is refused rather than applied.
	if _, err := s.FinishDatabaseRestore(ctx, rec.ID, domain.RestoreFailed, "late failure"); err == nil {
		t.Fatal("a closed restore was finished a second time")
	}
	// A late progress event does not reopen it.
	if _, err := s.AdvanceDatabaseRestore(ctx, rec.ID, domain.RestoreStepApplying, 1, 2); err == nil {
		t.Fatal("a closed restore accepted a progress update")
	}

	got, err := s.GetDatabaseRestore(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetDatabaseRestore: %v", err)
	}
	if got.Status != domain.RestoreSucceeded || got.Detail != "" {
		t.Fatalf("restore = %+v, want the first outcome untouched", got)
	}
}

// History is newest first, which is the order the page renders.
func TestStoreListDatabaseRestoresNewestFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	dbID, recordID := seedRestorable(t, s)

	var ids2 []string
	for range 3 {
		rec, err := s.CreateDatabaseRestore(ctx, ids.New(ids.PrefixDatabaseRestore), dbID, recordID, domain.RestoreStepFetching)
		if err != nil {
			t.Fatalf("CreateDatabaseRestore: %v", err)
		}
		if _, err := s.FinishDatabaseRestore(ctx, rec.ID, domain.RestoreSucceeded, ""); err != nil {
			t.Fatalf("FinishDatabaseRestore: %v", err)
		}
		ids2 = append(ids2, rec.ID)
		time.Sleep(3 * time.Millisecond) // started_at is the sort key
	}

	list, err := s.ListDatabaseRestores(ctx, dbID, 50)
	if err != nil {
		t.Fatalf("ListDatabaseRestores: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d restores, want 3", len(list))
	}
	if list[0].ID != ids2[2] {
		t.Fatalf("first listed = %s, want the newest %s", list[0].ID, ids2[2])
	}
}
