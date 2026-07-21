package docker

// Backup/restore executor tests (managed-databases.md §7): engine-derived
// commands, the archive-based transport (no exec stream framing, no exec
// stdin), and the round-trip of the exact bytes.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// fakeExec records commands and serves a fixed file for CopyFromContainer /
// captures CopyToContainer, standing in for the Docker engine.
type fakeExec struct {
	execCmds   [][]string
	dumpBody   []byte // what CopyFromContainer returns (the in-container dump)
	copiedIn   []byte // what CopyToContainer received (extracted from the tar)
	copiedDest string
	started    bool
	stopped    bool
	exitCode   int
}

func (f *fakeExec) ExecAndWait(_ context.Context, _ string, cmd []string) (int, []byte, error) {
	f.execCmds = append(f.execCmds, cmd)
	return f.exitCode, nil, nil
}

func (f *fakeExec) CopyFromContainer(_ context.Context, _, _ string) (io.ReadCloser, error) {
	// Return a tar containing the dump body under a single entry.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "dump", Mode: 0o600, Size: int64(len(f.dumpBody)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(f.dumpBody)
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

func (f *fakeExec) CopyToContainer(_ context.Context, _, dest string, tarStream io.Reader) error {
	f.copiedDest = dest
	tr := tar.NewReader(tarStream)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		f.copiedIn, _ = io.ReadAll(tr)
	}
	return nil
}

func (f *fakeExec) StartContainer(context.Context, string) error { f.started = true; return nil }
func (f *fakeExec) StopContainer(context.Context, string, time.Duration) error {
	f.stopped = true
	return nil
}
func (f *fakeExec) WaitHealthy(context.Context, string, time.Duration) error { return nil }

// fakeS3 captures the uploaded object and serves it back on download.
type fakeS3 struct {
	uploaded  map[string][]byte
	deleteErr map[string]bool // keys whose Delete should fail (prune partial-failure test)
	uploads   int
}

func newFakeS3() *fakeS3 { return &fakeS3{uploaded: map[string][]byte{}} }

func (s *fakeS3) Upload(_ context.Context, _, _, _, key, _, _ string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.uploaded[key] = data
	s.uploads++
	return nil
}

func (s *fakeS3) Download(_ context.Context, _, _, _, key, _, _ string) (io.ReadCloser, error) {
	data, ok := s.uploaded[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeS3) Delete(_ context.Context, _, _, _, key, _, _ string) error {
	if s.deleteErr != nil {
		if _, bad := s.deleteErr[key]; bad {
			return errors.New("delete failed")
		}
	}
	delete(s.uploaded, key)
	return nil
}

// A PostgreSQL backup dumps to a file, copies it out, gzips it, and uploads —
// and the uploaded object is the gzip of the exact dump bytes.
func TestBackupPostgresUploadsGzippedDump(t *testing.T) {
	eng := &fakeExec{dumpBody: []byte("-- pg_dumpall output\nCREATE TABLE t;\n")}
	s3 := newFakeS3()
	b := NewBackupExecutor(eng, s3, quietLog())

	ev := b.ExecuteBackup(context.Background(), &agentv1.DbBackupWork{
		BackupRecordId: "br_1", DbId: "db_1", ContainerName: "cypher-db-db_1",
		Engine: "postgresql", S3Key: "backups/db_1/t.gz",
	})
	if ev.GetOutcome() != agentv1.DbBackupEvent_OUTCOME_SUCCEEDED {
		t.Fatalf("outcome = %v (%s), want success", ev.GetOutcome(), ev.GetDetail())
	}
	// The dump command uses pg_dumpall redirected to the in-container file —
	// never a plane-supplied string.
	if got := strings.Join(eng.execCmds[0], " "); !strings.Contains(got, "pg_dumpall -U postgres > "+backupInContainerPath) {
		t.Fatalf("dump command = %q, want pg_dumpall redirect", got)
	}
	// The uploaded object gunzips back to the exact dump.
	gz, err := gzip.NewReader(bytes.NewReader(s3.uploaded["backups/db_1/t.gz"]))
	if err != nil {
		t.Fatalf("uploaded object is not gzip: %v", err)
	}
	got, _ := io.ReadAll(gz)
	if !bytes.Equal(got, eng.dumpBody) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, eng.dumpBody)
	}
}

