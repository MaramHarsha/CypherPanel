package databases

// Partial edits of backup targets and schedules (managed-databases.md §"Backup
// API"). Both were specified from the start and neither was reachable, so these
// tests fix the two behaviours a PATCH has to get right: an omitted field is
// left alone, and the merged result is held to the same bar as a create.

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeBackupStore satisfies both BackupTargetStore and BackupScheduleStore with
// two maps. Sealing is core/secret's job; sealer here just tags the plaintext so
// a test can prove which credential was written.
type fakeBackupStore struct {
	targets   map[string]domain.BackupTarget
	schedules map[string]domain.DatabaseBackup
	databases map[string]domain.Database
}

func newFakeBackupStore() *fakeBackupStore {
	return &fakeBackupStore{
		targets:   map[string]domain.BackupTarget{},
		schedules: map[string]domain.DatabaseBackup{},
		databases: map[string]domain.Database{"db_1": {ID: "db_1", Name: "atlas-pg"}},
	}
}

func (f *fakeBackupStore) CreateBackupTarget(_ context.Context, t domain.BackupTarget) (domain.BackupTarget, error) {
	f.targets[t.ID] = t
	return t, nil
}

func (f *fakeBackupStore) GetBackupTarget(_ context.Context, id string) (domain.BackupTarget, error) {
	t, ok := f.targets[id]
	if !ok {
		return domain.BackupTarget{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeBackupStore) ListBackupTargets(context.Context) ([]domain.BackupTarget, error) {
	out := make([]domain.BackupTarget, 0, len(f.targets))
	for _, t := range f.targets {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeBackupStore) UpdateBackupTarget(_ context.Context, t domain.BackupTarget) (domain.BackupTarget, error) {
	if _, ok := f.targets[t.ID]; !ok {
		return domain.BackupTarget{}, store.ErrNotFound
	}
	f.targets[t.ID] = t
	return t, nil
}

func (f *fakeBackupStore) DeleteBackupTarget(_ context.Context, id string) error {
	delete(f.targets, id)
	return nil
}

func (f *fakeBackupStore) CreateDatabaseBackup(_ context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	f.schedules[b.ID] = b
	return b, nil
}

func (f *fakeBackupStore) GetDatabaseBackup(_ context.Context, id string) (domain.DatabaseBackup, error) {
	b, ok := f.schedules[id]
	if !ok {
		return domain.DatabaseBackup{}, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeBackupStore) ListDatabaseBackups(_ context.Context, dbID string) ([]domain.DatabaseBackup, error) {
	var out []domain.DatabaseBackup
	for _, b := range f.schedules {
		if b.DatabaseID == dbID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeBackupStore) UpdateDatabaseBackup(_ context.Context, b domain.DatabaseBackup) (domain.DatabaseBackup, error) {
	if _, ok := f.schedules[b.ID]; !ok {
		return domain.DatabaseBackup{}, store.ErrNotFound
	}
	f.schedules[b.ID] = b
	return b, nil
}

func (f *fakeBackupStore) DeleteDatabaseBackup(_ context.Context, id string) error {
	delete(f.schedules, id)
	return nil
}

func (f *fakeBackupStore) GetDatabase(_ context.Context, id string) (domain.Database, error) {
	d, ok := f.databases[id]
	if !ok {
		return domain.Database{}, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeBackupStore) ListBackupRecords(context.Context, string) ([]domain.BackupRecord, error) {
	return nil, nil
}

// tagSealer records plaintext with a marker so a test can read back which
// credential was stored without depending on real encryption.
type tagSealer struct{}

func (tagSealer) Seal(pt []byte) ([]byte, []byte, error) {
	return append([]byte("sealed:"), pt...), []byte("n"), nil
}
func (tagSealer) Open(ct, _ []byte) ([]byte, error) { return ct, nil }

func str(s string) *string { return &s }

func seedTarget(t *testing.T, svc *BackupTargetService) domain.BackupTarget {
	t.Helper()
	tgt, err := svc.CreateTarget(context.Background(), BackupTargetInput{
		Name: "b2-backups", Endpoint: "https://s3.example.test", Bucket: "backups",
		Region: "eu-central-1", AccessKey: "AK", SecretKey: "SK", PathPrefix: "prod/",
	})
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return tgt
}

// TestUpdateTargetLeavesOmittedFieldsAlone is the reason this is a PATCH: an
// operator rotating one key must not have to re-send the other, or restate the
// bucket to change the region.
func TestUpdateTargetLeavesOmittedFieldsAlone(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupTargetService(st, tagSealer{})
	tgt := seedTarget(t, svc)

	got, err := svc.UpdateTarget(context.Background(), tgt.ID, UpdateTargetInput{Region: str("us-east-1")})
	if err != nil {
		t.Fatalf("UpdateTarget: %v", err)
	}
	if got.Region != "us-east-1" {
		t.Fatalf("region = %q, want the new one", got.Region)
	}
	if got.Name != "b2-backups" || got.Bucket != "backups" || got.Endpoint != "https://s3.example.test" || got.PathPrefix != "prod/" {
		t.Fatalf("an omitted field changed: %+v", got)
	}
	// Untouched credentials keep their existing ciphertext rather than being
	// resealed — or, worse, blanked.
	if string(got.AccessKeyCT) != "sealed:AK" || string(got.SecretKeyCT) != "sealed:SK" {
		t.Fatalf("credentials changed on an edit that did not send them: %q / %q", got.AccessKeyCT, got.SecretKeyCT)
	}
}

// TestUpdateTargetResealsOnlyTheCredentialSent: rotating one key must not
// disturb the other.
func TestUpdateTargetResealsOnlyTheCredentialSent(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupTargetService(st, tagSealer{})
	tgt := seedTarget(t, svc)

	got, err := svc.UpdateTarget(context.Background(), tgt.ID, UpdateTargetInput{SecretKey: str("SK2")})
	if err != nil {
		t.Fatalf("UpdateTarget: %v", err)
	}
	if string(got.SecretKeyCT) != "sealed:SK2" {
		t.Fatalf("secret key = %q, want the rotated one", got.SecretKeyCT)
	}
	if string(got.AccessKeyCT) != "sealed:AK" {
		t.Fatalf("access key = %q, want the original", got.AccessKeyCT)
	}
}

// TestUpdateTargetRejectsEmptyingARequiredField: a PATCH must not be able to
// leave a target in a state CreateTarget would have refused.
func TestUpdateTargetRejectsEmptyingARequiredField(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupTargetService(st, tagSealer{})
	tgt := seedTarget(t, svc)

	for _, in := range []UpdateTargetInput{
		{Name: str("")},
		{Endpoint: str("")},
		{Bucket: str("")},
		{AccessKey: str("")},
		{SecretKey: str("")},
	} {
		if _, err := svc.UpdateTarget(context.Background(), tgt.ID, in); err == nil {
			t.Fatalf("UpdateTarget(%+v) was accepted, want a validation error", in)
		}
	}
	// The stored row is untouched by a refused edit.
	if got, _ := svc.GetTarget(context.Background(), tgt.ID); got.Name != "b2-backups" {
		t.Fatalf("a refused edit still changed the target: %+v", got)
	}
}

func TestUpdateTargetOnAMissingTarget(t *testing.T) {
	svc := NewBackupTargetService(newFakeBackupStore(), tagSealer{})
	_, err := svc.UpdateTarget(context.Background(), "bt_nope", UpdateTargetInput{Region: str("x")})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func seedSchedule(t *testing.T, st *fakeBackupStore, svc *BackupScheduleService) domain.DatabaseBackup {
	t.Helper()
	st.targets["bt_1"] = domain.BackupTarget{ID: "bt_1", Name: "b2"}
	st.targets["bt_2"] = domain.BackupTarget{ID: "bt_2", Name: "glacier"}
	enabled := true
	b, err := svc.CreateSchedule(context.Background(), "db_1", BackupScheduleInput{
		TargetID: "bt_1", Schedule: "0 3 * * *", RetentionCount: 14, Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return b
}

// TestUpdateSchedulePausesWithoutForgetting: the common edit is "stop running
// this for now", and it must not disturb where the schedule wrote or how much
// it keeps.
func TestUpdateSchedulePausesWithoutForgetting(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupScheduleService(st)
	b := seedSchedule(t, st, svc)

	off := false
	got, err := svc.UpdateSchedule(context.Background(), b.ID, UpdateScheduleInput{Enabled: &off})
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if got.Enabled {
		t.Fatal("the schedule is still enabled after being paused")
	}
	if got.TargetID != "bt_1" || got.Schedule != "0 3 * * *" || got.RetentionCount != 14 {
		t.Fatalf("pausing changed something else: %+v", got)
	}
}

// TestUpdateScheduleRejectsAnUnknownTarget: a target that does not exist is a
// validation failure the caller can act on, not a foreign-key error surfacing
// from the database.
func TestUpdateScheduleRejectsAnUnknownTarget(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupScheduleService(st)
	b := seedSchedule(t, st, svc)

	_, err := svc.UpdateSchedule(context.Background(), b.ID, UpdateScheduleInput{TargetID: str("bt_nope")})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}

	// A real target moves the schedule.
	got, err := svc.UpdateSchedule(context.Background(), b.ID, UpdateScheduleInput{TargetID: str("bt_2")})
	if err != nil {
		t.Fatalf("UpdateSchedule to a real target: %v", err)
	}
	if got.TargetID != "bt_2" {
		t.Fatalf("target = %q, want bt_2", got.TargetID)
	}
}

// TestUpdateScheduleRetentionFloor: zero or less means "unset", and unset must
// mean the same thing here as it does at creation, or the two paths disagree
// about how much history exists.
func TestUpdateScheduleRetentionFloor(t *testing.T) {
	st := newFakeBackupStore()
	svc := NewBackupScheduleService(st)
	b := seedSchedule(t, st, svc)

	for _, n := range []int{0, -5} {
		got, err := svc.UpdateSchedule(context.Background(), b.ID, UpdateScheduleInput{RetentionCount: &n})
		if err != nil {
			t.Fatalf("UpdateSchedule(retention=%d): %v", n, err)
		}
		if got.RetentionCount != 7 {
			t.Fatalf("retention with %d = %d, want the creation default of 7", n, got.RetentionCount)
		}
	}
}
