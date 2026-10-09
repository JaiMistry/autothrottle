package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// defaultID is the first regular user on most Linux hosts, which is
// usually who owns the directory being mounted.
const defaultID = 1000

// dropPrivileges runs before anything else does. Started as root, it
// hands the data directory to PUID:PGID and then becomes that user for
// the rest of the process's life — the same contract as the linuxserver
// images people already know, done here because this image has no
// shell to do it in an entrypoint script.
//
// Root is held only for the ownership fix. Nothing has opened the
// database or a socket yet, and after this returns the process cannot
// get root back.
//
// Started as anyone else — a compose file with `user:`, or a
// rootless runtime — it changes nothing: whoever it already is, is who
// it stays.
func dropPrivileges(dataDir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	uid, err := idFromEnv("PUID")
	if err != nil {
		return err
	}
	gid, err := idFromEnv("PGID")
	if err != nil {
		return err
	}
	if uid == 0 || gid == 0 {
		return fmt.Errorf("PUID and PGID must not be 0: running as root would defeat the point of setting them")
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dataDir, err)
	}
	if err := chownTree(dataDir, uid, gid); err != nil {
		return err
	}

	// Supplementary groups first, then group, then user: once the user
	// changes, the process no longer has the right to change the others.
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("dropping supplementary groups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("switching to group %d: %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("switching to user %d: %w", uid, err)
	}
	return nil
}

func idFromEnv(name string) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return defaultID, nil
	}
	id, err := strconv.Atoi(raw)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("%s must be a number, got %q", name, raw)
	}
	return id, nil
}

// chownTree gives everything under dir to uid:gid, touching only what is
// owned by someone else — so a restart with nothing to fix costs one
// stat per file rather than a write.
func chownTree(dir string, uid, gid int) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid && int(st.Gid) == gid {
			return nil
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return fmt.Errorf("handing %s to %d:%d: %w", path, uid, gid, err)
		}
		return nil
	})
}

// restrictDataDir keeps the data directory to its owner. The database
// holds the account's password hash and every connection's encrypted
// credential, and the key that decrypts them sits beside it; none of it
// is anyone else's business on a shared host. The umask covers what
// SQLite creates later (its -wal and -shm files appear on demand).
func restrictDataDir(dataDir string) error {
	syscall.Umask(0o077)
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return fmt.Errorf("restricting %s: %w", dataDir, err)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dataDir, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(filepath.Join(dataDir, e.Name()), info.Mode().Perm()&0o700); err != nil {
				return fmt.Errorf("restricting %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}
