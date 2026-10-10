package version

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestLabel(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	cases := []struct{ stamp, want string }{
		{"1.0.0", "v1.0.0"},
		// release.yml stamps the git ref verbatim, which carries the "v".
		{"v1.0.0", "v1.0.0"},
		{"dev", "dev"},
		{"", "dev"},
		// Untagged builds are git describe output, not releases — even a
		// sha that starts with a digit.
		{"f3d60f3-dirty", "f3d60f3-dirty"},
		{"5812252-dirty", "5812252-dirty"},
		{"5812252", "5812252"},
		{"v1.0.0-3-gabc1234", "v1.0.0-3-gabc1234"},
	}
	for _, c := range cases {
		Version = c.stamp
		if got := Label(); got != c.want {
			t.Errorf("Label() with Version=%q = %q, want %q", c.stamp, got, c.want)
		}
	}
}

func TestNumber(t *testing.T) {
	original := Version
	defer func() { Version = original }()

	for stamp, want := range map[string]string{
		"v1.2.3":        "1.2.3",
		"1.2.3":         "1.2.3",
		"dev":           "",
		"f3d60f3-dirty": "",
		"5812252-dirty": "",
		"5812252":       "",
	} {
		Version = stamp
		if got := Number(); got != want {
			t.Errorf("Number() with Version=%q = %q, want %q", stamp, got, want)
		}
	}
}

// The link-time stamp wins; an unreadable one is left blank.
func TestCurrentReadsTheBuildDate(t *testing.T) {
	original := BuildDate
	defer func() { BuildDate = original }()

	BuildDate = "2026-10-01T12:00:00Z"
	if b := Current(); b.Built.Format("2006-01-02") != "2026-10-01" || b.Go == "" {
		t.Errorf("Current() = %+v", b)
	}
	BuildDate = "yesterday"
	if b := Current(); b.Built.Year() == 2026 && b.Built.Month() == 10 && b.Built.Day() == 1 {
		t.Errorf("an unreadable stamp was parsed: %v", b.Built)
	}
}

// A plain go build carries its commit and time in the VCS metadata; the
// link-time stamp, where set, wins.
func TestFromVCS(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.time", Value: "2026-10-01T12:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}
	var b Build
	fromVCS(&b, settings)
	if b.Commit != "0123456" || b.Built.Day() != 1 {
		t.Errorf("from VCS: %+v", b)
	}

	stamped := Build{Commit: "stamped", Built: time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)}
	fromVCS(&stamped, settings)
	if stamped.Commit != "stamped" || stamped.Built.Year() != 2025 {
		t.Errorf("the stamp was overwritten: %+v", stamped)
	}
}

// A release stamps the full revision; it is shown as git shows it.
func TestCurrentShortensTheCommit(t *testing.T) {
	original := Commit
	defer func() { Commit = original }()

	Commit = "fad935196f6b46ee9bb575b81d46d338cb72f112"
	if got := Current().Commit; got != "fad9351" {
		t.Errorf("Commit = %q, want fad9351", got)
	}
	Commit = "abc12"
	if got := Current().Commit; got != "abc12" {
		t.Errorf("a short stamp became %q", got)
	}
}
