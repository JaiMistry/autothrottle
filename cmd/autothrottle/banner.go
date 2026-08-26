package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JaiMistry/autothrottle/internal/version"
)

// bannerArt is "AutoThrottle" in figlet's slant font, kept under 62
// columns so it does not wrap in a default-width terminal.
const bannerArt = `
    ___         __      ________               __  __  __
   /   | __  __/ /_____/_  __/ /_  _________  / /_/ /_/ /__
  / /| |/ / / / __/ __ \/ / / __ \/ ___/ __ \/ __/ __/ / _ \
 / ___ / /_/ / /_/ /_/ / / / / / / /  / /_/ / /_/ /_/ /  __/
/_/  |_\__,_/\__/\____/_/ /_/ /_/_/   \____/\__/\__/_/\___/
`

// startupInfo is what the banner reports beyond the build itself: where
// this instance keeps its data, how it is reachable, and what it found
// configured. Enough to tell a healthy start from a confused one
// without opening the UI.
type startupInfo struct {
	DatabasePath string
	Address      string
	AuthDisabled bool
	MediaServers int
	Downloaders  int
	Mappings     int
}

// printBanner writes the block that opens the container log.
//
// Structured slog lines are right for events, and wrong for this: on
// startup you want the version, the build and what was found, laid out
// to be read at a glance rather than parsed. Everything after this line
// is structured as usual.
func printBanner(out io.Writer, build version.Build, info startupInfo) {
	var b strings.Builder
	b.WriteString(bannerArt)
	b.WriteString("  Throttles your download clients while something is playing.\n\n")

	runtimeDesc := fmt.Sprintf("%s %s/%s", build.Go, build.OS, build.Arch)
	if build.Docker {
		runtimeDesc += " (container)"
	}

	row := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "  %-11s %s\n", label, value)
	}
	row("Version", version.Label())
	row("Commit", build.Commit)
	row("Built", buildDate(build.Built))
	row("Runtime", runtimeDesc)
	row("Database", info.DatabasePath)
	row("Listening", info.Address)
	row("Auth", authDesc(info.AuthDisabled))
	row("Configured", configuredDesc(info))
	b.WriteString("\n")

	// Assembled first and written once. A failed write to stdout is not
	// something a banner can do anything about, and it is the only error
	// here worth naming: everything above writes to a strings.Builder,
	// which cannot fail.
	_, _ = io.WriteString(out, b.String())
}

func buildDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2 Jan 2006 15:04 MST")
}

func authDesc(disabled bool) string {
	if disabled {
		return "disabled — anyone who can reach this port has full access"
	}
	return "enabled"
}

// configuredDesc reads "3 media servers · 2 downloaders · 5 mappings",
// or says plainly that there is nothing to act on yet, which is the one
// case where a fresh install looks broken but is not.
func configuredDesc(info startupInfo) string {
	if info.MediaServers == 0 && info.Downloaders == 0 {
		return "nothing yet — add a media server and a downloader to begin"
	}
	parts := []string{
		count(info.MediaServers, "media server"),
		count(info.Downloaders, "downloader"),
		count(info.Mappings, "mapping"),
	}
	return strings.Join(parts, " · ")
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
