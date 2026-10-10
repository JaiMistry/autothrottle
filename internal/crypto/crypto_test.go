package crypto

import (
	"encoding/base64"
	"os"
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
	key2[0] = 1

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

	key1, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("first LoadOrGenerateKey: %v", err)
	}
	if len(key1) != keySize {
		t.Fatalf("got key of length %d, want %d", len(key1), keySize)
	}

	key2, err := LoadOrGenerateKey(keyPath)
	if err != nil {
		t.Fatalf("second LoadOrGenerateKey: %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call should load the persisted key, not generate a new one")
	}
}

// A key file that is damaged, the wrong size or unreadable stops start-up
// rather than being replaced: replacing it would orphan every credential.
func TestLoadOrGenerateKeyRefusesABadKey(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, path := range map[string]string{
		"not base64":      write("garbled", "%%%"),
		"wrong size":      write("short", base64.StdEncoding.EncodeToString([]byte("short"))),
		"a directory":     dir,
		"unwritable path": filepath.Join(dir, "missing", "key"),
	} {
		if _, err := LoadOrGenerateKey(path); err == nil {
			t.Errorf("%s: loaded a key", name)
		}
	}
}

func TestEncryptAndDecryptRejectBadInput(t *testing.T) {
	key := make([]byte, keySize)
	if _, err := Encrypt([]byte("short"), "x"); err == nil {
		t.Error("encrypted with a key of the wrong size")
	}
	sealed, _ := Encrypt(key, "x")
	for name, call := range map[string]func() error{
		"not base64": func() error { _, err := Decrypt(key, "%%%"); return err },
		"bad key":    func() error { _, err := Decrypt([]byte("short"), sealed); return err },
		"too short":  func() error { _, err := Decrypt(key, base64.StdEncoding.EncodeToString([]byte("abc"))); return err },
	} {
		if call() == nil {
			t.Errorf("%s: decrypted", name)
		}
	}
}
