package secret

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := NewBox(mustKey(t))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	plaintext := []byte("a CA private key, in PEM, would go here")
	ct, nonce, err := box.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := box.Open(ct, nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: got %q", got)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	box, _ := NewBox(mustKey(t))
	ct, nonce, err := box.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	other, _ := NewBox(mustKey(t))
	if _, err := other.Open(ct, nonce); err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	box, _ := NewBox(mustKey(t))
	ct, nonce, err := box.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	ct[0] ^= 0xff // flip a bit
	if _, err := box.Open(ct, nonce); err == nil {
		t.Fatal("expected tampered ciphertext to fail authentication")
	}
}

func TestNewBoxRejectsWrongKeySize(t *testing.T) {
	if _, err := NewBox(make([]byte, 16)); err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}

func TestDecodeMasterKey(t *testing.T) {
	if _, err := DecodeMasterKey("not-base64!!"); err == nil {
		t.Error("expected error for invalid base64")
	}
	if _, err := DecodeMasterKey("dG9vc2hvcnQ="); err == nil { // "tooshort"
		t.Error("expected error for wrong-length key")
	}
}
