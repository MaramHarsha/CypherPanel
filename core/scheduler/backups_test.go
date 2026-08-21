package scheduler

// Backup orchestration tests (managed-databases.md §7): RunBackup records a
// running row and publishes engine-only work; the event completes the record;
// restore is confirm-gated.

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

func seedBackupFixture(fs *fakeStore) (dbID, schedID string) {
	fs.dbs["db_1"] = domain.Database{ID: "db_1", ServerID: "srv_1", Engine: domain.EnginePostgreSQL, DataPath: "/var/lib/postgresql/data"}
	fs.targets["bt_1"] = domain.BackupTarget{
		ID: "bt_1", Endpoint: "http://minio:9000", Bucket: "b", Region: "r",
		AccessKeyCT: []byte("sealed:AK"), AccessKeyNonce: []byte("n"),
		SecretKeyCT: []byte("sealed:SK"), SecretKeyNonce: []byte("n"), PathPrefix: "pfx",
	}
	fs.schedules["bak_1"] = domain.DatabaseBackup{ID: "bak_1", DatabaseID: "db_1", TargetID: "bt_1", RetentionCount: 7}
	return "db_1", "bak_1"
}

func TestRunBackupRecordsAndPublishesEngineOnly(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	_, schedID := seedBackupFixture(fs)
	s := newScheduler(fs, fb)

	rec, err := s.RunBackup(context.Background(), schedID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	if rec.Status != domain.BackupRunning {
		t.Fatalf("record status = %s, want running", rec.Status)
	}
	p, ok := fb.last()
	if !ok || p.subject != subjects.DbBackup("srv_1") {
		t.Fatalf("published on %+v, want %s", p, subjects.DbBackup("srv_1"))
	}
	var w agentv1.DbBackupWork
	if err := proto.Unmarshal(p.data, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Engine-derived: the work carries the engine, the unsealed S3 creds, and a
	// key under the target prefix — but never a shell command or db password.
	if w.GetEngine() != "postgresql" {
		t.Fatalf("engine = %q, want postgresql", w.GetEngine())
	}
	if w.GetS3AccessKey() != "AK" || w.GetS3SecretKey() != "SK" {
		t.Fatalf("S3 creds not unsealed onto the work item: %q/%q", w.GetS3AccessKey(), w.GetS3SecretKey())
	}
	if !strings.HasPrefix(w.GetS3Key(), "pfx/db_1/") || !strings.HasSuffix(w.GetS3Key(), ".gz") {
		t.Fatalf("s3 key = %q, want pfx/db_1/<ts>.gz", w.GetS3Key())
	}
}

func TestHandleBackupEventCompletesRecord(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	_, schedID := seedBackupFixture(fs)
	s := newScheduler(fs, fb)

	rec, err := s.RunBackup(context.Background(), schedID)
	if err != nil {
		t.Fatalf("RunBackup: %v", err)
	}
	s.HandleDbBackupEvent(context.Background(), "srv_1", &agentv1.DbBackupEvent{
		BackupRecordId: rec.ID, DbId: "db_1",
		Outcome: agentv1.DbBackupEvent_OUTCOME_SUCCEEDED, ObjectKey: "pfx/db_1/x.gz", SizeBytes: 2048,
	})
	got, err := fs.GetBackupRecord(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("GetBackupRecord: %v", err)
	}
	if got.Status != domain.BackupSucceeded || got.SizeBytes != 2048 {
		t.Fatalf("record = %+v, want succeeded with size 2048", got)
	}
	if got.FinishedAt == nil {
		t.Fatal("succeeded record must have finished_at set")
	}
}

// A successful backup beyond the retention window dispatches a prune of the
// oldest S3 objects, and the prune event confirming deletion removes exactly
// those rows — while a key the agent could not delete keeps its row for the
// next sweep (self-healing).
func TestRetentionPruneDispatchAndConfirm(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	seedBackupFixture(fs) // retention_count = 7
	s := newScheduler(fs, fb)

	// Nine succeeded backups; keys "k0" (oldest) … "k8" (newest).
	base := time.Now().Add(-time.Hour)
	for i := range 9 {
		id := "br_" + string(rune('a'+i))
		fs.records[id] = domain.BackupRecord{
			ID: id, DatabaseBackupID: "bak_1", ObjectKey: "k" + string(rune('0'+i)),
			Status: domain.BackupSucceeded, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
	}

	// A fresh success triggers the retention sweep. Beyond the newest 7 are the
	// two oldest: k0 and k1.
	s.HandleDbBackupEvent(context.Background(), "srv_1", &agentv1.DbBackupEvent{
		BackupRecordId: "br_a", DbId: "db_1",
		Outcome: agentv1.DbBackupEvent_OUTCOME_SUCCEEDED, ObjectKey: "k0",
	})
	p, ok := fb.last()
	if !ok || p.subject != subjects.DbBackupPrune("srv_1") {
		t.Fatalf("prune published on %+v, want %s", p, subjects.DbBackupPrune("srv_1"))
	}
	var w agentv1.DbBackupPruneWork
	if err := proto.Unmarshal(p.data, &w); err != nil {
		t.Fatalf("unmarshal prune work: %v", err)
	}
	if len(w.GetS3Keys()) != 2 || !hasKey(w.GetS3Keys(), "k0") || !hasKey(w.GetS3Keys(), "k1") {
		t.Fatalf("prune keys = %v, want the two oldest [k0 k1]", w.GetS3Keys())
	}
	if w.GetS3AccessKey() != "AK" || w.GetS3SecretKey() != "SK" {
		t.Fatalf("prune work missing unsealed S3 creds: %q/%q", w.GetS3AccessKey(), w.GetS3SecretKey())
	}

	// Agent confirms k0 deleted, k1 failed → k0's row goes, k1's row stays.
	s.HandleDbBackupPruneEvent(context.Background(), "srv_1", &agentv1.DbBackupPruneEvent{
		DbId: "db_1", DeletedKeys: []string{"k0"}, FailedKeys: []string{"k1"},
	})
	if _, err := fs.GetBackupRecord(context.Background(), "br_a"); err == nil {
		t.Fatal("record for deleted key k0 should be gone")
	}
	if _, err := fs.GetBackupRecord(context.Background(), "br_b"); err != nil {
		t.Fatal("record for failed key k1 must survive for the next sweep")
	}
}

func hasKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestRestoreRequiresConfirm(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	seedBackupFixture(fs)
	fs.records["br_1"] = domain.BackupRecord{ID: "br_1", DatabaseBackupID: "bak_1", ObjectKey: "pfx/db_1/x.gz", Status: domain.BackupSucceeded}
	s := newScheduler(fs, fb)

	if err := s.RunRestore(context.Background(), "db_1", "br_1", false); err != ErrRestoreNotConfirmed {
		t.Fatalf("unconfirmed restore err = %v, want ErrRestoreNotConfirmed", err)
	}
	if fb.count() != 0 {
		t.Fatal("unconfirmed restore must publish no work")
	}
	if err := s.RunRestore(context.Background(), "db_1", "br_1", true); err != nil {
		t.Fatalf("confirmed restore: %v", err)
	}
	if p, _ := fb.last(); p.subject != subjects.DbRestore("srv_1") {
		t.Fatalf("restore published on %s, want %s", p.subject, subjects.DbRestore("srv_1"))
	}
}

// The sweeper fires a schedule whose next cron time (from last run, or creation
// if never run) has passed, and leaves not-yet-due ones alone.
func TestSweepDueBackupsFiresOnlyDue(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	seedBackupFixture(fs)
	// The clock is pinned, and every fixture time is relative to it.
	//
	// Against a real clock this test asserted something that is only true for
	// most of the day: bak_2 is a daily 3am schedule whose last run is five
	// minutes ago, so between 03:00 and 03:05 its next run lands in the past
	// and the sweep correctly fires both. The sweep was right and the test was
	// wrong, once a day, for five minutes — which is exactly how it failed in
	// CI rather than here.
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	// due: every-minute schedule created two minutes ago, never run.
	due := fs.schedules["bak_1"]
	due.Schedule = "* * * * *"
	due.Enabled = true
	due.CreatedAt = now.Add(-2 * time.Minute)
	fs.schedules["bak_1"] = due
	// not due: daily-at-3am schedule that ran five minutes ago.
	ran := now.Add(-5 * time.Minute)
	fs.schedules["bak_2"] = domain.DatabaseBackup{
		ID: "bak_2", DatabaseID: "db_1", TargetID: "bt_1", RetentionCount: 7,
		Schedule: "0 3 * * *", Enabled: true, LastRunAt: &ran,
	}
	s := newScheduler(fs, fb)
	s.now = func() time.Time { return now }

	s.SweepDueBackups(context.Background())

	// Exactly one backup published (bak_1), and bak_1's last run advanced.
	var published int
	for _, p := range fb.work {
		if p.subject == subjects.DbBackup("srv_1") {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("published %d backups, want 1 (only the due schedule)", published)
	}
	if fs.schedules["bak_1"].LastRunAt == nil {
		t.Fatal("due schedule's last_run_at not advanced (would double-fire next sweep)")
	}
}

// Disabled and manual-only (” schedule) entries are never fired, and a bad
// cron expression is skipped without stalling the sweep.
func TestSweepSkipsDisabledManualAndBadCron(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	seedBackupFixture(fs)
	old := time.Now().Add(-time.Hour)
	fs.schedules["bak_disabled"] = domain.DatabaseBackup{ID: "bak_disabled", DatabaseID: "db_1", TargetID: "bt_1", Schedule: "* * * * *", Enabled: false, CreatedAt: old}
	fs.schedules["bak_manual"] = domain.DatabaseBackup{ID: "bak_manual", DatabaseID: "db_1", TargetID: "bt_1", Schedule: "", Enabled: true, CreatedAt: old}
	fs.schedules["bak_bad"] = domain.DatabaseBackup{ID: "bak_bad", DatabaseID: "db_1", TargetID: "bt_1", Schedule: "not a cron", Enabled: true, CreatedAt: old}
	// The seeded bak_1 has no schedule string → manual only → not fired either.
	s := newScheduler(fs, fb)

	s.SweepDueBackups(context.Background())

	for _, p := range fb.work {
		if p.subject == subjects.DbBackup("srv_1") {
			t.Fatalf("a disabled/manual/bad-cron schedule was fired: %+v", p)
		}
	}
}
