package scheduler

// Backup orchestration tests (managed-databases.md §7): RunBackup records a
// running row and publishes engine-only work; the event completes the record;
// restore is confirm-gated.

import (
	"context"
	"strings"
	"testing"

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
