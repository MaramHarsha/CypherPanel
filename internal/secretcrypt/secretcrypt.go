// Package secretcrypt provides authenticated symmetric encryption (AES-256-GCM)
// for secrets that must be stored recoverably — e.g. hosted-account database
// passwords the panel has to display or re-use. These cannot be hashed (we need
// the plaintext back), so they are encrypted at rest with an operator-held key,
// never written to a plaintext column.
package secretcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts/decrypts with a fixed 32-byte key using AES-256-GCM. The
// output layout is nonce || ciphertext(+tag), so each ciphertext is
// self-describing and safe to store as an opaque blob.
type Cipher struct {
	aead cipher.AEAD
}

// New builds a Cipher from a 32-byte key.
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secretcrypt: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretcrypt: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretcrypt: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns nonce||ciphertext for plaintext.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secretcrypt: nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ct.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(data []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("secretcrypt: ciphertext too short")
	}
	nonce, ct := data[:ns], data[ns:]
	plaintext, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("secretcrypt: decrypt: %w", err)
	}
	return plaintext, nil
}
