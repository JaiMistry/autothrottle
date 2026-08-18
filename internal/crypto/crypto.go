// Package crypto encrypts credentials for connected services (media
// server tokens, downloader passwords) at rest. Never store these
// plaintext — see plan.md's Security section for why.
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

// LoadOrGenerateKey returns the encryption key from AUTOTHROTTLE_ENCRYPTION_KEY
// if set (base64-encoded, 32 bytes decoded), otherwise loads a
// previously-generated key from keyPath, or generates and persists a new
// one there (mode 0600) if neither exists.
func LoadOrGenerateKey(envValue, keyPath string) ([]byte, error) {
	if envValue != "" {
		key, err := base64.StdEncoding.DecodeString(envValue)
		if err != nil {
			return nil, fmt.Errorf("decoding AUTOTHROTTLE_ENCRYPTION_KEY: %w", err)
		}
		if len(key) != keySize {
			return nil, fmt.Errorf("AUTOTHROTTLE_ENCRYPTION_KEY must decode to %d bytes, got %d", keySize, len(key))
		}
		return key, nil
	}

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

// Encrypt returns AES-256-GCM ciphertext, base64-encoded for storage as
// SQLite TEXT. The nonce is prepended to the ciphertext.
func Encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
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
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
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