// A restore downloads, copies the dump INTO the container, and runs the restore
// command that reads it — no exec stdin involved.
func TestRestorePostgresCopiesInAndRunsRestore(t *testing.T) {
	eng := &fakeExec{}
	s3 := newFakeS3()
	// Seed S3 with a gzipped dump.
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	_, _ = gw.Write([]byte("RESTORE ME"))
	_ = gw.Close()
	s3.uploaded["backups/db_1/t.gz"] = gzbuf.Bytes()

	b := NewBackupExecutor(eng, s3, quietLog())
	ev := b.ExecuteRestore(context.Background(), &agentv1.DbRestoreWork{
		RestoreId: "rs_1", DbId: "db_1", ContainerName: "cypher-db-db_1",
		Engine: "postgresql", S3Key: "backups/db_1/t.gz",
	})
	if ev.GetOutcome() != agentv1.DbRestoreEvent_OUTCOME_SUCCEEDED {
		t.Fatalf("outcome = %v (%s), want success", ev.GetOutcome(), ev.GetDetail())
	}
	if !bytes.Equal(eng.copiedIn, []byte("RESTORE ME")) {
		t.Fatalf("copied-in bytes = %q, want the decompressed dump", eng.copiedIn)
	}
	if got := strings.Join(eng.execCmds[0], " "); !strings.Contains(got, "psql -U postgres -f "+restoreInContainerPath) {
		t.Fatalf("restore command = %q, want psql -f the copied file", got)
	}
}

// Redis restore uses the RDB restart path: stop, copy dump.rdb into the data
// dir, start — no restore exec command.
func TestRestoreRedisUsesRestartPath(t *testing.T) {
	eng := &fakeExec{}
	s3 := newFakeS3()
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	_, _ = gw.Write([]byte("REDIS-RDB-BYTES"))
	_ = gw.Close()
	s3.uploaded["k.gz"] = gzbuf.Bytes()

	b := NewBackupExecutor(eng, s3, quietLog())
	ev := b.ExecuteRestore(context.Background(), &agentv1.DbRestoreWork{
		RestoreId: "rs_1", DbId: "db_1", ContainerName: "cypher-db-db_1",
		Engine: "redis", DataPath: "/data", S3Key: "k.gz",
	})
	if ev.GetOutcome() != agentv1.DbRestoreEvent_OUTCOME_SUCCEEDED {
		t.Fatalf("outcome = %v (%s), want success", ev.GetOutcome(), ev.GetDetail())
	}
	if !eng.stopped || !eng.started {
		t.Fatalf("redis restore must stop then start the container (stopped=%v started=%v)", eng.stopped, eng.started)
	}
	if eng.copiedDest != "/data/" {
		t.Fatalf("copied dest = %q, want /data/", eng.copiedDest)
	}
	if len(eng.execCmds) != 0 {
		t.Fatalf("redis restore ran %d exec commands, want 0 (restart path)", len(eng.execCmds))
	}
}

// A non-zero dump exit code fails the backup (and never uploads).
func TestBackupFailsOnNonZeroDump(t *testing.T) {
	eng := &fakeExec{exitCode: 1}
	s3 := newFakeS3()
	b := NewBackupExecutor(eng, s3, quietLog())
	ev := b.ExecuteBackup(context.Background(), &agentv1.DbBackupWork{
		BackupRecordId: "br_1", DbId: "db_1", ContainerName: "c", Engine: "postgresql", S3Key: "k",
	})
	if ev.GetOutcome() != agentv1.DbBackupEvent_OUTCOME_FAILED {
		t.Fatal("non-zero dump exit must fail the backup")
	}
	if s3.uploads != 0 {
		t.Fatal("a failed dump must not upload anything")
	}
}

func TestBackupUnsupportedEngine(t *testing.T) {
	b := NewBackupExecutor(&fakeExec{}, newFakeS3(), quietLog())
	ev := b.ExecuteBackup(context.Background(), &agentv1.DbBackupWork{Engine: "cockroach", ContainerName: "c"})
	if ev.GetOutcome() != agentv1.DbBackupEvent_OUTCOME_FAILED || !strings.Contains(ev.GetDetail(), "unsupported engine") {
		t.Fatalf("event = %+v, want unsupported-engine failure", ev)
	}
}

// ExecutePrune deletes the requested objects and reports exactly which ones it
// removed vs. left behind; deleting an absent key is a success (idempotent).
func TestExecutePrunePartialAndIdempotent(t *testing.T) {
	s3 := newFakeS3()
	s3.uploaded["keep"] = []byte("x")
	s3.uploaded["a"] = []byte("x")
	s3.uploaded["b"] = []byte("x")
	s3.deleteErr = map[string]bool{"b": true} // b fails
	b := NewBackupExecutor(&fakeExec{}, s3, quietLog())

	// "a" exists (deletes), "b" fails, "gone" is already absent (idempotent OK).
	ev := b.ExecutePrune(context.Background(), &agentv1.DbBackupPruneWork{
		DbId: "db_1", S3Keys: []string{"a", "b", "gone"},
	})
	if got := ev.GetDeletedKeys(); len(got) != 2 || !contains(got, "a") || !contains(got, "gone") {
		t.Fatalf("deleted = %v, want [a gone]", got)
	}
	if got := ev.GetFailedKeys(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("failed = %v, want [b]", got)
	}
	if _, ok := s3.uploaded["a"]; ok {
		t.Fatal("object a should have been deleted from S3")
	}
	if _, ok := s3.uploaded["keep"]; !ok {
		t.Fatal("unrelated object keep must survive")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
