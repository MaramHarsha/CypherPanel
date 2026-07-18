package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testIdentity() *Identity {
	return &Identity{
		ServerID:  "srv_test",
		NATSURL:   "tls://plane.example.com:4222",
		CertPEM:   []byte("cert-pem"),
		KeyPEM:    []byte("key-pem"),
		CACertPEM: []byte("ca-pem"),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := testIdentity()
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ServerID != want.ServerID || got.NATSURL != want.NATSURL {
		t.Errorf("meta round-trip: got %+v", got)
	}
	for _, c := range []struct {
		name      string
		got, want []byte
	}{
		{"cert", got.CertPEM, want.CertPEM},
		{"key", got.KeyPEM, want.KeyPEM},
		{"ca", got.CACertPEM, want.CACertPEM},
	} {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s round-trip: got %q want %q", c.name, c.got, c.want)
		}
	}
}

// TestPrivateKeyPermissions: the key never leaves this host and must not be
// world-readable (threat-model §5.1).
func TestPrivateKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, testIdentity()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "agent-key.pem"))
	if err != nil {
		t.Fatalf("Stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key permissions = %o, want 600", perm)
	}
}

func TestLoadWithoutEnrollmentFails(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Load on empty dir succeeded; want error pointing at enroll")
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("Exists true on empty dir")
	}
	if err := Save(dir, testIdentity()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !Exists(dir) {
		t.Error("Exists false after Save")
	}
}
