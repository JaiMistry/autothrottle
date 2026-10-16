//go:build !windows

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

func TestIDFromEnv(t *testing.T) {
	t.Setenv("PUID", "")
	if id, err := idFromEnv("PUID"); err != nil || id != defaultID {
		t.Errorf("unset PUID = %d %v, want %d", id, err, defaultID)
	}
	t.Setenv("PUID", "1234")
	if id, _ := idFromEnv("PUID"); id != 1234 {
		t.Errorf("PUID=1234 read as %d", id)
	}
	for _, bad := range []string{"abc", "-1", "1.5"} {
		t.Setenv("PUID", bad)
		if _, err := idFromEnv("PUID"); err == nil {
			t.Errorf("PUID=%q was accepted", bad)
		}
	}
}

// Without root the privilege drop changes nothing.
func TestDropPrivilegesIsANoOpWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this covers the non-root path")
	}
	dir := t.TempDir()
	if err := dropPrivileges(dir); err != nil {
		t.Fatalf("dropPrivileges as non-root: %v", err)
	}
}

// chownTree hands over only what someone else owns, and reports what it
// cannot.
func TestChownTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := chownTree(dir, os.Getuid(), os.Getgid()); err != nil {
		t.Errorf("handing a tree to its owner: %v", err)
	}
	if err := chownTree(filepath.Join(dir, "missing"), os.Getuid(), os.Getgid()); err == nil {
		t.Error("walking a missing directory succeeded")
	}
	if os.Geteuid() != 0 {
		if err := chownTree(dir, os.Getuid()+1, os.Getgid()+1); err == nil {
			t.Error("a non-root user gave files away")
		}
	}
}

func TestRestrictDataDirFailures(t *testing.T) {
	if err := restrictDataDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("restricting a missing directory succeeded")
	}
	// Subdirectories are left alone.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Past whatever umask an earlier test left.
	if err := os.Chmod(filepath.Join(dir, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restrictDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "backups")); info.Mode().Perm() != 0o755 {
		t.Errorf("a subdirectory was changed to %v", info.Mode().Perm())
	}
}

// Unix permissions only: Windows ignores the mode bits on a directory.
func TestCheckWritableReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	_ = os.Mkdir(ro, 0o500)
	if err := checkWritable(ro); err == nil {
		t.Error("a read-only dir passed")
	}
}
