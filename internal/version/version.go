// Package version carries the build's identity, set at link time.
package version

import (
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Version is overridden with -ldflags "-X .../internal/version.Version=1.2.3".
var Version = "dev"

// Label renders the version for display: "v1.0.0" for a release, the
// stamp as is otherwise. Tags already carry the "v", so it is trimmed
// before being added.
func Label() string {
	v := strings.TrimPrefix(Version, "v")
	if v == "" || v == "dev" {
		return "dev"
	}
	// Only a release number gets the "v"; an untagged build's stamp
	// ("f3d60f3-dirty") is not a release.
	if !isReleaseNumber(v) {
		return v
	}
	return "v" + v
}

// isReleaseNumber tells a release number from a commit description by
// digit-dot-digit: shas can start with a digit too.
func isReleaseNumber(v string) bool {
	dot := strings.IndexByte(v, '.')
	if dot <= 0 || dot == len(v)-1 {
		return false
	}
	return isDigit(v[0]) && isDigit(v[dot+1])
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Number is the release number with no leading "v" and no decoration,
// for comparing against a release tag. Empty for an unstamped build.
func Number() string {
	v := strings.TrimPrefix(Version, "v")
	if !isReleaseNumber(v) {
		return ""
	}
	return v
}

// Commit is the short revision the binary was built from, when known.
var Commit = ""

// BuildDate is RFC 3339, stamped at link time when known.
var BuildDate = ""

// Build describes the running binary for the About card.
type Build struct {
	Version string
	Commit  string
	Built   time.Time
	Go      string
	OS      string
	Arch    string
	Docker  bool
}

// Current reads the build's identity: the link-time stamp (Docker
// builds), else the VCS metadata the toolchain embeds (plain go build).
func Current() Build {
	b := Build{
		Version: Version,
		Commit:  shortCommit(Commit),
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Docker:  inDocker(),
	}
	if BuildDate != "" {
		if t, err := time.Parse(time.RFC3339, BuildDate); err == nil {
			b.Built = t
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		fromVCS(&b, info.Settings)
	}
	return b
}

// fromVCS fills what the link-time stamp left blank from the VCS
// metadata a plain go build embeds.
func fromVCS(b *Build, settings []debug.BuildSetting) {
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			if b.Commit == "" {
				b.Commit = shortCommit(s.Value)
			}
		case "vcs.time":
			if b.Built.IsZero() {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
					b.Built = t
				}
			}
		}
	}
}

// shortCommit is the seven-character form git shows. A release stamps
// the full revision; anything shorter is kept as it is.
func shortCommit(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// inDocker reports whether this runs in a container, from the marker
// file Docker writes. A display hint only.
func inDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}
