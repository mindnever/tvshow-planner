// Package secretbox encrypts small secrets (the Jellyfin token) at rest.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

type Box struct{ aead cipher.AEAD }

// New derives an AES-256-GCM key from the configured secret.
func New(secret string) (*Box, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plaintext string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce) // never fails (crypto/rand panics instead)
	return b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
}

func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("secretbox: ciphertext too short")
	}
	pt, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	return string(pt), err
}
