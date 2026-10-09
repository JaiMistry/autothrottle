package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The database and key are kept from other users on the host.
func TestRestrictDataDir(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"autothrottle.db", "autothrottle.db-wal", "encryption.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := restrictDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("directory is %v", info.Mode().Perm())
	}
	for _, name := range []string{"autothrottle.db", "autothrottle.db-wal", "encryption.key"} {
		if info, _ := os.Stat(filepath.Join(dir, name)); info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", name, info.Mode().Perm())
		}
	}
	// Files made afterwards, as SQLite's are, start private.
	if err := os.WriteFile(filepath.Join(dir, "autothrottle.db-shm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "autothrottle.db-shm")); info.Mode().Perm() != 0o600 {
		t.Errorf("a new file is %v", info.Mode().Perm())
	}
}
