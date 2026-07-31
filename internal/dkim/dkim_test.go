package dkim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateProducesUsableKeyMaterial(t *testing.T) {
	k, err := Generate("example.com", "")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if k.Selector != DefaultSelector {
		t.Errorf("selector = %q, want the default %q", k.Selector, DefaultSelector)
	}
	if !strings.HasPrefix(k.PublicTXT, "v=DKIM1; k=rsa; p=") {
		t.Errorf("public record is not a DKIM TXT value: %q", k.PublicTXT)
	}
	if !strings.Contains(string(k.PrivatePEM), "PRIVATE KEY") {
		t.Error("private key is not PEM-encoded")
	}
}

func TestGenerateRequiresDomain(t *testing.T) {
	if _, err := Generate("", ""); err == nil {
		t.Error("expected an error for an empty domain")
	}
}

// A redelivered mail.create task must not rotate a live key — senders that
// already pass DKIM would start failing.
func TestEnsureKeyIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureKey(dir, "example.com", "")
	if err != nil {
		t.Fatalf("first EnsureKey: %v", err)
	}
	second, err := EnsureKey(dir, "example.com", "")
	if err != nil {
		t.Fatalf("second EnsureKey: %v", err)
	}
	if first.PublicTXT != second.PublicTXT {
		t.Error("EnsureKey rotated the key on a second call; it must reuse the existing one")
	}
}

func TestEnsureKeyWritesRestrictivePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureKey(dir, "example.com", "sel1"); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	info, err := os.Stat(KeyPath(dir, "example.com", "sel1"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	// Windows does not model POSIX bits; the assertion is only meaningful on
	// the platform the agent actually runs on.
	if os.PathSeparator == '/' && info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
}

// A corrupt key file must fail loudly: silently regenerating would publish a
// new public key while mail keeps being signed with something else.
func TestEnsureKeyRejectsCorruptExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := KeyPath(dir, "example.com", "")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a pem key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureKey(dir, "example.com", ""); err == nil {
		t.Fatal("expected an error for a corrupt existing key")
	}
}

func TestPublicTXTFromPrivateRoundTrips(t *testing.T) {
	k, err := Generate("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := PublicTXTFromPrivate(k.PrivatePEM)
	if err != nil {
		t.Fatalf("PublicTXTFromPrivate: %v", err)
	}
	if got != k.PublicTXT {
		t.Error("recovered public record does not match the generated one")
	}
}

func TestRecordName(t *testing.T) {
	if got := RecordName("example.com", ""); got != "cypher._domainkey.example.com" {
		t.Errorf("RecordName default = %q", got)
	}
	if got := RecordName("example.com", "s2"); got != "s2._domainkey.example.com" {
		t.Errorf("RecordName custom = %q", got)
	}
}

// A 2048-bit key's record exceeds the 255-byte DNS character-string limit, so
// it must be published as several quoted chunks or resolvers reject it.
func TestSplitTXTChunksLongValues(t *testing.T) {
	short := SplitTXT("v=spf1 -all")
	if short != `"v=spf1 -all"` {
		t.Errorf("a short value should be a single quoted string, got %s", short)
	}

	k, err := Generate("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(k.PublicTXT) <= 255 {
		t.Fatalf("expected a 2048-bit DKIM record to exceed 255 bytes, got %d", len(k.PublicTXT))
	}
	out := SplitTXT(k.PublicTXT)
	if strings.Count(out, `"`)/2 < 2 {
		t.Errorf("long value was not split into multiple strings: %s", out)
	}
	for _, chunk := range strings.Split(out, `" "`) {
		if len(strings.Trim(chunk, `"`)) > 255 {
			t.Errorf("chunk exceeds the 255-byte limit: %d bytes", len(chunk))
		}
	}
	// Reassembling the chunks must give back exactly the original value.
	if rejoined := strings.ReplaceAll(strings.Trim(out, `"`), `" "`, ""); rejoined != k.PublicTXT {
		t.Error("splitting the record lost or altered data")
	}
}

func TestWriteRspamdConfigIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "keys")
	localDir := filepath.Join(dir, "local.d")

	if err := WriteRspamdConfig(localDir, keyDir, ""); err != nil {
		t.Fatalf("WriteRspamdConfig: %v", err)
	}
	path := filepath.Join(localDir, "dkim_signing.conf")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(first), keyDir) {
		t.Error("config does not reference the key directory")
	}

	if err := WriteRspamdConfig(localDir, keyDir, ""); err != nil {
		t.Fatalf("second WriteRspamdConfig: %v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Error("config changed on an identical second write")
	}
}
