package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

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

// Not running as root — as in this test — the privilege drop must
// leave everything exactly as it was.
func TestDropPrivilegesIsANoOpWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this covers the non-root path")
	}
	dir := t.TempDir()
	if err := dropPrivileges(dir); err != nil {
		t.Fatalf("dropPrivileges as non-root: %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritable(dir); err != nil {
		t.Fatalf("writable dir rejected: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("the write probe was left behind")
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		_ = os.Mkdir(ro, 0o500)
		if err := checkWritable(ro); err == nil {
			t.Error("a read-only dir passed")
		}
	}
}

func TestBanner(t *testing.T) {
	var b strings.Builder
	printBanner(&b, version.Current(), startupInfo{
		DatabasePath: "/data/autothrottle.db", Address: ":7676",
		AuthMode: auth.ModeSetup,
	})
	out := b.String()
	for _, want := range []string{"Throttles your download clients", ":7676", "First run", "not set up yet", "nothing yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner is missing %q", want)
		}
	}

	b.Reset()
	printBanner(&b, version.Current(), startupInfo{AuthMode: auth.ModeEnvironment, AuthUser: "jai", MediaServers: 2, Downloaders: 1, Mappings: 1})
	out = b.String()
	if strings.Contains(out, "First run") {
		t.Error("the setup box printed when setup is not pending")
	}
	if !strings.Contains(out, "2 media servers · 1 downloader · 1 mapping") {
		t.Errorf("configured line wrong:\n%s", out)
	}
}
