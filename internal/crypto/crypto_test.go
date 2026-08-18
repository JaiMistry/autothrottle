package crypto

import (
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(i)
	}

	const secret = "sk-plex-token-abc123"
	ciphertext, err := Encrypt(key, secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == secret {
		t.Fatal("ciphertext must not equal plaintext")
	}

	plaintext, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plaintext != secret {
		t.Fatalf("round trip mismatch: got %q, want %q", plaintext, secret)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	key1 := make([]byte, keySize)
	key2 := make([]byte, keySize)
	key2[0] = 1 // differ from key1

	ciphertext, err := Encrypt(key1, "secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(key2, ciphertext); err == nil {
		t.Fatal("expected decryption with wrong key to fail")
	}
}

func TestLoadOrGenerateKey_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encryption.key")

	key1, err := LoadOrGenerateKey("", keyPath)
	if err != nil {
		t.Fatalf("first LoadOrGenerateKey: %v", err)
	}
	if len(key1) != keySize {
		t.Fatalf("got key of length %d, want %d", len(key1), keySize)
	}

	key2, err := LoadOrGenerateKey("", keyPath)
	if err != nil {
		t.Fatalf("second LoadOrGenerateKey: %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call should load the persisted key, not generate a new one")
	}
}

func TestLoadOrGenerateKey_FromEnv(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encryption.key")

	// A key that is NOT 32 bytes when decoded should be rejected.
	if _, err := LoadOrGenerateKey("dG9vc2hvcnQ=", keyPath); err == nil {
		t.Fatal("expected error for wrong-length env key")
	}
}
