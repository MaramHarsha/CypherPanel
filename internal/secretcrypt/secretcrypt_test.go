package secretcrypt

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	c, err := New(newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("s3cr3t-db-password!")
	ct, err := c.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, secret) {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := c.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round trip mismatch: %q != %q", got, secret)
	}
}

func TestEncryptIsNonDeterministic(t *testing.T) {
	c, _ := New(newKey(t))
	a, _ := c.Encrypt([]byte("same"))
	b, _ := c.Encrypt([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext must differ (random nonce)")
	}
}

func TestBadKeySize(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}

func TestTamperDetected(t *testing.T) {
	c, _ := New(newKey(t))
	ct, _ := c.Encrypt([]byte("data"))
	ct[len(ct)-1] ^= 0xff // flip a tag bit
	if _, err := c.Decrypt(ct); err == nil {
		t.Fatal("tampered ciphertext must fail authentication")
	}
}
