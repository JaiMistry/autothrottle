package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// syncBuffer is a log both run and the test touch.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func clearAuthEnv(t *testing.T) {
	for _, k := range []string{"AUTH_USERNAME", "AUTH_PASSWORD", "AUTH_DISABLED"} {
		t.Setenv(k, "")
	}
}

var listening = regexp.MustCompile(`msg=listening addr=(\S+)`)

// A full start: the banner, the web UI answering, and on shutdown a
// clean exit with this run's stop time recorded.
func TestRunServesUntilStopped(t *testing.T) {
	clearAuthEnv(t)
	dir := t.TempDir()
	var logs, banner syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, slog.New(slog.NewTextHandler(&logs, nil)), dir, "127.0.0.1:0", &banner) }()

	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		if time.Now().After(deadline) {
			t.Fatalf("never listened; log:\n%s", logs.String())
		}
		if m := listening.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, err := http.Get("http://" + addr + "/setup")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("first-run setup answered %d", resp.StatusCode)
	}
	// Logged, which announces itself to open pages.
	resp, err = http.Get("http://" + addr + "/no-such-page")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(banner.String(), "First run") {
		t.Errorf("banner:\n%s", banner.String())
	}

	// Stored to the millisecond.
	stopped := time.Now().Truncate(time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after its context ended")
	}

	database, err := db.Open(filepath.Join(dir, "autothrottle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	runs, err := database.Runs(stopped.Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(runs) != 1 || runs[0].End.Before(stopped) {
		t.Errorf("the run's stop was not recorded: %+v, %v", runs, err)
	}
	for _, name := range []string{"autothrottle.db", "encryption.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Each thing startup needs, missing, stops it with a reason.
func TestRunStartupFailures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()

	cases := []struct {
		name, want string
		prepare    func(t *testing.T, dir string) (dataDir, addr string)
	}{
		{"data directory is a file", "not usable", func(t *testing.T, dir string) (string, string) {
			file := filepath.Join(dir, "file")
			_ = os.WriteFile(file, nil, 0o600)
			return file, "127.0.0.1:0"
		}},
		{"database is not a database", "opening database", func(t *testing.T, dir string) (string, string) {
			_ = os.WriteFile(filepath.Join(dir, "autothrottle.db"), bytes.Repeat([]byte("not sqlite "), 200), 0o600)
			return dir, "127.0.0.1:0"
		}},
		{"half a credential pair", "loading auth config", func(t *testing.T, dir string) (string, string) {
			t.Setenv("AUTH_USERNAME", "admin")
			return dir, "127.0.0.1:0"
		}},
		{"damaged key", "loading encryption key", func(t *testing.T, dir string) (string, string) {
			_ = os.WriteFile(filepath.Join(dir, "encryption.key"), []byte("%%%"), 0o600)
			return dir, "127.0.0.1:0"
		}},
		{"port taken", "listening on", func(t *testing.T, dir string) (string, string) {
			return dir, busy.Addr().String()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearAuthEnv(t)
			dataDir, addr := c.prepare(t, t.TempDir())
			err := run(context.Background(), logger, dataDir, addr, &syncBuffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestCountRowsReadsAFailureAsZero(t *testing.T) {
	if got := countRows([]int{1, 2}, nil); got != 2 {
		t.Errorf("countRows = %d", got)
	}
	if got := countRows([]int{1}, errors.New("unreadable")); got != 0 {
		t.Errorf("countRows on an error = %d", got)
	}
}

func TestBannerAuthModes(t *testing.T) {
	for mode, want := range map[auth.Mode]string{
		auth.ModeDisabled: "anyone who can reach this port",
		auth.ModeAccount:  "account jai",
	} {
		if got := authDesc(startupInfo{AuthMode: mode, AuthUser: "jai"}); !strings.Contains(got, want) {
			t.Errorf("authDesc(%v) = %q", mode, got)
		}
	}
	if buildDate(time.Time{}) != "" || buildDate(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) == "" {
		t.Error("buildDate prints a missing date, or drops a real one")
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
