// Package secretbox seals short secrets (Shopify access tokens) for storage at
// rest with AES-256-GCM, replacing print-queue-be's Fernet. The key is any string
// from config; it is hashed to a 32-byte AES key so any passphrase length works.
// Ciphertext is self-describing: base64(nonce || ciphertext || tag).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// ErrEmptyKey is returned when no encryption key is configured.
var ErrEmptyKey = errors.New("secretbox: encryption key is empty")

// Box seals and opens secrets with a fixed key.
type Box struct {
	key [32]byte
}

// New derives a Box from a key string. An empty key is rejected so a
// misconfiguration cannot silently store tokens in the clear.
func New(key string) (*Box, error) {
	if key == "" {
		return nil, ErrEmptyKey
	}
	return &Box{key: sha256.Sum256([]byte(key))}, nil
}

// Seal encrypts plaintext and returns a base64 token.
func (b *Box) Seal(plaintext string) (string, error) {
	gcm, err := b.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal, returning the plaintext. A tampered or wrong-key token
// fails authentication and returns an error.
func (b *Box) Open(token string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	gcm, err := b.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("secretbox: ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (b *Box) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
