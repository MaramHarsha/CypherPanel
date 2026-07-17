// Package secret provides authenticated symmetric encryption for data at rest.
//
// Its first use is the control-plane CA private key (asset A2, threat-model
// §5.1): the key is sealed with a master key supplied out-of-band via config
// (CYPHERD_MASTER_KEY) and never stored alongside the ciphertext it protects,
// so a database breach alone cannot recover it.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// KeySize is the required master-key length in bytes (AES-256).
const KeySize = 32

// Box seals and opens byte slices with AES-256-GCM (authenticated encryption).
type Box struct {
	aead cipher.AEAD
}

// NewBox constructs a Box from a 32-byte master key.
func NewBox(masterKey []byte) (*Box, error) {
	if len(masterKey) != KeySize {
		return nil, fmt.Errorf("secret: master key must be %d bytes, got %d", KeySize, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("secret: constructing cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: constructing GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, returning the ciphertext and the fresh random nonce
// used. Store both; the nonce is not secret.
func (b *Box) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secret: generating nonce: %w", err)
	}
	return b.aead.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Open decrypts and authenticates ciphertext sealed with the same master key
// and the given nonce. A wrong key or tampered ciphertext yields an error.
func (b *Box) Open(ciphertext, nonce []byte) ([]byte, error) {
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secret: opening ciphertext: %w", err)
	}
	return plaintext, nil
}

// DecodeMasterKey parses a standard-base64-encoded 32-byte master key.
func DecodeMasterKey(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("secret: decoding master key: %w", err)
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("secret: master key decodes to %d bytes, want %d", len(raw), KeySize)
	}
	return raw, nil
}
