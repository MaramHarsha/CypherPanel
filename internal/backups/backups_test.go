package backups

import (
	"context"
	"strings"
	"testing"
)

func TestRetentionIsZero(t *testing.T) {
	tests := []struct {
		name string
		r    Retention
		want bool
	}{
		{"all zero", Retention{}, true},
		{"negative counts are still nothing", Retention{Daily: -1, Weekly: -2}, true},
		{"one daily is enough", Retention{Daily: 1}, false},
		{"monthly only", Retention{Monthly: 6}, false},
		{"full policy", Retention{Daily: 7, Weekly: 4, Monthly: 6}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.IsZero(); got != tt.want {
				t.Errorf("IsZero() = %v, want %v", got, tt.want)
			}
		})
	}
}

// An empty retention policy must be refused before restic is ever invoked:
// `restic forget` with no --keep-* flags prunes every snapshot in the
// repository, which is not a recoverable mistake.
func TestForgetRefusesEmptyRetention(t *testing.T) {
	r := &Restic{}
	err := r.Forget(context.Background(), Spec{Repo: Repo{Repository: "/tmp/x", Password: "x"}}, Retention{})
	if err == nil {
		t.Fatal("expected an error for an empty retention policy, got nil")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error should explain the refusal, got: %v", err)
	}
}

func TestBackupRejectsNoPaths(t *testing.T) {
	r := &Restic{}
	_, err := r.Backup(context.Background(), Spec{Repo: Repo{Repository: "/tmp/x", Password: "x"}})
	if err == nil {
		t.Fatal("expected an error when no paths are given, got nil")
	}
}

func TestRestoreRequiresSnapshotAndTarget(t *testing.T) {
	r := &Restic{}
	spec := Spec{Repo: Repo{Repository: "/tmp/x", Password: "x"}}
	if err := r.Restore(context.Background(), spec, "", "/tmp/out"); err == nil {
		t.Error("expected an error for an empty snapshot id")
	}
	if err := r.Restore(context.Background(), spec, "abc123", ""); err == nil {
		t.Error("expected an error for an empty target")
	}
}
