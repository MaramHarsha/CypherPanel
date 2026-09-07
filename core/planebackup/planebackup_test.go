package planebackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeDB is a table set with COPY bodies.
type fakeDB struct {
	tables map[string]string
	order  []string
	loaded map[string]string
	schema int64
	empty  bool
}

func (f *fakeDB) Tables(context.Context) ([]string, error) { return f.order, nil }
func (f *fakeDB) CopyTo(_ context.Context, w io.Writer, table string) (int64, error) {
	body, ok := f.tables[table]
	if !ok {
		return 0, errors.New("no such table")
	}
	if _, err := io.WriteString(w, body); err != nil {
		return 0, err
	}
	return int64(strings.Count(body, "\n")), nil
}
func (f *fakeDB) CopyFrom(_ context.Context, r io.Reader, table string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if f.loaded == nil {
		f.loaded = map[string]string{}
	}
	f.loaded[table] = string(body)
	return nil
}
func (f *fakeDB) SchemaVersion(context.Context) (int64, error) { return f.schema, nil }
func (f *fakeDB) IsEmpty(context.Context) (bool, error)        { return f.empty, nil }

type fakeMigrator struct {
	upTo    int64
	wentUp  bool
	current int64
}

func (m *fakeMigrator) UpTo(_ context.Context, v int64) error { m.upTo = v; return nil }
func (m *fakeMigrator) Up(context.Context) error              { m.wentUp = true; return nil }
func (m *fakeMigrator) Current() int64                        { return m.current }

func sampleDB() *fakeDB {
	return &fakeDB{
		order:  []string{"teams", "projects", "applications"},
		schema: 48,
		empty:  true,
		tables: map[string]string{
			"teams":        "tm_1\tAcme\n",
			"projects":     "prj_1\ttm_1\tacme-website\n",
			"applications": "app_1\tenv_1\tapi\n",
		},
	}
}

