// Package crypto encrypts credentials for connected services (media
// server tokens, downloader passwords) at rest.
//
// Never store these plaintext. They grant control of the services they
// belong to, and this app's database is a single file on a home server
// — copied into backups, moved between hosts, and handled far more
// casually than anything that looks like a credential store.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

const keySize = 32 // AES-256

// LoadOrGenerateKey loads the key stored at keyPath, or generates one
// and saves it there (mode 0600) on first run.
//
// Deliberately not configurable. The key lives beside the database, so
// it protects stored credentials from a copy of the database file taken
// without it — a backup job that only grabs *.db files, a database
// attached to a bug report — and it does that without anyone having to
// know it exists.
func LoadOrGenerateKey(keyPath string) ([]byte, error) {
	if existing, err := os.ReadFile(keyPath); err == nil {
		key, err := base64.StdEncoding.DecodeString(string(existing))
		if err != nil {
			return nil, fmt.Errorf("decoding persisted key at %s: %w", keyPath, err)
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("persisted key at %s is the wrong size", keyPath)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", keyPath, err)
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("persisting generated key to %s: %w", keyPath, err)
	}
	return key, nil
}

// newGCM builds the AEAD both directions use, so the cipher choice is
// made in exactly one place.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt returns AES-256-GCM ciphertext, base64-encoded for storage as
// SQLite TEXT. The nonce is prepended to the ciphertext.
func Encrypt(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func Decrypt(key []byte, encoded string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
