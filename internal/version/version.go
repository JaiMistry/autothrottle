// Package version carries the build's identity. Set at link time; the
// fallback is what a plain `go build` or `go run` produces, which is
// exactly when "dev" is the honest answer.
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

// Label renders the running version for display: "v1.0.0" for a
// release, "dev" for a build with no release number.
//
// The stamp comes straight from the git tag, so it may or may not
// already carry a leading "v" — release.yml passes the ref name
// verbatim, which is "v1.0.0". Trimming before re-adding is what stops
// a real tagged release rendering as "vv1.0.0".
func Label() string {
	v := strings.TrimPrefix(Version, "v")
	if v == "" || v == "dev" {
		return "dev"
	}
	// Only a release number gets dressed up with the "v". A local build
	// off an untagged repo is stamped with what `git describe` could
	// manage — "f3d60f3-dirty" — and printing that as "vf3d60f3-dirty"
	// would assert a release that does not exist.
	if !isReleaseNumber(v) {
		return v
	}
	return "v" + v
}

// isReleaseNumber reports whether a stamp looks like a version rather
// than a commit description. A leading digit is not enough on its own:
// plenty of commit shas start with one, and "5812252-dirty" would then
// be printed as "v5812252-dirty". Requiring digit-dot-digit is what
// separates a release number from a sha, since every tag here is
// semver-shaped.
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
