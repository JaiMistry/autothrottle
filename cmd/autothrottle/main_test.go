package main

import (
	"os"
	"strings"
	"testing"

	"github.com/JaiMistry/autothrottle/internal/auth"
	"github.com/JaiMistry/autothrottle/internal/version"
)

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritable(dir); err != nil {
		t.Fatalf("writable dir rejected: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Error("the write probe was left behind")
	}
}

func TestBanner(t *testing.T) {
	var b strings.Builder
	printBanner(&b, version.Current(), startupInfo{
		DatabasePath: "/data/autothrottle.db", Address: ":7676",
		AuthMode: auth.ModeSetup,
	})
	out := b.String()
	for _, want := range []string{"Intelligently throttles download clients", ":7676", "First run", "not set up yet", "nothing yet"} {
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
