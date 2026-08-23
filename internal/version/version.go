// Package version carries the build's identity. Set at link time; the
// fallback is what a plain `go build` or `go run` produces, which is
// exactly when "dev" is the honest answer.
package version

import (
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

// Version is overridden with -ldflags "-X .../internal/version.Version=1.2.3".
var Version = "dev"

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

// Current reads the build's identity, preferring what was stamped at
// link time and falling back to the VCS metadata the toolchain embeds
// on its own. A Docker build has no .git to read, so the stamp is what
// carries there; a local `go build` has no stamp, so the metadata is.
func Current() Build {
	b := Build{
		Version: Version,
		Commit:  Commit,
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
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if b.Commit == "" && len(s.Value) >= 7 {
				b.Commit = s.Value[:7]
			}
		case "vcs.time":
			if b.Built.IsZero() {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
					b.Built = t
				}
			}
		}
	}
	return b
}

// inDocker reports whether this process is running inside a container.
// The marker file is what Docker itself writes; it is a hint for a
// display line, not a security boundary.
func inDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}