// The property the whole feature rests on: an archive written by this panel,
// with a key only the operator has, opens back into the same rows.
func TestAnArchiveRoundTrips(t *testing.T) {
	recipient, identity, err := GenerateRecoveryKey()
	if err != nil {
		t.Fatalf("GenerateRecoveryKey: %v", err)
	}
	src := sampleDB()

	var buf bytes.Buffer
	man, err := Export(context.Background(), src, AgeCrypto{}, &buf, recipient, "the-master-key", "v0.4.0")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(man.Tables) != 3 || man.SchemaVersion != 48 {
		t.Fatalf("manifest = %+v", man)
	}

	dst := &fakeDB{order: src.order, empty: true}
	mig := &fakeMigrator{current: 60}
	res, err := Restore(context.Background(), &buf, RestoreOptions{
		DB: dst, Enc: AgeCrypto{}, Migrate: mig, Identity: identity,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for name, body := range src.tables {
		if dst.loaded[name] != body {
			t.Errorf("%s round-tripped as %q, want %q", name, dst.loaded[name], body)
		}
	}
	// The master key travels INSIDE, because a snapshot without it is a
	// database nobody can open: the CA is sealed with it and the plane fails
	// closed on a wrong one.
	if res.MasterKey != "the-master-key" {
		t.Errorf("master key = %q; a snapshot without it is worthless", res.MasterKey)
	}
	// The schema is rebuilt to the SNAPSHOT's version, then carried forward
	// through the ordinary path.
	if mig.upTo != 48 {
		t.Errorf("migrated to %d, want the snapshot's own 48", mig.upTo)
	}
	if !mig.wentUp {
		t.Error("the restore did not migrate forward to this build")
	}
}

// The archive is worthless to anyone but the key holder — which is the point,
// because the master key is inside it and an unencrypted plane snapshot in a
// bucket is strictly worse than the database it came from.
func TestAnotherKeyCannotOpenTheArchive(t *testing.T) {
	recipient, _, _ := GenerateRecoveryKey()
	_, otherIdentity, _ := GenerateRecoveryKey()

	var buf bytes.Buffer
	if _, err := Export(context.Background(), sampleDB(), AgeCrypto{}, &buf, recipient, "k", "v0.4.0"); err != nil {
		t.Fatalf("Export: %v", err)
	}
	_, err := Restore(context.Background(), &buf, RestoreOptions{
		DB: &fakeDB{empty: true}, Enc: AgeCrypto{}, Migrate: &fakeMigrator{current: 60},
		Identity: otherIdentity,
	})
	if err == nil {
		t.Fatal("a different recovery key opened the archive")
	}
}

// Restoring over a live panel is how a fleet ends up with two half-planes.
func TestANonEmptyTargetIsRefusedWithoutForce(t *testing.T) {
	recipient, identity, _ := GenerateRecoveryKey()
	var buf bytes.Buffer
	if _, err := Export(context.Background(), sampleDB(), AgeCrypto{}, &buf, recipient, "k", "v0.4.0"); err != nil {
		t.Fatalf("Export: %v", err)
	}
	body := buf.Bytes()

	_, err := Restore(context.Background(), bytes.NewReader(body), RestoreOptions{
		DB: &fakeDB{empty: false}, Enc: AgeCrypto{}, Migrate: &fakeMigrator{current: 60}, Identity: identity,
	})
	if !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("a non-empty target was accepted: %v", err)
	}

	dst := &fakeDB{order: []string{"teams", "projects", "applications"}, empty: false}
	if _, err := Restore(context.Background(), bytes.NewReader(body), RestoreOptions{
		DB: dst, Enc: AgeCrypto{}, Migrate: &fakeMigrator{current: 60}, Identity: identity, Force: true,
	}); err != nil {
		t.Fatalf("--force was refused: %v", err)
	}
}

// A snapshot from a newer panel cannot be restored by this binary, and the
// honest answer is one line long — naming the version to install.
func TestASnapshotFromANewerPanelIsRefusedByNumber(t *testing.T) {
	recipient, identity, _ := GenerateRecoveryKey()
	src := sampleDB()
	src.schema = 99

	var buf bytes.Buffer
	if _, err := Export(context.Background(), src, AgeCrypto{}, &buf, recipient, "k", "v9.9.9"); err != nil {
		t.Fatalf("Export: %v", err)
	}
	_, err := Restore(context.Background(), &buf, RestoreOptions{
		DB: &fakeDB{empty: true}, Enc: AgeCrypto{}, Migrate: &fakeMigrator{current: 48}, Identity: identity,
	})
	if !errors.Is(err, ErrNewerSnapshot) {
		t.Fatalf("a newer snapshot was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "v9.9.9") {
		t.Errorf("the refusal does not name the version to install: %v", err)
	}
}

// Load order is DERIVED from the catalog, so a new table or a new foreign key
// needs no edit anywhere.
func TestLoadOrderPutsParentsFirst(t *testing.T) {
	order, err := SortTables(
		[]string{"applications", "environments", "projects", "teams"},
		map[string][]string{
			"applications": {"environments"},
			"environments": {"projects"},
			"projects":     {"teams"},
		})
	if err != nil {
		t.Fatalf("SortTables: %v", err)
	}
	pos := map[string]int{}
	for i, t := range order {
		pos[t] = i
	}
	for child, parents := range map[string][]string{
		"applications": {"environments"}, "environments": {"projects"}, "projects": {"teams"},
	} {
		for _, p := range parents {
			if pos[p] > pos[child] {
				t.Errorf("%s loads before its parent %s: %v", child, p, order)
			}
		}
	}
}

// A self-reference orders within one COPY and must not read as a cycle.
func TestASelfReferenceIsNotACycle(t *testing.T) {
	if _, err := SortTables([]string{"nodes"}, map[string][]string{"nodes": {"nodes"}}); err != nil {
		t.Fatalf("a self-referencing table was rejected: %v", err)
	}
}

// A real cycle fails LOUDLY at export time — in CI, rather than during a
// recovery.
func TestACycleFailsAtExportTime(t *testing.T) {
	_, err := SortTables([]string{"a", "b"}, map[string][]string{"a": {"b"}, "b": {"a"}})
	if err == nil {
		t.Fatal("a cyclic foreign key graph sorted without complaint")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("the failure does not say what is wrong: %v", err)
	}
}

// The panel stores only a public key, so a stored recipient has to be one.
func TestOnlyAnAgePublicKeyIsAcceptedAsARecipient(t *testing.T) {
	recipient, identity, _ := GenerateRecoveryKey()
	if !ValidRecipient(recipient) {
		t.Error("a generated recipient was rejected")
	}
	// The PRIVATE half must not be accepted as a recipient: storing it would
	// undo the entire asymmetric design.
	if ValidRecipient(identity) {
		t.Error("a private key was accepted as a recipient — the plane must only ever be able to write")
	}
	for _, bad := range []string{"", "not-a-key", "age1", "ssh-ed25519 AAAA"} {
		if ValidRecipient(bad) {
			t.Errorf("ValidRecipient(%q) = true", bad)
		}
	}
}
